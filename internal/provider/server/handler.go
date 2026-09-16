package server

import (
	"context"

	"mlink/internal/model"
	"mlink/internal/provider/protocol"
)

type Handler interface {
	Initialize(context.Context, protocol.InitializeParams) (protocol.InitializeResult, error)
	Health(context.Context, protocol.HealthParams) (protocol.HealthResult, error)
	CaptureTurn(context.Context, protocol.CaptureParams) (model.WriteReceipt, error)
	Recall(context.Context, protocol.RecallParams) (model.ContextBundle, error)
	Shutdown(context.Context, protocol.ShutdownParams) error
}

type ArchiveSessionHandler interface {
	ArchiveSession(context.Context, protocol.ArchiveSessionParams) (protocol.ArchiveSessionResult, error)
}

type HandlerError struct {
	Code    protocol.ErrorCode
	Message string
}

func (e *HandlerError) Error() string {
	if e == nil {
		return "provider handler failed"
	}
	return e.Message
}
