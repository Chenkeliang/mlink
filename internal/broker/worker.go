package broker

import (
	"context"
	"errors"
	"time"

	"mlink/internal/journal"
	"mlink/internal/model"
	"mlink/internal/provider/host"
	"mlink/internal/provider/protocol"
)

type Worker struct {
	Journal       Journal
	Provider      Provider
	Clock         func() time.Time
	lastReconcile time.Time
}

const fragmentReconcileInterval = 30 * time.Second

func (w *Worker) DrainOne(ctx context.Context) error {
	if w.Journal == nil || w.Provider == nil {
		return errors.New("worker Journal and Provider are required")
	}
	now := time.Now().UTC()
	if w.Clock != nil {
		now = w.Clock().UTC()
	}
	events, err := w.Journal.ClaimReady(ctx, now, 1)
	if err != nil {
		return err
	}
	if len(events) == 0 {
		if !w.lastReconcile.IsZero() && now.Before(w.lastReconcile.Add(fragmentReconcileInterval)) {
			return w.drainFinalization(ctx, now)
		}
		w.lastReconcile = now
		reconciled, err := w.Journal.ReconcileCompleteFragments(ctx, 1)
		if err != nil {
			return err
		}
		if reconciled == 0 {
			return w.drainFinalization(ctx, now)
		}
		events, err = w.Journal.ClaimReady(ctx, now, 1)
		if err != nil {
			return err
		}
		if len(events) == 0 {
			return w.drainFinalization(ctx, now)
		}
	}
	event := events[0]
	receipt, captureErr := w.Provider.CaptureTurn(ctx, event.Route, host.CallMeta{IdempotencyKey: event.IdempotencyKey}, event.Turn)
	if captureErr == nil {
		if receipt.State == model.WriteVisible {
			err = w.Journal.MarkVisible(ctx, event.ID, receipt)
		} else {
			err = w.Journal.MarkAccepted(ctx, event.ID, receipt)
		}
	} else {
		err = w.markTurnFailure(ctx, event, captureErr, now)
	}
	if err != nil {
		return err
	}
	return w.drainFinalization(ctx, now)
}

func (w *Worker) markTurnFailure(ctx context.Context, event journal.Event, captureErr error, now time.Time) error {
	var callErr *host.CallError
	if !errors.As(captureErr, &callErr) {
		return w.Journal.MarkPermanent(ctx, event.ID, "provider_failure")
	}
	code := string(callErr.Code)
	if code == "" {
		code = "provider_failure"
	}
	if callErr.Delivery != host.DeliveryNotSent && !callErr.ReplaySafe {
		return w.Journal.MarkAmbiguous(ctx, event.ID, code)
	}
	if transient(callErr.Code) && (callErr.Delivery == host.DeliveryNotSent || callErr.ReplaySafe) {
		return w.Journal.MarkRetryable(ctx, event.ID, code, now.Add(retryDelay(event.AttemptCount)))
	}
	return w.Journal.MarkPermanent(ctx, event.ID, code)
}

func (w *Worker) drainFinalization(ctx context.Context, now time.Time) error {
	ready, err := w.Journal.ClaimReadyFinalizations(ctx, now, 1)
	if err != nil || len(ready) == 0 {
		return err
	}
	finalization := ready[0]
	archiver, supported := w.Provider.(SessionArchiver)
	if !supported {
		return w.Journal.MarkFinalizationCompleted(ctx, finalization.ID)
	}
	archiveErr := archiver.ArchiveSession(
		ctx, finalization.Route,
		host.CallMeta{IdempotencyKey: finalization.IdempotencyKey},
		finalization.Identity,
	)
	if archiveErr == nil || errors.Is(archiveErr, host.ErrCapabilityUnavailable) {
		return w.Journal.MarkFinalizationCompleted(ctx, finalization.ID)
	}
	var callErr *host.CallError
	if !errors.As(archiveErr, &callErr) {
		return w.Journal.MarkFinalizationPermanent(ctx, finalization.ID, "provider_failure")
	}
	code := string(callErr.Code)
	if code == "" {
		code = "provider_failure"
	}
	if callErr.Delivery != host.DeliveryNotSent && !callErr.ReplaySafe {
		return w.Journal.MarkFinalizationAmbiguous(ctx, finalization.ID, code)
	}
	if transient(callErr.Code) && (callErr.Delivery == host.DeliveryNotSent || callErr.ReplaySafe) {
		return w.Journal.MarkFinalizationRetryable(ctx, finalization.ID, code, now.Add(retryDelay(finalization.AttemptCount)))
	}
	return w.Journal.MarkFinalizationPermanent(ctx, finalization.ID, code)
}

func transient(code protocol.ErrorCode) bool {
	return code == protocol.ErrorDeadlineExceeded || code == protocol.ErrorRateLimited || code == protocol.ErrorTemporarilyUnavailable
}

func retryDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	if attempt > 9 {
		attempt = 9
	}
	return time.Second * time.Duration(1<<(attempt-1))
}
