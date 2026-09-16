package server

import (
	"context"
	"mlink/internal/model"
	"mlink/internal/provider/protocol"
)

type UserTurnHandler interface {
	ObserveUserTurn(context.Context, protocol.ObserveUserTurnParams) (model.ObservationReceipt, error)
}
