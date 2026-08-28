package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"mlink/internal/provider/manifest"
	"mlink/internal/provider/protocol"
)

const (
	defaultConcurrency = 4
	maximumConcurrency = 64
)

type Server struct {
	handler Handler
}

func New(handler Handler) *Server {
	return &Server{handler: handler}
}

type readResult struct {
	message protocol.Message
	err     error
}

type outbound struct {
	payload json.RawMessage
	written chan error
}

type completion struct {
	id      string
	payload json.RawMessage
}

type activeCall struct {
	cancel context.CancelFunc
	limit  chan struct{}
}

func (s *Server) Serve(ctx context.Context, input io.Reader, output io.Writer) error {
	if s == nil || s.handler == nil {
		return errors.New("provider server handler is not configured")
	}
	ctx, cancelServer := context.WithCancel(ctx)
	defer cancelServer()
	if closer, ok := input.(io.Closer); ok {
		defer closer.Close()
	}
	if closer, ok := output.(io.Closer); ok {
		defer closer.Close()
	}

	readCh := make(chan readResult, 1)
	go readMessages(ctx, protocol.NewDecoder(input), readCh)

	writeCh := make(chan outbound, maximumConcurrency+1)
	writerErr := make(chan error, 1)
	go writeMessages(ctx, protocol.NewEncoder(output), writeCh, writerErr)

	initialized := false
	stopping := false
	seenIDs := make(map[string]struct{})
	limits := make(map[string]chan struct{})
	active := make(map[string]activeCall)
	completed := make(chan completion, maximumConcurrency+1)
	var activeHandlers sync.WaitGroup
	shutdownComplete := make(chan completion, 1)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-writerErr:
			return err
		case result := <-readCh:
			if result.err != nil {
				if errors.Is(result.err, io.EOF) && stopping {
					continue
				}
				return result.err
			}
			message := result.message
			if !initialized {
				if message.Kind != protocol.MessageRequest || message.Method != "initialize" {
					return errors.New("initialize must be the first provider request")
				}
				initializeResult, payload, successful, err := s.initialize(ctx, message)
				if err != nil {
					return err
				}
				seenIDs[message.ID] = struct{}{}
				if !successful {
					if err := writeAndWait(ctx, writeCh, writerErr, payload); err != nil {
						return err
					}
					return errors.New("provider initialize failed")
				}
				initialized = true
				limits = capabilityLimits(initializeResult.Capabilities)
				if err := queueOutbound(ctx, writeCh, outbound{payload: payload}); err != nil {
					return err
				}
				continue
			}
			if message.Kind == protocol.MessageNotification {
				var params protocol.CancelParams
				if err := protocol.DecodeParams(message.Params, &params); err != nil {
					return errors.New("invalid cancel notification")
				}
				if call, exists := active[params.ID]; exists {
					call.cancel()
				}
				continue
			}
			if message.Kind != protocol.MessageRequest {
				return errors.New("provider server received a JSON-RPC response")
			}
			if _, duplicate := seenIDs[message.ID]; duplicate {
				payload := errorPayload(message.ID, protocol.ErrorProtocol, "duplicate JSON-RPC request ID")
				if err := queueOutbound(ctx, writeCh, outbound{payload: payload}); err != nil {
					return err
				}
				continue
			}
			seenIDs[message.ID] = struct{}{}
			if message.Method == "initialize" {
				payload := errorPayload(message.ID, protocol.ErrorProtocol, "initialize already completed")
				if err := queueOutbound(ctx, writeCh, outbound{payload: payload}); err != nil {
					return err
				}
				continue
			}
			if stopping {
				payload := errorPayload(message.ID, protocol.ErrorTemporarilyUnavailable, "provider is stopping")
				if err := queueOutbound(ctx, writeCh, outbound{payload: payload}); err != nil {
					return err
				}
				continue
			}
			if message.Method == "shutdown" {
				params, requestCtx, cancel, err := decodeShutdown(ctx, message.Params)
				if err != nil {
					payload := errorPayload(message.ID, protocol.ErrorProtocol, "invalid shutdown request")
					if err := queueOutbound(ctx, writeCh, outbound{payload: payload}); err != nil {
						return err
					}
					continue
				}
				stopping = true
				go s.finishShutdown(requestCtx, cancel, message.ID, params, &activeHandlers, shutdownComplete)
				continue
			}

			limit, supported := limits[message.Method]
			if !supported {
				payload := errorPayload(message.ID, protocol.ErrorUnsupportedCapability, "provider capability is unavailable")
				if err := queueOutbound(ctx, writeCh, outbound{payload: payload}); err != nil {
					return err
				}
				continue
			}
			select {
			case limit <- struct{}{}:
			default:
				payload := errorPayload(message.ID, protocol.ErrorRateLimited, "provider concurrency limit reached")
				if err := queueOutbound(ctx, writeCh, outbound{payload: payload}); err != nil {
					return err
				}
				continue
			}
			requestCtx, cancel, invoke, err := s.prepareCall(ctx, message)
			if err != nil {
				<-limit
				payload := errorPayload(message.ID, protocol.ErrorProtocol, "invalid provider request")
				if errors.Is(err, context.DeadlineExceeded) {
					payload = errorPayload(message.ID, protocol.ErrorDeadlineExceeded, "provider request deadline exceeded")
				}
				if err := queueOutbound(ctx, writeCh, outbound{payload: payload}); err != nil {
					return err
				}
				continue
			}
			active[message.ID] = activeCall{cancel: cancel, limit: limit}
			activeHandlers.Add(1)
			go func(id string) {
				defer activeHandlers.Done()
				payload := invoke(requestCtx, id)
				select {
				case completed <- completion{id: id, payload: payload}:
				case <-ctx.Done():
				}
			}(message.ID)

		case result := <-completed:
			if call, exists := active[result.id]; exists {
				delete(active, result.id)
				call.cancel()
				<-call.limit
			}
			if err := queueOutbound(ctx, writeCh, outbound{payload: result.payload}); err != nil {
				return err
			}

		case result := <-shutdownComplete:
			written := make(chan error, 1)
			if err := queueOutbound(ctx, writeCh, outbound{payload: result.payload, written: written}); err != nil {
				return err
			}
			select {
			case err := <-written:
				return err
			case err := <-writerErr:
				return err
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
}

func readMessages(ctx context.Context, decoder *protocol.Decoder, target chan<- readResult) {
	for {
		raw, err := decoder.ReadFrame()
		if err != nil {
			select {
			case target <- readResult{err: err}:
			case <-ctx.Done():
			}
			return
		}
		message, err := protocol.ParseMessage(raw)
		select {
		case target <- readResult{message: message, err: err}:
		case <-ctx.Done():
			return
		}
		if err != nil {
			return
		}
	}
}

func writeMessages(ctx context.Context, encoder *protocol.Encoder, source <-chan outbound, failed chan<- error) {
	for {
		select {
		case <-ctx.Done():
			return
		case item := <-source:
			err := encoder.WriteFrame(item.payload)
			if item.written != nil {
				item.written <- err
			}
			if err != nil {
				select {
				case failed <- err:
				default:
				}
				return
			}
		}
	}
}

func (s *Server) initialize(
	ctx context.Context,
	message protocol.Message,
) (protocol.InitializeResult, json.RawMessage, bool, error) {
	var params protocol.InitializeParams
	if err := protocol.DecodeParams(message.Params, &params); err != nil {
		return protocol.InitializeResult{}, errorPayload(
			message.ID, protocol.ErrorProtocol, "invalid initialize request",
		), false, nil
	}
	requestCtx, cancel, err := contextForMeta(ctx, params.Meta)
	if err != nil {
		return protocol.InitializeResult{}, errorPayloadFor(message.ID, err), false, nil
	}
	defer cancel()
	result, handlerErr := s.handler.Initialize(requestCtx, params)
	if handlerErr != nil {
		return result, errorPayloadFor(message.ID, handlerErr), false, nil
	}
	payload, err := protocol.EncodeResult(message.ID, result)
	if err != nil {
		return protocol.InitializeResult{}, nil, false, fmt.Errorf("encode initialize result: %w", err)
	}
	return result, payload, true, nil
}

func (s *Server) prepareCall(
	ctx context.Context,
	message protocol.Message,
) (context.Context, context.CancelFunc, func(context.Context, string) json.RawMessage, error) {
	switch message.Method {
	case "health":
		var params protocol.HealthParams
		if err := protocol.DecodeParams(message.Params, &params); err != nil {
			return nil, nil, nil, err
		}
		requestCtx, cancel, err := contextForMeta(ctx, params.Meta)
		if err != nil {
			return nil, nil, nil, err
		}
		return requestCtx, cancel, func(callCtx context.Context, id string) json.RawMessage {
			result, err := s.handler.Health(callCtx, params)
			return resultPayload(id, result, err)
		}, nil
	case "capture_turn":
		var params protocol.CaptureParams
		if err := protocol.DecodeParams(message.Params, &params); err != nil {
			return nil, nil, nil, err
		}
		requestCtx, cancel, err := contextForMeta(ctx, params.Meta)
		if err != nil {
			return nil, nil, nil, err
		}
		return requestCtx, cancel, func(callCtx context.Context, id string) json.RawMessage {
			result, err := s.handler.CaptureTurn(callCtx, params)
			return resultPayload(id, result, err)
		}, nil
	case "recall":
		var params protocol.RecallParams
		if err := protocol.DecodeParams(message.Params, &params); err != nil {
			return nil, nil, nil, err
		}
		requestCtx, cancel, err := contextForMeta(ctx, params.Meta)
		if err != nil {
			return nil, nil, nil, err
		}
		return requestCtx, cancel, func(callCtx context.Context, id string) json.RawMessage {
			result, err := s.handler.Recall(callCtx, params)
			return resultPayload(id, result, err)
		}, nil
	default:
		return nil, nil, nil, errors.New("unsupported provider request")
	}
}

func decodeShutdown(
	ctx context.Context,
	raw json.RawMessage,
) (protocol.ShutdownParams, context.Context, context.CancelFunc, error) {
	var params protocol.ShutdownParams
	if err := protocol.DecodeParams(raw, &params); err != nil {
		return params, nil, nil, err
	}
	requestCtx, cancel, err := contextForMeta(ctx, params.Meta)
	return params, requestCtx, cancel, err
}

func (s *Server) finishShutdown(
	ctx context.Context,
	cancel context.CancelFunc,
	id string,
	params protocol.ShutdownParams,
	active *sync.WaitGroup,
	target chan<- completion,
) {
	defer cancel()
	waited := make(chan struct{})
	go func() {
		active.Wait()
		close(waited)
	}()
	select {
	case <-waited:
	case <-ctx.Done():
		target <- completion{id: id, payload: errorPayloadFor(id, ctx.Err())}
		return
	}
	err := s.handler.Shutdown(ctx, params)
	target <- completion{id: id, payload: resultPayload(id, struct{}{}, err)}
}

func contextForMeta(parent context.Context, meta protocol.RequestMeta) (context.Context, context.CancelFunc, error) {
	if meta.RequestID == "" || meta.DeadlineUnixMS <= 0 {
		return nil, nil, errors.New("request metadata is incomplete")
	}
	deadline := time.UnixMilli(meta.DeadlineUnixMS)
	if !deadline.After(time.Now()) {
		return nil, nil, context.DeadlineExceeded
	}
	ctx, cancel := context.WithDeadline(parent, deadline)
	return ctx, cancel, nil
}

func capabilityLimits(capabilities map[string]manifest.CapabilityDescriptor) map[string]chan struct{} {
	result := make(map[string]chan struct{}, len(capabilities))
	for name, descriptor := range capabilities {
		limit := descriptor.MaxInFlight
		if limit <= 0 {
			limit = defaultConcurrency
		}
		if limit > maximumConcurrency {
			limit = maximumConcurrency
		}
		result[name] = make(chan struct{}, limit)
	}
	return result
}

func resultPayload(id string, result any, err error) json.RawMessage {
	if err != nil {
		return errorPayloadFor(id, err)
	}
	payload, encodeErr := protocol.EncodeResult(id, result)
	if encodeErr != nil {
		return errorPayload(id, protocol.ErrorPermanentFailure, "provider result could not be encoded")
	}
	return payload
}

func errorPayloadFor(id string, err error) json.RawMessage {
	code := protocol.ErrorPermanentFailure
	message := "provider request failed"
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		code = protocol.ErrorDeadlineExceeded
		message = "provider request deadline exceeded"
	} else {
		var handlerErr *HandlerError
		if errors.As(err, &handlerErr) {
			code = handlerErr.Code
			message = handlerErr.Message
		}
	}
	return errorPayload(id, code, message)
}

func errorPayload(id string, code protocol.ErrorCode, message string) json.RawMessage {
	payload, err := protocol.EncodeError(id, protocol.RPCError{Code: -32000, Message: message, ErrorCode: code})
	if err != nil {
		fallback, _ := protocol.EncodeError(id, protocol.RPCError{
			Code: -32603, Message: "provider internal error", ErrorCode: protocol.ErrorPermanentFailure,
		})
		return fallback
	}
	return payload
}

func queueOutbound(ctx context.Context, target chan<- outbound, item outbound) error {
	select {
	case target <- item:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func writeAndWait(
	ctx context.Context,
	target chan<- outbound,
	writerErr <-chan error,
	payload json.RawMessage,
) error {
	written := make(chan error, 1)
	if err := queueOutbound(ctx, target, outbound{payload: payload, written: written}); err != nil {
		return err
	}
	select {
	case err := <-written:
		return err
	case err := <-writerErr:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}
