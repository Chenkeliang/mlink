package backend

import (
	"context"
	"errors"
	"testing"

	"mlink/internal/config"
	"mlink/internal/connection"
	"mlink/internal/model"
	"mlink/internal/provider/host"
	"mlink/internal/provider/manifest"
	"mlink/internal/provider/protocol"
	"mlink/internal/provider/tencentdb"
)

type fakeSecretStore struct {
	values map[string][]byte
}

func (s fakeSecretStore) Put(context.Context, string, []byte) error { return nil }
func (s fakeSecretStore) Delete(context.Context, string) error      { return nil }
func (s fakeSecretStore) Get(_ context.Context, account string) ([]byte, error) {
	value, ok := s.values[account]
	if !ok {
		return nil, errors.New("missing secret")
	}
	return append([]byte(nil), value...), nil
}

type fakeSession struct {
	route    connection.RouteKey
	captured int
}

type archivingFakeSession struct {
	fakeSession
	archived model.IdentityScope
}

func (s *archivingFakeSession) Capabilities() map[string]manifest.CapabilityDescriptor {
	return map[string]manifest.CapabilityDescriptor{"archive_session": {Version: 1}}
}

func (s *archivingFakeSession) ArchiveSession(_ context.Context, _ host.CallMeta, identity model.IdentityScope) error {
	s.archived = identity
	return nil
}

func (s *fakeSession) RouteKey() connection.RouteKey                          { return s.route }
func (s *fakeSession) Capabilities() map[string]manifest.CapabilityDescriptor { return nil }
func (s *fakeSession) Health(context.Context) (protocol.HealthResult, error) {
	return protocol.HealthResult{}, nil
}
func (s *fakeSession) CaptureTurn(context.Context, host.CallMeta, model.Turn) (model.WriteReceipt, error) {
	s.captured++
	return model.WriteReceipt{ReceiptID: "receipt", State: model.WriteAccepted}, nil
}
func (s *fakeSession) Recall(context.Context, host.CallMeta, model.RecallRequest) (model.ContextBundle, error) {
	return model.ContextBundle{}, nil
}
func (s *fakeSession) Shutdown(context.Context) error    { return nil }
func (s *fakeSession) State() host.State                 { return host.StateReady }
func (s *fakeSession) Done() <-chan struct{}             { return make(chan struct{}) }
func (s *fakeSession) ExitEvent() (host.ExitEvent, bool) { return host.ExitEvent{}, false }

func TestRuntimeStartsPinnedConnectionWithKeychainSecrets(t *testing.T) {
	var captured host.Config
	starts := 0
	runtime := NewRuntime(RuntimeConfig{
		Secrets:          fakeSecretStore{values: map[string][]byte{"connection/local/token": []byte("secret-token")}},
		Manifest:         tencentdb.BundledManifest(),
		PackageDirectory: t.TempDir(),
		RuntimeDirectory: t.TempDir(),
		StartProvider: func(_ context.Context, cfg host.Config) (host.Session, error) {
			starts++
			captured = cfg
			return &fakeSession{route: cfg.Snapshot.RouteKey()}, nil
		},
	})
	connectionConfig := config.Connection{
		ID: "local", ProviderID: "dev.mlink.tencentdb", ProviderVersion: "0.1.0", ConfigRevision: "rev-1",
		ProviderConfig: map[string]any{"base_url": "http://127.0.0.1:8096", "service_id": "default", "timeout_ms": 5000},
		SecretRefs:     map[string]string{"token": "keychain://dev.mlink/connection/local/token"},
	}
	session, err := runtime.Start(context.Background(), connectionConfig)
	if err != nil {
		t.Fatal(err)
	}
	if session.RouteKey().ConfigRevision != "rev-1" || captured.Secrets["token"] != "secret-token" {
		t.Fatalf("captured config = %#v", captured)
	}
	second, err := runtime.Start(context.Background(), connectionConfig)
	if err != nil || second != session || starts != 1 {
		t.Fatalf("Provider reuse = first:%p second:%p starts:%d error:%v", session, second, starts, err)
	}
	if _, err := runtime.CaptureTurn(context.Background(), connection.RouteKey{
		ConnectionID: "local", ProviderID: "dev.mlink.tencentdb", ProviderVersion: "0.1.0", ConfigRevision: "rev-other",
	}, host.CallMeta{IdempotencyKey: "event"}, model.Turn{}); !errors.Is(err, ErrConnectionUnavailable) {
		t.Fatalf("CaptureTurn() error = %v", err)
	}
}

func TestRuntimeRoutesOptionalArchiveSession(t *testing.T) {
	route := connection.RouteKey{
		ConnectionID: "local", ProviderID: "dev.mlink.tencentdb",
		ProviderVersion: "0.1.0", ConfigRevision: "rev-1",
	}
	session := &archivingFakeSession{fakeSession: fakeSession{route: route}}
	runtime := &Runtime{sessions: map[string]host.Session{routeKey(route): session}}
	identity := model.IdentityScope{
		ConnectionID: "local", TenantID: "team-a", AgentID: "agent-a",
		UserID: "user-a", SessionID: "session-a",
	}
	if err := runtime.ArchiveSession(context.Background(), route, host.CallMeta{IdempotencyKey: "fin-a"}, identity); err != nil {
		t.Fatal(err)
	}
	if session.archived != identity {
		t.Fatalf("archived identity = %#v", session.archived)
	}
}

func TestRuntimeReportsUnsupportedArchiveSession(t *testing.T) {
	route := connection.RouteKey{
		ConnectionID: "local", ProviderID: "dev.mlink.fixture",
		ProviderVersion: "0.1.0", ConfigRevision: "rev-1",
	}
	runtime := &Runtime{sessions: map[string]host.Session{routeKey(route): &fakeSession{route: route}}}
	err := runtime.ArchiveSession(context.Background(), route, host.CallMeta{IdempotencyKey: "fin-a"}, model.IdentityScope{
		ConnectionID: "local", TenantID: "team-a", AgentID: "agent-a",
		UserID: "user-a", SessionID: "session-a",
	})
	if !errors.Is(err, host.ErrCapabilityUnavailable) {
		t.Fatalf("ArchiveSession() error = %v", err)
	}
}
