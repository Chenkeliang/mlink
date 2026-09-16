package host

import (
	"context"
	"errors"
	"mlink/internal/model"
	"mlink/internal/provider/protocol"
	"time"
)

type UserTurnObserver interface {
	ObserveUserTurn(context.Context, CallMeta, model.UserTurn) (model.ObservationReceipt, error)
}

func (s *processSession) ObserveUserTurn(ctx context.Context, meta CallMeta, turn model.UserTurn) (model.ObservationReceipt, error) {
	descriptor, err := s.requireReadyCapability("observe_user_turn")
	if err != nil {
		return model.ObservationReceipt{}, err
	}
	if turn.Identity.ConnectionID != s.route.ConnectionID || (!turn.Ended && turn.Message.Role != "user") {
		return model.ObservationReceipt{}, errors.New("invalid user-turn identity or role")
	}
	if err := turn.Identity.ValidateForCapture(); err != nil {
		return model.ObservationReceipt{}, err
	}
	callCtx, cancel := withDefaultDeadline(ctx, 5*time.Second)
	defer cancel()
	// Control-plane activity must not queue behind a slow capture/recall.
	message, _, err := s.callRPC(callCtx, meta.IdempotencyKey, "observe_user_turn", true, func(m protocol.RequestMeta) (any, error) {
		p := protocol.ObserveUserTurnParams{Meta: m, Turn: turn}
		if err := validateRequestSize(p, descriptor.MaxRequestBytes); err != nil {
			return nil, err
		}
		return p, nil
	})
	if err != nil {
		return model.ObservationReceipt{}, err
	}
	var receipt model.ObservationReceipt
	if err := protocol.DecodeParams(message.Result, &receipt); err != nil {
		return receipt, err
	}
	return receipt, nil
}
