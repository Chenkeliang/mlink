package broker

import (
	"context"
	"errors"
	"time"

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
			return nil
		}
		w.lastReconcile = now
		reconciled, err := w.Journal.ReconcileCompleteFragments(ctx, 1)
		if err != nil || reconciled == 0 {
			return err
		}
		events, err = w.Journal.ClaimReady(ctx, now, 1)
		if err != nil || len(events) == 0 {
			return err
		}
	}
	event := events[0]
	receipt, captureErr := w.Provider.CaptureTurn(ctx, event.Route, host.CallMeta{IdempotencyKey: event.IdempotencyKey}, event.Turn)
	if captureErr == nil {
		if receipt.State == model.WriteVisible {
			return w.Journal.MarkVisible(ctx, event.ID, receipt)
		}
		return w.Journal.MarkAccepted(ctx, event.ID, receipt)
	}
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
