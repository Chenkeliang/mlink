package host

import (
	"context"
	"time"

	"mlink/internal/connection"
	"mlink/internal/model"
	"mlink/internal/provider/manifest"
	"mlink/internal/provider/protocol"
)

type State string

const (
	StateNew          State = "new"
	StateStarting     State = "starting"
	StateInitializing State = "initializing"
	StateReady        State = "ready"
	StateStopping     State = "stopping"
	StateStopped      State = "stopped"
	StateFaulted      State = "faulted"
)

type DeliveryState string

const (
	DeliveryNotSent   DeliveryState = "not_sent"
	DeliveryMaybeSent DeliveryState = "maybe_sent"
	DeliverySent      DeliveryState = "sent"
)

type CallError struct {
	Code           protocol.ErrorCode
	RequestID      string
	IdempotencyKey string
	Delivery       DeliveryState
	ReplaySafe     bool
	Message        string
}

func (e *CallError) Error() string {
	if e == nil || e.Message == "" {
		return "provider call failed"
	}
	return e.Message
}

type ExitEvent struct {
	Expected   bool      `json:"expected"`
	ExitCode   int       `json:"exit_code"`
	ErrorCode  string    `json:"error_code"`
	OccurredAt time.Time `json:"occurred_at"`
}

type CallMeta struct {
	IdempotencyKey string
}

type Session interface {
	RouteKey() connection.RouteKey
	Capabilities() map[string]manifest.CapabilityDescriptor
	Health(context.Context) (protocol.HealthResult, error)
	CaptureTurn(context.Context, CallMeta, model.Turn) (model.WriteReceipt, error)
	Recall(context.Context, CallMeta, model.RecallRequest) (model.ContextBundle, error)
	Shutdown(context.Context) error
	State() State
	Done() <-chan struct{}
	ExitEvent() (ExitEvent, bool)
}

type revisionPaths struct {
	Home string
	Temp string
}
