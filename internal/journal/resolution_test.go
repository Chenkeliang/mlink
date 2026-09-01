package journal

import (
	"context"
	"errors"
	"testing"
)

func TestResolveUnresolvedEventPreservesDeliveryStateAndClearsPayload(t *testing.T) {
	store := openTestStore(t)
	event, _, err := store.EnqueueTurn(context.Background(), fixtureEnvelope("user-resolve", "turn-resolve", "content"))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MarkAmbiguous(context.Background(), event.ID, "response_lost"); err != nil {
		t.Fatal(err)
	}
	resolved, err := store.ResolveUnresolvedEvent(context.Background(), event.ID[len(event.ID)-8:], ResolutionRequest{
		Resolution: ResolutionDiscarded, Reason: "legacy scope inactive", ResolvedBy: "uid:501@host",
	})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.State != StateAmbiguous || resolved.Resolution != ResolutionDiscarded || resolved.ResolvedReason == "" || resolved.ResolvedAt == nil {
		t.Fatalf("resolved = %#v", resolved)
	}
	var payload []byte
	if err := store.db.QueryRow(`SELECT payload FROM journal_events WHERE id = ?`, event.ID).Scan(&payload); err != nil || payload != nil {
		t.Fatalf("payload/error = %q/%v", payload, err)
	}
	summary, _ := store.QueueSummary(context.Background())
	if summary.Ambiguous != 0 {
		t.Fatalf("summary = %#v", summary)
	}
}

func TestResolveUnresolvedEventRequiresUniqueSuffixReasonAndProviderEvidence(t *testing.T) {
	store := openTestStore(t)
	event, _, _ := store.EnqueueTurn(context.Background(), fixtureEnvelope("user-evidence", "turn-evidence", "content"))
	_ = store.MarkAmbiguous(context.Background(), event.ID, "response_lost")
	for _, request := range []ResolutionRequest{
		{Resolution: ResolutionDiscarded, ResolvedBy: "uid:501@host"},
		{Resolution: ResolutionDelivered, Reason: "verified", ResolvedBy: "uid:501@host"},
		{Resolution: "unknown", Reason: "verified", ResolvedBy: "uid:501@host"},
	} {
		if _, err := store.ResolveUnresolvedEvent(context.Background(), event.ID, request); err == nil {
			t.Fatalf("ResolveUnresolvedEvent(%#v) error = nil", request)
		}
	}
	if _, err := store.ResolveUnresolvedEvent(context.Background(), "short", ResolutionRequest{Resolution: ResolutionDiscarded, Reason: "verified", ResolvedBy: "uid:501@host"}); err == nil {
		t.Fatal("short suffix accepted")
	}
	_, err := store.ResolveUnresolvedEvent(context.Background(), event.ID, ResolutionRequest{Resolution: ResolutionDelivered, Reason: "verified", ProviderRef: "provider-ref", ResolvedBy: "uid:501@host"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ResolveUnresolvedEvent(context.Background(), event.ID, ResolutionRequest{Resolution: ResolutionDiscarded, Reason: "again", ResolvedBy: "uid:501@host"}); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("double resolution error = %v", err)
	}
}
