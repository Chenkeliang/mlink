package broker

import (
	"context"
	"testing"

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

var _ = journal.StateQueued
