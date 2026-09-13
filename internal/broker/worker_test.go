package broker

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"mlink/internal/connection"
	"mlink/internal/journal"
	"mlink/internal/model"
	"mlink/internal/provider/host"
	"mlink/internal/provider/protocol"
)

type recordingProvider struct {
	route      connection.RouteKey
	captureErr error
	receipt    model.WriteReceipt
	calls      int
}

type archivingProvider struct {
	recordingProvider
	archiveErr   error
	archiveCalls int
	archiveRoute connection.RouteKey
	identity     model.IdentityScope
}

func (p *archivingProvider) ArchiveSession(_ context.Context, route connection.RouteKey, _ host.CallMeta, identity model.IdentityScope) error {
	p.archiveCalls++
	p.archiveRoute = route
	p.identity = identity
	return p.archiveErr
}

func (p *recordingProvider) CaptureTurn(_ context.Context, route connection.RouteKey, _ host.CallMeta, _ model.Turn) (model.WriteReceipt, error) {
	p.calls++
	p.route = route
	return p.receipt, p.captureErr
}

func (p *recordingProvider) Recall(context.Context, connection.RouteKey, host.CallMeta, model.RecallRequest) (model.ContextBundle, error) {
	return model.ContextBundle{}, nil
}

func brokerTestStore(t *testing.T) *journal.Store {
	t.Helper()
	store, _ := brokerTestStoreAt(t)
	return store
}

func brokerTestStoreAt(t *testing.T) (*journal.Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "journal.db")
	store, err := journal.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, path
}

func brokerEnvelope(turnID, revision string) journal.Envelope {
	return journal.Envelope{
		AdapterID: "codex",
		Route:     connection.RouteKey{ConnectionID: "local", ProviderID: "dev.mlink.tencentdb", ProviderVersion: "0.1.0", ConfigRevision: revision},
		Turn: model.Turn{
			Identity: model.IdentityScope{ConnectionID: "local", TenantID: "personal", AgentID: "codex", UserID: "usr_a", SessionID: "session", TurnID: turnID},
			Messages: []model.Message{{Role: "user", Content: "remember", OccurredAt: time.Now().UTC()}},
		},
	}
}

func insertStrandedPair(t *testing.T, path string, envelope journal.Envelope) {
	t.Helper()
	identity := envelope.Turn.Identity
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, role := range []string{"user", "assistant"} {
		if _, err := db.Exec(`
			INSERT INTO turn_fragments(
				adapter_id, connection_id, tenant_id, agent_id, user_id, session_id, turn_id,
				role, content, content_hash, occurred_at, provider_id, provider_version, config_revision, actor_digest
			) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			envelope.AdapterID, envelope.Route.ConnectionID, identity.TenantID, identity.AgentID,
			identity.UserID, identity.SessionID, identity.TurnID, role, []byte(role), "hash-"+role,
			"2026-09-02T04:16:00Z", envelope.Route.ProviderID, envelope.Route.ProviderVersion,
			envelope.Route.ConfigRevision, "",
		); err != nil {
			t.Fatal(err)
		}
	}
}

func TestWorkerMarksMaybeSentUnsafeCaptureAmbiguous(t *testing.T) {
	store := brokerTestStore(t)
	event, _, err := store.EnqueueTurn(context.Background(), brokerEnvelope("turn-ambiguous", "rev-1"))
	if err != nil {
		t.Fatal(err)
	}
	provider := &recordingProvider{captureErr: &host.CallError{
		Delivery: host.DeliveryMaybeSent, ReplaySafe: false, Code: protocol.ErrorTemporarilyUnavailable,
	}}
	worker := Worker{Journal: store, Provider: provider, Clock: func() time.Time { return time.Now().UTC() }}
	if err := worker.DrainOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, err := store.Event(context.Background(), event.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != journal.StateAmbiguous {
		t.Fatalf("state = %q", got.State)
	}
}

func TestQueuedEventKeepsOriginalRouteRevision(t *testing.T) {
	store := brokerTestStore(t)
	event, _, err := store.EnqueueTurn(context.Background(), brokerEnvelope("turn-route", "rev-old"))
	if err != nil {
		t.Fatal(err)
	}
	provider := &recordingProvider{receipt: model.WriteReceipt{ReceiptID: "receipt", State: model.WriteAccepted}}
	worker := Worker{Journal: store, Provider: provider, Clock: func() time.Time { return time.Now().UTC() }}
	if err := worker.DrainOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	if provider.route.ConfigRevision != "rev-old" {
		t.Fatalf("revision = %q", provider.route.ConfigRevision)
	}
	got, err := store.Event(context.Background(), event.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != journal.StateAccepted {
		t.Fatalf("state = %q", got.State)
	}
}

func TestWorkerRetriesOnlyDefinitelyNotSentTransientFailure(t *testing.T) {
	store := brokerTestStore(t)
	event, _, err := store.EnqueueTurn(context.Background(), brokerEnvelope("turn-retry", "rev-1"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	provider := &recordingProvider{captureErr: &host.CallError{
		Delivery: host.DeliveryNotSent, ReplaySafe: false, Code: protocol.ErrorRateLimited,
	}}
	worker := Worker{Journal: store, Provider: provider, Clock: func() time.Time { return now }}
	if err := worker.DrainOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, err := store.Event(context.Background(), event.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != journal.StateRetryableFailed || got.NextAttemptAt == nil || !got.NextAttemptAt.After(now) {
		t.Fatalf("event = %#v", got)
	}
}

func TestWorkerRetriesDefinitelyNotSentDeadline(t *testing.T) {
	store := brokerTestStore(t)
	event, _, err := store.EnqueueTurn(context.Background(), brokerEnvelope("turn-deadline", "rev-1"))
	if err != nil {
		t.Fatal(err)
	}
	provider := &recordingProvider{captureErr: &host.CallError{
		Delivery: host.DeliveryNotSent, ReplaySafe: false, Code: protocol.ErrorDeadlineExceeded,
	}}
	worker := Worker{Journal: store, Provider: provider, Clock: func() time.Time { return time.Now().UTC() }}
	if err := worker.DrainOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, err := store.Event(context.Background(), event.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != journal.StateRetryableFailed {
		t.Fatalf("state = %q", got.State)
	}
}

func TestWorkerReconcilesAndDeliversStrandedCompleteTurn(t *testing.T) {
	store, path := brokerTestStoreAt(t)
	envelope := brokerEnvelope("turn-stranded", "rev-1")
	insertStrandedPair(t, path, envelope)
	provider := &recordingProvider{receipt: model.WriteReceipt{ReceiptID: "receipt", State: model.WriteAccepted}}
	worker := Worker{Journal: store, Provider: provider}
	if err := worker.DrainOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	if provider.calls != 1 {
		t.Fatalf("capture calls = %d, want 1", provider.calls)
	}
}

func TestWorkerReconcilesAtBoundedIntervalWhenIdle(t *testing.T) {
	store, path := brokerTestStoreAt(t)
	now := time.Date(2026, 9, 2, 4, 0, 0, 0, time.UTC)
	provider := &recordingProvider{receipt: model.WriteReceipt{ReceiptID: "receipt", State: model.WriteAccepted}}
	worker := Worker{Journal: store, Provider: provider, Clock: func() time.Time { return now }}
	if err := worker.DrainOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	insertStrandedPair(t, path, brokerEnvelope("turn-after-idle-scan", "rev-1"))
	if err := worker.DrainOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	if provider.calls != 0 {
		t.Fatalf("capture calls before interval = %d, want 0", provider.calls)
	}
	now = now.Add(30 * time.Second)
	if err := worker.DrainOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	if provider.calls != 1 {
		t.Fatalf("capture calls after interval = %d, want 1", provider.calls)
	}
}

func TestWorkerArchivesAfterLastTurnIsAccepted(t *testing.T) {
	store := brokerTestStore(t)
	envelope := brokerEnvelope("turn-final", "rev-1")
	event, _, err := store.EnqueueTurn(context.Background(), envelope)
	if err != nil {
		t.Fatal(err)
	}
	request := journal.FinalizationRequest{AdapterID: envelope.AdapterID, Route: envelope.Route, Identity: envelope.Turn.Identity}
	request.Identity.TurnID = ""
	finalization, _, _, err := store.RequestFinalization(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	provider := &archivingProvider{recordingProvider: recordingProvider{
		receipt: model.WriteReceipt{ReceiptID: "receipt", State: model.WriteAccepted},
	}}
	worker := Worker{Journal: store, Provider: provider, Clock: func() time.Time { return time.Now().UTC() }}
	if err := worker.DrainOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	gotEvent, err := store.Event(context.Background(), event.ID)
	if err != nil || gotEvent.State != journal.StateAccepted {
		t.Fatalf("event = %#v %v", gotEvent, err)
	}
	gotFinalization, err := store.Finalization(context.Background(), finalization.ID)
	if err != nil || gotFinalization.State != journal.StateCompleted || provider.archiveCalls != 1 {
		t.Fatalf("finalization/provider = %#v calls:%d error:%v", gotFinalization, provider.archiveCalls, err)
	}
	if provider.archiveRoute != request.Route || provider.identity != request.Identity {
		t.Fatalf("archive route/identity = %#v/%#v", provider.archiveRoute, provider.identity)
	}
}

func TestWorkerCompletesFinalizationForProviderWithoutArchiveCapability(t *testing.T) {
	store := brokerTestStore(t)
	request := journal.FinalizationRequest{
		AdapterID: "codex", Route: brokerEnvelope("turn", "rev-1").Route,
		Identity: brokerEnvelope("turn", "rev-1").Turn.Identity,
	}
	request.Identity.TurnID = ""
	finalization, _, _, err := store.RequestFinalization(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	worker := Worker{Journal: store, Provider: &recordingProvider{}, Clock: func() time.Time { return time.Now().UTC() }}
	if err := worker.DrainOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, err := store.Finalization(context.Background(), finalization.ID)
	if err != nil || got.State != journal.StateCompleted {
		t.Fatalf("finalization = %#v %v", got, err)
	}
}

func TestWorkerRetriesReplaySafeFinalizationFailure(t *testing.T) {
	store := brokerTestStore(t)
	request := journal.FinalizationRequest{
		AdapterID: "codex", Route: brokerEnvelope("turn", "rev-1").Route,
		Identity: brokerEnvelope("turn", "rev-1").Turn.Identity,
	}
	request.Identity.TurnID = ""
	finalization, _, _, err := store.RequestFinalization(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 13, 9, 0, 0, 0, time.UTC)
	provider := &archivingProvider{archiveErr: &host.CallError{
		Delivery: host.DeliveryMaybeSent, ReplaySafe: true, Code: protocol.ErrorTemporarilyUnavailable,
	}}
	worker := Worker{Journal: store, Provider: provider, Clock: func() time.Time { return now }}
	if err := worker.DrainOne(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, err := store.Finalization(context.Background(), finalization.ID)
	if err != nil || got.State != journal.StateRetryableFailed || got.NextAttemptAt == nil || !got.NextAttemptAt.After(now) {
		t.Fatalf("finalization = %#v %v", got, err)
	}
}
