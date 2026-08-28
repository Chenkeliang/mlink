package broker

import (
	"context"
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
}

func (p *recordingProvider) CaptureTurn(_ context.Context, route connection.RouteKey, _ host.CallMeta, _ model.Turn) (model.WriteReceipt, error) {
	p.route = route
	return p.receipt, p.captureErr
}

func (p *recordingProvider) Recall(context.Context, connection.RouteKey, host.CallMeta, model.RecallRequest) (model.ContextBundle, error) {
	return model.ContextBundle{}, nil
}

func brokerTestStore(t *testing.T) *journal.Store {
	t.Helper()
	store, err := journal.Open(context.Background(), filepath.Join(t.TempDir(), "journal.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
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
