package journal

import (
	"context"
	"errors"
	"testing"
	"time"

	"mlink/internal/connection"
	"mlink/internal/model"
)

func fixtureEnvelope(userID, turnID, content string) Envelope {
	return Envelope{
		AdapterID: "hermes",
		Route: connection.RouteKey{
			ConnectionID:    "local",
			ProviderID:      "dev.mlink.tencentdb",
			ProviderVersion: "0.1.0",
			ConfigRevision:  "rev-1",
		},
		Turn: model.Turn{
			Identity: model.IdentityScope{
				ConnectionID: "local",
				TenantID:     "personal",
				AgentID:      "hermes",
				UserID:       userID,
				SessionID:    "session-1",
				TurnID:       turnID,
			},
			Messages: []model.Message{{Role: "user", Content: content, OccurredAt: time.Date(2026, 8, 28, 10, 0, 0, 0, time.UTC)}},
		},
	}
}

func TestEnqueueTurnDeduplicatesAndDetectsConflict(t *testing.T) {
	store := openTestStore(t)
	first, inserted, err := store.EnqueueTurn(context.Background(), fixtureEnvelope("usr_a", "turn-1", "hello"))
	if err != nil || !inserted {
		t.Fatalf("first = %#v %v %v", first, inserted, err)
	}
	second, inserted, err := store.EnqueueTurn(context.Background(), fixtureEnvelope("usr_a", "turn-1", "hello"))
	if err != nil || inserted || second.ID != first.ID {
		t.Fatalf("duplicate = %#v %v %v", second, inserted, err)
	}
	_, _, err = store.EnqueueTurn(context.Background(), fixtureEnvelope("usr_a", "turn-1", "changed"))
	if !errors.Is(err, ErrTurnConflict) {
		t.Fatalf("error = %v", err)
	}
}

func TestEnqueueTurnSeparatesUsersWithSameSessionAndTurn(t *testing.T) {
	store := openTestStore(t)
	a, inserted, err := store.EnqueueTurn(context.Background(), fixtureEnvelope("usr_a", "turn-1", "hello"))
	if err != nil || !inserted {
		t.Fatalf("user a = %#v %v %v", a, inserted, err)
	}
	b, inserted, err := store.EnqueueTurn(context.Background(), fixtureEnvelope("usr_b", "turn-1", "hello"))
	if err != nil || !inserted {
		t.Fatalf("user b = %#v %v %v", b, inserted, err)
	}
	if a.ID == b.ID {
		t.Fatalf("cross-user event IDs collided: %q", a.ID)
	}
}

func TestMarkAcceptedClearsFinalPayload(t *testing.T) {
	store := openTestStore(t)
	event, _, err := store.EnqueueTurn(context.Background(), fixtureEnvelope("usr_a", "turn-2", "sensitive"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MarkAccepted(context.Background(), event.ID, model.WriteReceipt{ReceiptID: "r1", State: model.WriteAccepted}); err != nil {
		t.Fatal(err)
	}
	if got := rawPayloadForTest(t, store, event.ID); got != nil {
		t.Fatalf("payload retained: %q", got)
	}
	got, err := store.Event(context.Background(), event.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != StateAccepted || got.Receipt.ReceiptID != "r1" {
		t.Fatalf("event = %#v", got)
	}
}

func TestAmbiguousRetainsPayloadAndBlocks(t *testing.T) {
	store := openTestStore(t)
	event, _, err := store.EnqueueTurn(context.Background(), fixtureEnvelope("usr_a", "turn-3", "audit me"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MarkAmbiguous(context.Background(), event.ID, "maybe_sent"); err != nil {
		t.Fatal(err)
	}
	if got := rawPayloadForTest(t, store, event.ID); len(got) == 0 {
		t.Fatal("ambiguous payload was cleared")
	}
	blocking, err := store.ListBlockingEvents(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(blocking) != 1 || blocking[0].ID != event.ID || blocking[0].State != StateAmbiguous {
		t.Fatalf("blocking = %#v", blocking)
	}
}

func TestClaimAndRetryUseCompareAndSetTransitions(t *testing.T) {
	store := openTestStore(t)
	event, _, err := store.EnqueueTurn(context.Background(), fixtureEnvelope("usr_a", "turn-4", "retry"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 28, 11, 0, 0, 0, time.UTC)
	claimed, err := store.ClaimReady(context.Background(), now, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(claimed) != 1 || claimed[0].ID != event.ID || claimed[0].State != StateDispatching {
		t.Fatalf("claimed = %#v", claimed)
	}
	next := now.Add(time.Minute)
	if err := store.MarkRetryable(context.Background(), event.ID, "temporarily_unavailable", next); err != nil {
		t.Fatal(err)
	}
	if claimed, err = store.ClaimReady(context.Background(), now, 1); err != nil || len(claimed) != 0 {
		t.Fatalf("early claim = %#v, %v", claimed, err)
	}
	if claimed, err = store.ClaimReady(context.Background(), next, 1); err != nil || len(claimed) != 1 {
		t.Fatalf("ready claim = %#v, %v", claimed, err)
	}
}

func TestRetryCompletesDeliveryAttemptAudit(t *testing.T) {
	store := openTestStore(t)
	event, _, err := store.EnqueueTurn(context.Background(), fixtureEnvelope("usr_a", "turn-audit", "retry"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 28, 11, 0, 0, 0, time.UTC)
	if _, err := store.ClaimReady(context.Background(), now, 1); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkRetryable(context.Background(), event.ID, "temporarily_unavailable", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	var finishedAt, state, errorCode string
	if err := store.db.QueryRow(`
		SELECT finished_at, delivery_state, error_code
		FROM delivery_attempts WHERE event_id = ? ORDER BY id DESC LIMIT 1`, event.ID).Scan(&finishedAt, &state, &errorCode); err != nil {
		t.Fatal(err)
	}
	if finishedAt == "" || state != string(StateRetryableFailed) || errorCode != "temporarily_unavailable" {
		t.Fatalf("attempt = finished:%q state:%q error:%q", finishedAt, state, errorCode)
	}
}

func TestRecordFragmentPairsOutOfOrderAndEnqueuesTurn(t *testing.T) {
	store := openTestStore(t)
	envelope := fixtureEnvelope("usr_a", "turn-fragments", "prompt")
	assistant := Fragment{
		AdapterID:  envelope.AdapterID,
		Route:      envelope.Route,
		Identity:   envelope.Turn.Identity,
		Role:       "assistant",
		Content:    "answer",
		OccurredAt: time.Date(2026, 8, 28, 11, 1, 0, 0, time.UTC),
	}
	user := assistant
	user.Role = "user"
	user.Content = "prompt"
	user.OccurredAt = time.Date(2026, 8, 28, 11, 0, 0, 0, time.UTC)
	if err := store.RecordFragment(context.Background(), assistant); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordFragment(context.Background(), user); err != nil {
		t.Fatal(err)
	}
	var eventID string
	if err := store.db.QueryRow("SELECT id FROM journal_events WHERE turn_id = ?", "turn-fragments").Scan(&eventID); err != nil {
		t.Fatal(err)
	}
	event, err := store.Event(context.Background(), eventID)
	if err != nil {
		t.Fatal(err)
	}
	if len(event.Turn.Messages) != 2 || event.Turn.Messages[0].Role != "user" || event.Turn.Messages[1].Role != "assistant" {
		t.Fatalf("messages = %#v", event.Turn.Messages)
	}
	var fragments int
	if err := store.db.QueryRow("SELECT count(*) FROM turn_fragments WHERE turn_id = ?", "turn-fragments").Scan(&fragments); err != nil {
		t.Fatal(err)
	}
	if fragments != 0 {
		t.Fatalf("fragments retained after enqueue = %d", fragments)
	}
}

func rawPayloadForTest(t *testing.T, store *Store, id string) []byte {
	t.Helper()
	var payload []byte
	if err := store.db.QueryRow("SELECT payload FROM journal_events WHERE id = ?", id).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	return payload
}
