package broker

import (
	"context"
	"mlink/internal/connection"
	"mlink/internal/journal"
	"mlink/internal/model"
	"mlink/internal/provider/host"
	"testing"
	"time"
)

type observedProvider struct {
	recordingProvider
	turns []model.UserTurn
}

func (p *observedProvider) ObserveUserTurn(_ context.Context, _ connection.RouteKey, _ host.CallMeta, turn model.UserTurn) (model.ObservationReceipt, error) {
	p.turns = append(p.turns, turn)
	return model.ObservationReceipt{Paused: true, CorrectionStatus: "queued", TaskID: "correction-task"}, nil
}

func TestUserFragmentObservesCorrectionBeforeAssistantAndDeduplicates(t *testing.T) {
	store := brokerTestStore(t)
	p := &observedProvider{}
	service := Service{Journal: store, Provider: p}
	envelope := brokerEnvelope("first", "rev-1")
	f := journal.Fragment{AdapterID: envelope.AdapterID, Route: envelope.Route, Identity: envelope.Turn.Identity, Role: "user", Content: "怎么改址", OccurredAt: time.Now().UTC()}
	if err := service.SubmitFragment(context.Background(), f); err != nil {
		t.Fatal(err)
	}
	f.Role = "assistant"
	f.Content = "不要走 /ops/exec"
	if err := service.SubmitFragment(context.Background(), f); err != nil {
		t.Fatal(err)
	}
	f.Identity.TurnID = "second"
	f.Role = "user"
	f.Content = "纠正：已经建单时应该走 /ops/exec"
	if err := service.SubmitFragment(context.Background(), f); err != nil {
		t.Fatal(err)
	}
	if err := service.SubmitFragment(context.Background(), f); err != nil {
		t.Fatal(err)
	}
	if len(p.turns) != 2 {
		t.Fatalf("observations = %d", len(p.turns))
	}
	got := p.turns[1]
	if !got.Correction || got.Message.Content != f.Content || len(got.Previous) != 2 || got.Previous[1].Content != "不要走 /ops/exec" {
		t.Fatalf("missing corrected context: %+v", got)
	}
	active, err := store.ActiveObservations(context.Background())
	if err != nil || len(active) != 1 || active[0].Turn.Identity.TurnID != "second" {
		t.Fatalf("active=%+v err=%v", active, err)
	}
}
