package broker

import (
	"context"
	"testing"
	"time"

	"mlink/internal/journal"
)

func TestServiceSubmitTurnReturnsExistingReceiptForReplay(t *testing.T) {
	store := brokerTestStore(t)
	service := Service{Journal: store}
	first, err := service.SubmitTurn(context.Background(), brokerEnvelope("turn-replay", "rev-1"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.SubmitTurn(context.Background(), brokerEnvelope("turn-replay", "rev-1"))
	if err != nil {
		t.Fatal(err)
	}
	if !first.Queued || second.Queued || first.EventID != second.EventID {
		t.Fatalf("receipts = %#v %#v", first, second)
	}
	blocking, err := store.ListBlockingEvents(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(blocking) != 0 {
		t.Fatalf("blocking = %#v", blocking)
	}
}

func TestServiceFinalizePersistsCanonicalScope(t *testing.T) {
	store := brokerTestStore(t)
	service := Service{Journal: store}
	request := journal.FinalizationRequest{
		AdapterID: "codex",
		Route:     brokerEnvelope("turn", "rev-1").Route,
		Identity:  brokerEnvelope("turn", "rev-1").Turn.Identity,
	}
	request.Identity.TurnID = ""
	pending, err := service.FinalizeSession(context.Background(), request)
	if err != nil || pending != 0 {
		t.Fatalf("FinalizeSession() = %d, %v", pending, err)
	}
	claimed, err := store.ClaimReadyFinalizations(context.Background(), time.Now().UTC(), 1)
	if err != nil || len(claimed) != 1 || claimed[0].Identity != request.Identity || claimed[0].Route != request.Route {
		t.Fatalf("claimed = %#v, %v", claimed, err)
	}
}

var _ = journal.StateQueued
