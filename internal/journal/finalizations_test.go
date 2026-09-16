package journal

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"mlink/internal/connection"
	"mlink/internal/model"
)

func fixtureFinalization() FinalizationRequest {
	return FinalizationRequest{
		AdapterID: "codex",
		Route: connection.RouteKey{
			ConnectionID: "local", ProviderID: "dev.mlink.tencentdb",
			ProviderVersion: "0.1.0", ConfigRevision: "rev-1",
		},
		Identity: model.IdentityScope{
			ConnectionID: "local", TenantID: "personal", AgentID: "codex",
			UserID: "usr-a", SessionID: "session-a",
		},
	}
}

func TestRequestFinalizationPersistsDeduplicatesAndAllowsResume(t *testing.T) {
	store := openTestStore(t)
	request := fixtureFinalization()
	first, queued, pending, err := store.RequestFinalization(context.Background(), request)
	if err != nil || !queued || pending != 0 || first.State != StateQueued {
		t.Fatalf("first request = %#v queued:%v pending:%d error:%v", first, queued, pending, err)
	}
	second, queued, _, err := store.RequestFinalization(context.Background(), request)
	if err != nil || queued || second.ID != first.ID || second.State != StateQueued {
		t.Fatalf("duplicate request = %#v queued:%v error:%v", second, queued, err)
	}

	now := time.Date(2026, 9, 13, 8, 0, 0, 0, time.UTC)
	claimed, err := store.ClaimReadyFinalizations(context.Background(), now, 1)
	if err != nil || len(claimed) != 1 || claimed[0].ID != first.ID {
		t.Fatalf("claimed = %#v error:%v", claimed, err)
	}
	if err := store.MarkFinalizationCompleted(context.Background(), first.ID); err != nil {
		t.Fatal(err)
	}
	resumed, queued, _, err := store.RequestFinalization(context.Background(), request)
	if err != nil || !queued || resumed.ID != first.ID || resumed.State != StateQueued || resumed.AttemptCount != 0 {
		t.Fatalf("resumed request = %#v queued:%v error:%v", resumed, queued, err)
	}
}

func TestFinalizationWaitsForPendingAndUnresolvedTurns(t *testing.T) {
	store := openTestStore(t)
	request := fixtureFinalization()
	envelope := fixtureEnvelope(request.Identity.UserID, "turn-before-end", "remember")
	envelope.AdapterID = request.AdapterID
	envelope.Route = request.Route
	envelope.Turn.Identity = request.Identity
	envelope.Turn.Identity.TurnID = "turn-before-end"
	event, _, err := store.EnqueueTurn(context.Background(), envelope)
	if err != nil {
		t.Fatal(err)
	}
	_, _, pending, err := store.RequestFinalization(context.Background(), request)
	if err != nil || pending != 1 {
		t.Fatalf("pending/error = %d/%v", pending, err)
	}
	now := time.Date(2026, 9, 13, 8, 0, 0, 0, time.UTC)
	if claimed, err := store.ClaimReadyFinalizations(context.Background(), now, 1); err != nil || len(claimed) != 0 {
		t.Fatalf("finalization claimed before turn delivery: %#v %v", claimed, err)
	}
	if err := store.MarkPermanent(context.Background(), event.ID, "rejected"); err != nil {
		t.Fatal(err)
	}
	if claimed, err := store.ClaimReadyFinalizations(context.Background(), now, 1); err != nil || len(claimed) != 0 {
		t.Fatalf("finalization claimed with unresolved turn: %#v %v", claimed, err)
	}
	if _, err := store.ResolveUnresolvedEvent(context.Background(), event.ID, ResolutionRequest{
		Resolution: ResolutionDiscarded, Reason: "operator verified", ResolvedBy: "uid:501@host",
	}); err != nil {
		t.Fatal(err)
	}
	claimed, err := store.ClaimReadyFinalizations(context.Background(), now, 1)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("finalization after resolution = %#v %v", claimed, err)
	}
}

func TestFinalizationKeepsStrandedCompleteFragmentsUntilReconciled(t *testing.T) {
	store := openTestStore(t)
	request := fixtureFinalization()
	for _, role := range []string{"user", "assistant"} {
		if _, err := store.db.Exec(`
			INSERT INTO turn_fragments(
				adapter_id, connection_id, tenant_id, agent_id, user_id, session_id, turn_id,
				role, content, content_hash, occurred_at, provider_id, provider_version,
				config_revision, actor_digest
			) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			request.AdapterID, request.Route.ConnectionID, request.Identity.TenantID,
			request.Identity.AgentID, request.Identity.UserID, request.Identity.SessionID,
			"turn-stranded", role, []byte(role), "hash-"+role, "2026-09-13T08:00:00Z",
			request.Route.ProviderID, request.Route.ProviderVersion, request.Route.ConfigRevision, "",
		); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, _, err := store.RequestFinalization(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	var fragments int
	if err := store.db.QueryRow(`SELECT count(*) FROM turn_fragments WHERE session_id=?`, request.Identity.SessionID).Scan(&fragments); err != nil || fragments != 2 {
		t.Fatalf("fragments/error = %d/%v", fragments, err)
	}
	now := time.Date(2026, 9, 13, 8, 0, 0, 0, time.UTC)
	if claimed, err := store.ClaimReadyFinalizations(context.Background(), now, 1); err != nil || len(claimed) != 0 {
		t.Fatalf("finalization claimed before reconciliation: %#v %v", claimed, err)
	}
	if count, err := store.ReconcileCompleteFragments(context.Background(), 1); err != nil || count != 1 {
		t.Fatalf("reconciled/error = %d/%v", count, err)
	}
	events, err := store.ClaimReady(context.Background(), now, 1)
	if err != nil || len(events) != 1 {
		t.Fatalf("events/error = %#v/%v", events, err)
	}
	if err := store.MarkAccepted(context.Background(), events[0].ID, model.WriteReceipt{ReceiptID: "r1", State: model.WriteAccepted}); err != nil {
		t.Fatal(err)
	}
	if claimed, err := store.ClaimReadyFinalizations(context.Background(), now, 1); err != nil || len(claimed) != 1 {
		t.Fatalf("finalization after reconciliation = %#v %v", claimed, err)
	}
}

func TestRequestFinalizationClearsOnlyScopedIncompleteFragments(t *testing.T) {
	store := openTestStore(t)
	request := fixtureFinalization()
	fragment := Fragment{
		AdapterID: request.AdapterID, Route: request.Route, Identity: request.Identity,
		Role: "user", Content: "orphan", OccurredAt: time.Now().UTC(),
	}
	fragment.Identity.TurnID = "turn-orphan"
	if err := store.RecordFragment(context.Background(), fragment); err != nil {
		t.Fatal(err)
	}
	other := fragment
	other.Identity.SessionID = "session-other"
	other.Identity.TurnID = "turn-other"
	if err := store.RecordFragment(context.Background(), other); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := store.RequestFinalization(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	var scoped, untouched int
	if err := store.db.QueryRow(`SELECT count(*) FROM turn_fragments WHERE session_id=?`, request.Identity.SessionID).Scan(&scoped); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRow(`SELECT count(*) FROM turn_fragments WHERE session_id=?`, other.Identity.SessionID).Scan(&untouched); err != nil {
		t.Fatal(err)
	}
	if scoped != 0 || untouched != 1 {
		t.Fatalf("fragment counts = scoped:%d untouched:%d", scoped, untouched)
	}
}

func TestFinalizationDispatchLeaseSurvivesRestartAndRetryBackoff(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.db")
	store, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	request := fixtureFinalization()
	finalization, _, _, err := store.RequestFinalization(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 13, 8, 0, 0, 0, time.UTC)
	if claimed, err := store.ClaimReadyFinalizations(context.Background(), now, 1); err != nil || len(claimed) != 1 {
		t.Fatalf("initial claim = %#v %v", claimed, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if claimed, err := store.ClaimReadyFinalizations(context.Background(), now.Add(finalizationDispatchLease-time.Millisecond), 1); err != nil || len(claimed) != 0 {
		t.Fatalf("claim before lease expiry = %#v %v", claimed, err)
	}
	claimed, err := store.ClaimReadyFinalizations(context.Background(), now.Add(finalizationDispatchLease), 1)
	if err != nil || len(claimed) != 1 || claimed[0].ID != finalization.ID || claimed[0].AttemptCount != 2 {
		t.Fatalf("claim after restart = %#v %v", claimed, err)
	}
	next := now.Add(time.Minute)
	if err := store.MarkFinalizationRetryable(context.Background(), finalization.ID, "temporarily_unavailable", next); err != nil {
		t.Fatal(err)
	}
	if claimed, err := store.ClaimReadyFinalizations(context.Background(), next.Add(-time.Millisecond), 1); err != nil || len(claimed) != 0 {
		t.Fatalf("retry claimed early = %#v %v", claimed, err)
	}
	if claimed, err := store.ClaimReadyFinalizations(context.Background(), next, 1); err != nil || len(claimed) != 1 {
		t.Fatalf("retry claim = %#v %v", claimed, err)
	}
}
