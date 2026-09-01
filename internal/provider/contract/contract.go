package contract

import (
	"context"
	"errors"
	"testing"

	"mlink/internal/model"
)

type Provider interface {
	CaptureTurn(context.Context, model.Turn) (model.WriteReceipt, error)
	Recall(context.Context, model.RecallRequest) (model.ContextBundle, error)
}

type Factory func(t *testing.T) Provider

func Run(t *testing.T, factory Factory) {
	t.Helper()

	t.Run("missing identity is rejected", func(t *testing.T) {
		provider := factory(t)
		if _, err := provider.Recall(context.Background(), model.RecallRequest{Query: "probe"}); err == nil {
			t.Fatal("Recall() accepted a request without identity")
		}
		if _, err := provider.CaptureTurn(context.Background(), model.Turn{
			Messages: []model.Message{{Role: "user", Content: "probe"}},
		}); err == nil {
			t.Fatal("CaptureTurn() accepted a turn without identity")
		}
	})

	t.Run("new user is empty", func(t *testing.T) {
		provider := factory(t)
		bundle, err := provider.Recall(context.Background(), recallRequest("user-new", "probe"))
		if err != nil {
			t.Fatalf("Recall() error = %v", err)
		}
		for _, item := range bundle.Items {
			if item.Scope == model.ScopeUser {
				t.Fatalf("new user received private item %#v", item)
			}
		}
	})

	t.Run("user A memory is hidden from user B", func(t *testing.T) {
		provider := factory(t)
		if err := checkUserIsolation(provider); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("agent shared items are absent by default", func(t *testing.T) {
		provider := factory(t)
		bundle, err := provider.Recall(context.Background(), recallRequest("user-a", "probe"))
		if err != nil {
			t.Fatalf("Recall() error = %v", err)
		}
		for _, item := range bundle.Items {
			if item.Scope == model.ScopeAgent {
				t.Fatalf("default recall returned Agent-shared item %#v", item)
			}
		}
	})

	t.Run("stable IDs are unique", func(t *testing.T) {
		provider := factory(t)
		bundle, err := provider.Recall(context.Background(), recallRequest("user-a", "duplicate"))
		if err != nil {
			t.Fatalf("Recall() error = %v", err)
		}
		seen := make(map[string]struct{}, len(bundle.Items))
		for _, item := range bundle.Items {
			if _, exists := seen[item.ID]; exists {
				t.Fatalf("duplicate ContextItem ID %q", item.ID)
			}
			seen[item.ID] = struct{}{}
		}
	})

	t.Run("canceled context stops recall", func(t *testing.T) {
		provider := factory(t)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := provider.Recall(ctx, recallRequest("user-a", "probe"))
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Recall() error = %v, want context.Canceled", err)
		}
	})
}

func checkUserIsolation(provider Provider) error {
	turn := model.Turn{
		Identity: model.IdentityScope{
			TenantID: "team-contract", UserID: "user-a", AgentID: "agent-contract",
			SessionID: "session-a", TurnID: "turn-a",
		},
		Messages: []model.Message{{Role: "user", Content: "USER_A_CANARY"}},
	}
	if _, err := provider.CaptureTurn(context.Background(), turn); err != nil {
		return err
	}
	bundle, err := provider.Recall(context.Background(), recallRequest("user-b", "USER_A_CANARY"))
	if err != nil {
		return err
	}
	for _, item := range bundle.Items {
		if item.Scope == model.ScopeUser && item.Text == "USER_A_CANARY" {
			return errors.New("user B received user A private item")
		}
	}
	return nil
}

func recallRequest(userID, query string) model.RecallRequest {
	return model.RecallRequest{
		Identity: model.IdentityScope{TenantID: "team-contract", UserID: userID, AgentID: "agent-contract"},
		Query:    query,
	}
}
