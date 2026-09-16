package journal

import (
	"context"
	"mlink/internal/connection"
	"mlink/internal/model"
	"testing"
)

func TestObservationReceiptPersistsAndContextStaysScoped(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	i := model.IdentityScope{ConnectionID: "local", TenantID: "team", AgentID: "agent", UserID: "user", SessionID: "session", TurnID: "one"}
	turn := model.UserTurn{Identity: i, Message: model.Message{Role: "user", Content: "old question"}}
	route := connection.RouteKey{ConnectionID: "local", ProviderID: "dev.mlink.tencentdb", ProviderVersion: "0.1.0", ConfigRevision: "one"}
	id, claimed, err := s.ClaimObservation(ctx, "hermes", route, turn)
	if err != nil || !claimed {
		t.Fatal(err)
	}
	if _, again, err := s.ClaimObservation(ctx, "hermes", route, turn); err != nil || again {
		t.Fatal("duplicate claimed")
	}
	if err := s.SaveObservation(ctx, id, model.ObservationReceipt{Paused: true, CorrectionStatus: "queued", TaskID: "task", RelatedSkillRefs: []string{"skill:old@v2"}}); err != nil {
		t.Fatal(err)
	}
	active, err := s.ActiveObservations(ctx)
	if err != nil || len(active) != 1 || active[0].Turn.Correction {
		t.Fatal("active recovery would repeat correction")
	}
	if err := s.CompleteObservation(ctx, "hermes", i, []model.Message{turn.Message, {Role: "assistant", Content: "old answer"}}); err != nil {
		t.Fatal(err)
	}
	i.TurnID = "two"
	previous, err := s.PreviousMessages(ctx, "hermes", i)
	if err != nil || len(previous) != 2 || previous[1].Content != "old answer" {
		t.Fatalf("previous %+v %v", previous, err)
	}
	i.UserID = "another"
	previous, err = s.PreviousMessages(ctx, "hermes", i)
	if err != nil || len(previous) != 0 {
		t.Fatal("identity leak")
	}
	active, err = s.ActiveObservations(ctx)
	if err != nil || len(active) != 0 {
		t.Fatal("completed turn remains active")
	}
}
