package host

import (
	"time"

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

type revisionPaths struct {
	Home string
	Temp string
}
