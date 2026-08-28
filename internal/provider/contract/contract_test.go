package contract

import (
	"context"
	"sync"
	"testing"

	"mlink/internal/model"
)

func TestRunAcceptsIsolatedProvider(t *testing.T) {
	Run(t, func(*testing.T) Provider {
		return &memoryProvider{itemsByUser: make(map[string][]model.ContextItem)}
	})
}

func TestRunRejectsProviderThatLeaksAcrossUsers(t *testing.T) {
	ok := testing.RunTests(
		func(_, _ string) (bool, error) { return true, nil },
		[]testing.InternalTest{{
			Name: "leaky provider contract",
			F: func(inner *testing.T) {
				Run(inner, func(*testing.T) Provider { return leakyProvider{} })
			},
		}},
	)
	if ok {
		t.Fatal("contract accepted a provider that returns user A memory to user B")
	}
}

type memoryProvider struct {
	mu          sync.Mutex
	itemsByUser map[string][]model.ContextItem
}

func (p *memoryProvider) CaptureTurn(_ context.Context, turn model.Turn) (model.WriteReceipt, error) {
	if err := turn.Identity.ValidateForCapture(); err != nil {
		return model.WriteReceipt{}, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.itemsByUser[turn.Identity.UserID] = []model.ContextItem{{
		ID:     "l1:" + turn.Identity.TurnID,
		Kind:   "episodic",
		Scope:  model.ScopeUser,
		Text:   turn.Messages[0].Content,
		Source: "fixture:l1",
	}}
	return model.WriteReceipt{AcceptedIDs: []string{turn.Identity.TurnID}}, nil
}

func (p *memoryProvider) Recall(ctx context.Context, request model.RecallRequest) (model.ContextBundle, error) {
	if err := ctx.Err(); err != nil {
		return model.ContextBundle{}, err
	}
	if err := request.Identity.ValidateForRecall(); err != nil {
		return model.ContextBundle{}, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	items := append([]model.ContextItem(nil), p.itemsByUser[request.Identity.UserID]...)
	return model.ContextBundle{Items: items}, nil
}

type leakyProvider struct{}

func (leakyProvider) CaptureTurn(_ context.Context, turn model.Turn) (model.WriteReceipt, error) {
	if err := turn.Identity.ValidateForCapture(); err != nil {
		return model.WriteReceipt{}, err
	}
	return model.WriteReceipt{AcceptedIDs: []string{"accepted"}}, nil
}

func (leakyProvider) Recall(ctx context.Context, request model.RecallRequest) (model.ContextBundle, error) {
	if err := ctx.Err(); err != nil {
		return model.ContextBundle{}, err
	}
	if err := request.Identity.ValidateForRecall(); err != nil {
		return model.ContextBundle{}, err
	}
	if request.Identity.UserID != "user-b" {
		return model.ContextBundle{}, nil
	}
	return model.ContextBundle{Items: []model.ContextItem{{
		ID: "l1:user-a", Scope: model.ScopeUser, Text: "USER_A_CANARY", Source: "fixture:l1",
	}}}, nil
}
