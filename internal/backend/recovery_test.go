package backend

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"mlink/internal/broker"
	"mlink/internal/config"
	"mlink/internal/journal"
	"mlink/internal/model"
	"mlink/internal/provider/host"
	"mlink/internal/provider/manifest"
	"mlink/internal/provider/tencentdb"
)

type recoverySession struct {
	fakeSession
	faulted  atomic.Bool
	writes   atomic.Int32
	archives atomic.Int32
}

func (s *recoverySession) Capabilities() map[string]manifest.CapabilityDescriptor {
	return map[string]manifest.CapabilityDescriptor{"archive_session": {}}
}

func (s *recoverySession) ArchiveSession(context.Context, host.CallMeta, model.IdentityScope) error {
	s.archives.Add(1)
	return nil
}

func (s *recoverySession) State() host.State {
	if s.faulted.Load() {
		return host.StateFaulted
	}
	return host.StateReady
}

func (s *recoverySession) CaptureTurn(context.Context, host.CallMeta, model.Turn) (model.WriteReceipt, error) {
	s.writes.Add(1)
	return model.WriteReceipt{ReceiptID: "receipt", State: model.WriteAccepted}, nil
}

func recoveryConnection() config.Connection {
	return config.Connection{
		ID: "local", ProviderID: "dev.mlink.tencentdb", ProviderVersion: "0.1.0", ConfigRevision: "rev-1",
		ProviderConfig: map[string]any{"base_url": "http://127.0.0.1:8420"},
		SecretRefs:     map[string]string{"token": "keychain://dev.mlink/connection/local/token"},
	}
}

func TestRuntimeRecoversOnePinnedSessionForConcurrentCalls(t *testing.T) {
	var sessions []*recoverySession
	r := NewRuntime(RuntimeConfig{
		Manifest: tencentdb.BundledManifest(),
		Secrets:  fakeSecretStore{values: map[string][]byte{"connection/local/token": []byte("token")}},
		StartProvider: func(_ context.Context, cfg host.Config) (host.Session, error) {
			if string(cfg.Snapshot.Config()) != `{"base_url":"http://127.0.0.1:8420"}` {
				t.Errorf("recovery changed pinned config: %s", cfg.Snapshot.Config())
			}
			s := &recoverySession{fakeSession: fakeSession{route: cfg.Snapshot.RouteKey()}}
			sessions = append(sessions, s)
			return s, nil
		},
	})
	cfg := recoveryConnection()
	first, err := r.Start(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ProviderConfig["base_url"] = "http://different.invalid"
	cfg.SecretRefs["token"] = "keychain://dev.mlink/missing"
	sessions[0].faulted.Store(true)
	var wg sync.WaitGroup
	for i := range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var err error
			if i%3 == 0 {
				_, err = r.Recall(context.Background(), first.RouteKey(), host.CallMeta{}, model.RecallRequest{})
			} else if i%3 == 1 {
				err = r.ArchiveSession(context.Background(), first.RouteKey(), host.CallMeta{}, model.IdentityScope{})
			} else {
				_, err = r.CaptureTurn(context.Background(), first.RouteKey(), host.CallMeta{}, model.Turn{})
			}
			if err != nil {
				t.Errorf("call after exit: %v", err)
			}
		}()
	}
	wg.Wait()
	if len(sessions) != 2 {
		t.Fatalf("started %d processes, want 2", len(sessions))
	}
	if sessions[0].writes.Load() != 0 || sessions[1].writes.Load() != 4 || sessions[1].archives.Load() != 4 {
		t.Fatal("writes were duplicated or sent to dead session")
	}
	if err := r.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Recall(context.Background(), first.RouteKey(), host.CallMeta{}, model.RecallRequest{}); !errors.Is(err, ErrConnectionUnavailable) {
		t.Fatalf("recall after shutdown: %v", err)
	}
	if len(sessions) != 2 {
		t.Fatal("shutdown resurrected provider")
	}
}

func TestWorkerRetriesFailedProviderRestartWithoutSendingTwice(t *testing.T) {
	starts := 0
	var first, recovered *recoverySession
	r := NewRuntime(RuntimeConfig{
		Manifest: tencentdb.BundledManifest(),
		Secrets:  fakeSecretStore{values: map[string][]byte{"connection/local/token": []byte("token")}},
		StartProvider: func(_ context.Context, cfg host.Config) (host.Session, error) {
			starts++
			if starts == 2 {
				return nil, errors.New("provider launch failed")
			}
			s := &recoverySession{fakeSession: fakeSession{route: cfg.Snapshot.RouteKey()}}
			if starts == 1 {
				first = s
			} else {
				recovered = s
			}
			return s, nil
		},
	})
	s, err := r.Start(context.Background(), recoveryConnection())
	if err != nil {
		t.Fatal(err)
	}
	defer r.Shutdown(context.Background())
	first.faulted.Store(true)
	store, err := journal.Open(context.Background(), filepath.Join(t.TempDir(), "journal.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now().UTC()
	event, _, err := store.EnqueueTurn(context.Background(), journal.Envelope{
		AdapterID: "hermes", Route: s.RouteKey(),
		Turn: model.Turn{Identity: model.IdentityScope{ConnectionID: "local", TenantID: "team", AgentID: "agent", UserID: "user", SessionID: "session", TurnID: "turn"},
			Messages: []model.Message{{Role: "user", Content: "remember", OccurredAt: now}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	w := broker.Worker{Journal: store, Provider: r, Clock: func() time.Time { return now }}
	if err := w.DrainOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, err := store.Event(context.Background(), event.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != journal.StateRetryableFailed || got.NextAttemptAt == nil {
		t.Fatalf("launch failure was not retryable: %+v", got)
	}
	now = got.NextAttemptAt.Add(time.Second)
	if err := w.DrainOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, err = store.Event(context.Background(), event.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != journal.StateAccepted || first.writes.Load() != 0 || recovered == nil || recovered.writes.Load() != 1 {
		t.Fatalf("retry did not deliver exactly once: %+v", got)
	}
}
