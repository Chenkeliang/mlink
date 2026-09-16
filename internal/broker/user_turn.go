package broker

import (
	"context"
	"mlink/internal/connection"
	"mlink/internal/journal"
	"mlink/internal/model"
	"mlink/internal/provider/host"
)

type userTurnObserver interface {
	ObserveUserTurn(context.Context, connection.RouteKey, host.CallMeta, model.UserTurn) (model.ObservationReceipt, error)
}

type observationJournal interface {
	ClaimObservation(context.Context, string, connection.RouteKey, model.UserTurn) (string, bool, error)
	SaveObservation(context.Context, string, model.ObservationReceipt) error
	PreviousMessages(context.Context, string, model.IdentityScope) ([]model.Message, error)
	CompleteObservation(context.Context, string, model.IdentityScope, []model.Message) error
	EndSessionObservations(context.Context, string, model.IdentityScope) error
}

func (s Service) observeFragment(ctx context.Context, f journal.Fragment) error {
	j, ok := s.Journal.(observationJournal)
	if !ok {
		return nil
	}
	if f.Role == "assistant" {
		return j.CompleteObservation(ctx, f.AdapterID, f.Identity, []model.Message{{Role: f.Role, Content: f.Content, OccurredAt: f.OccurredAt}})
	}
	if f.Role != "user" {
		return nil
	}
	p, ok := s.Provider.(userTurnObserver)
	if !ok {
		return nil
	}
	previous, err := j.PreviousMessages(ctx, f.AdapterID, f.Identity)
	if err != nil {
		return err
	}
	turn := model.UserTurn{Identity: f.Identity, Message: model.Message{Role: "user", Content: f.Content, OccurredAt: f.OccurredAt}, Previous: previous, Correction: model.IsExplicitCorrection(f.Content)}
	id, claimed, err := j.ClaimObservation(ctx, f.AdapterID, f.Route, turn)
	if err != nil || !claimed {
		return err
	}
	receipt, err := p.ObserveUserTurn(ctx, f.Route, host.CallMeta{IdempotencyKey: id}, turn)
	if err != nil {
		receipt = model.ObservationReceipt{CorrectionStatus: "failed", Error: "provider_observation_failed"}
	}
	return j.SaveObservation(context.WithoutCancel(ctx), id, receipt)
}
