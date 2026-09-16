package host

import (
	"context"
	"errors"
	"testing"

	"mlink/internal/model"
	"mlink/internal/provider/protocol"
)

func TestCaptureOnExitedSessionIsDefinitelyNotSent(t *testing.T) {
	for _, state := range []State{StateFaulted, StateStopped} {
		s := &processSession{state: state}
		_, err := s.CaptureTurn(context.Background(), CallMeta{IdempotencyKey: "pending-turn"}, model.Turn{})
		var callErr *CallError
		if !errors.As(err, &callErr) || callErr.Code != protocol.ErrorTemporarilyUnavailable || callErr.Delivery != DeliveryNotSent {
			t.Fatalf("state %s: want retryable unsent error, got %v", state, err)
		}
	}
}
