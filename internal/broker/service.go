package broker

import (
	"context"
	"errors"
	"time"

	"mlink/internal/connection"
	"mlink/internal/journal"
	"mlink/internal/model"
	"mlink/internal/provider/host"
)

type Provider interface {
	CaptureTurn(context.Context, connection.RouteKey, host.CallMeta, model.Turn) (model.WriteReceipt, error)
	Recall(context.Context, connection.RouteKey, host.CallMeta, model.RecallRequest) (model.ContextBundle, error)
}

type SessionArchiver interface {
	ArchiveSession(context.Context, connection.RouteKey, host.CallMeta, model.IdentityScope) error
}

type Journal interface {
	RecordFragment(context.Context, journal.Fragment) error
	ReconcileCompleteFragments(context.Context, int) (int, error)
	EnqueueTurn(context.Context, journal.Envelope) (journal.Event, bool, error)
	ClaimReady(context.Context, time.Time, int) ([]journal.Event, error)
	MarkAccepted(context.Context, string, model.WriteReceipt) error
	MarkVisible(context.Context, string, model.WriteReceipt) error
	MarkRetryable(context.Context, string, string, time.Time) error
	MarkPermanent(context.Context, string, string) error
	MarkAmbiguous(context.Context, string, string) error
	FlushSession(context.Context, string, string) (int, error)
	RequestFinalization(context.Context, journal.FinalizationRequest) (journal.Finalization, bool, int, error)
	ClaimReadyFinalizations(context.Context, time.Time, int) ([]journal.Finalization, error)
	MarkFinalizationCompleted(context.Context, string) error
	MarkFinalizationRetryable(context.Context, string, string, time.Time) error
	MarkFinalizationPermanent(context.Context, string, string) error
	MarkFinalizationAmbiguous(context.Context, string, string) error
}

type SubmitReceipt struct {
	EventID string `json:"event_id"`
	Queued  bool   `json:"queued"`
}

type Service struct {
	Journal  Journal
	Provider Provider
}

func (s Service) SubmitTurn(ctx context.Context, envelope journal.Envelope) (SubmitReceipt, error) {
	if s.Journal == nil {
		return SubmitReceipt{}, errors.New("journal is unavailable")
	}
	event, inserted, err := s.Journal.EnqueueTurn(ctx, envelope)
	if err != nil {
		return SubmitReceipt{}, err
	}
	return SubmitReceipt{EventID: event.ID, Queued: inserted}, nil
}

func (s Service) SubmitFragment(ctx context.Context, fragment journal.Fragment) error {
	if s.Journal == nil {
		return errors.New("journal is unavailable")
	}
	return s.Journal.RecordFragment(ctx, fragment)
}

func (s Service) Recall(ctx context.Context, route connection.RouteKey, idempotencyKey string, request model.RecallRequest) (model.ContextBundle, error) {
	if s.Provider == nil {
		return model.ContextBundle{}, errors.New("Provider runtime is unavailable")
	}
	return s.Provider.Recall(ctx, route, host.CallMeta{IdempotencyKey: idempotencyKey}, request)
}

func (s Service) Flush(ctx context.Context, adapterID, sessionID string) (int, error) {
	if s.Journal == nil {
		return 0, errors.New("journal is unavailable")
	}
	return s.Journal.FlushSession(ctx, adapterID, sessionID)
}

func (s Service) FinalizeSession(ctx context.Context, request journal.FinalizationRequest) (int, error) {
	if s.Journal == nil {
		return 0, errors.New("journal is unavailable")
	}
	_, _, pending, err := s.Journal.RequestFinalization(ctx, request)
	return pending, err
}
