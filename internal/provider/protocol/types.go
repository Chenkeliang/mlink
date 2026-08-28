package protocol

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"mlink/internal/connection"
	"mlink/internal/model"
	"mlink/internal/provider/manifest"
)

const Version = "1.0"

type ErrorCode string

const (
	ErrorConfiguration          ErrorCode = "configuration_error"
	ErrorAuthenticationFailed   ErrorCode = "authentication_failed"
	ErrorInvalidIdentity        ErrorCode = "invalid_identity"
	ErrorUnsupportedCapability  ErrorCode = "unsupported_capability"
	ErrorDeadlineExceeded       ErrorCode = "deadline_exceeded"
	ErrorRateLimited            ErrorCode = "rate_limited"
	ErrorTemporarilyUnavailable ErrorCode = "temporarily_unavailable"
	ErrorPermanentFailure       ErrorCode = "permanent_failure"
	ErrorAmbiguousResult        ErrorCode = "ambiguous_result"
	ErrorProtocol               ErrorCode = "protocol_error"
)

var stableErrorCodes = map[ErrorCode]struct{}{
	ErrorConfiguration:          {},
	ErrorAuthenticationFailed:   {},
	ErrorInvalidIdentity:        {},
	ErrorUnsupportedCapability:  {},
	ErrorDeadlineExceeded:       {},
	ErrorRateLimited:            {},
	ErrorTemporarilyUnavailable: {},
	ErrorPermanentFailure:       {},
	ErrorAmbiguousResult:        {},
	ErrorProtocol:               {},
}

type CallMeta struct {
	IdempotencyKey string `json:"idempotency_key,omitempty"`
}

type RequestMeta struct {
	RequestID      string `json:"request_id"`
	DeadlineUnixMS int64  `json:"deadline_unix_ms"`
	IdempotencyKey string `json:"idempotency_key,omitempty"`
}

type InitializeParams struct {
	Meta             RequestMeta         `json:"meta"`
	ProtocolVersions []string            `json:"protocol_versions"`
	Route            connection.RouteKey `json:"route"`
	Config           json.RawMessage     `json:"config"`
	Secrets          map[string]string   `json:"secrets"`
}

type InitializeResult struct {
	ProviderID      string                                   `json:"provider_id"`
	ProviderVersion string                                   `json:"provider_version"`
	ProtocolVersion string                                   `json:"protocol_version"`
	Capabilities    map[string]manifest.CapabilityDescriptor `json:"capabilities"`
}

type HealthParams struct {
	Meta RequestMeta `json:"meta"`
}

type CaptureParams struct {
	Meta RequestMeta `json:"meta"`
	Turn model.Turn  `json:"turn"`
}

type RecallParams struct {
	Meta    RequestMeta         `json:"meta"`
	Request model.RecallRequest `json:"request"`
}

type ShutdownParams struct {
	Meta RequestMeta `json:"meta"`
}

type CancelParams struct {
	ID string `json:"id"`
}

type HealthResult struct {
	Process     string   `json:"process"`
	Config      string   `json:"config"`
	Backend     string   `json:"backend"`
	Diagnostics []string `json:"diagnostics,omitempty"`
}

type MessageKind uint8

const (
	MessageRequest MessageKind = iota + 1
	MessageNotification
	MessageResponse
)

type Message struct {
	Kind   MessageKind
	ID     string
	Method string
	Params json.RawMessage
	Result json.RawMessage
	Error  *RPCError
}

type RPCError struct {
	Code      int
	Message   string
	ErrorCode ErrorCode
}

type wireMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   json.RawMessage `json:"error,omitempty"`
}

type wireError struct {
	Code    int           `json:"code"`
	Message string        `json:"message"`
	Data    wireErrorData `json:"data"`
}

type wireErrorData struct {
	Code ErrorCode `json:"code"`
}

var knownRequestMethods = map[string]struct{}{
	"initialize":   {},
	"health":       {},
	"capture_turn": {},
	"recall":       {},
	"shutdown":     {},
}

func ParseMessage(raw []byte) (Message, error) {
	var wire wireMessage
	if err := decodeStrict(raw, &wire); err != nil {
		return Message{}, fmt.Errorf("decode JSON-RPC message: %w", err)
	}
	if wire.JSONRPC != "2.0" {
		return Message{}, errors.New("JSON-RPC version must be 2.0")
	}
	if wire.Method != "" {
		if len(wire.Result) != 0 || len(wire.Error) != 0 || len(wire.Params) == 0 {
			return Message{}, errors.New("JSON-RPC request has invalid fields")
		}
		if wire.Method == "$/cancelRequest" {
			if len(wire.ID) != 0 {
				return Message{}, errors.New("cancel must be a notification")
			}
			var params CancelParams
			if err := decodeStrict(wire.Params, &params); err != nil || !validDecimalID(params.ID) {
				return Message{}, errors.New("cancel contains an invalid request ID")
			}
			return Message{Kind: MessageNotification, Method: wire.Method, Params: wire.Params}, nil
		}
		id, err := parseID(wire.ID)
		if err != nil {
			return Message{}, err
		}
		return Message{Kind: MessageRequest, ID: id, Method: wire.Method, Params: wire.Params}, nil
	}

	id, err := parseID(wire.ID)
	if err != nil {
		return Message{}, err
	}
	if len(wire.Params) != 0 || (len(wire.Result) == 0) == (len(wire.Error) == 0) {
		return Message{}, errors.New("JSON-RPC response must contain exactly one of result or error")
	}
	message := Message{Kind: MessageResponse, ID: id, Result: wire.Result}
	if len(wire.Error) != 0 {
		var failure wireError
		if err := decodeStrict(wire.Error, &failure); err != nil {
			return Message{}, errors.New("JSON-RPC response contains an invalid error")
		}
		if failure.Message == "" {
			return Message{}, errors.New("JSON-RPC error message is empty")
		}
		if _, stable := stableErrorCodes[failure.Data.Code]; !stable {
			return Message{}, errors.New("JSON-RPC response contains an unknown stable error code")
		}
		message.Error = &RPCError{Code: failure.Code, Message: failure.Message, ErrorCode: failure.Data.Code}
	}
	return message, nil
}

func EncodeRequest(id, method string, params any) (json.RawMessage, error) {
	if !validDecimalID(id) {
		return nil, errors.New("JSON-RPC request ID must be a decimal string")
	}
	if _, known := knownRequestMethods[method]; !known {
		return nil, fmt.Errorf("unknown JSON-RPC method %q", method)
	}
	paramsRaw, err := marshalObject(params)
	if err != nil {
		return nil, err
	}
	return json.Marshal(struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      string          `json:"id"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params"`
	}{JSONRPC: "2.0", ID: id, Method: method, Params: paramsRaw})
}

func EncodeNotification(method string, params any) (json.RawMessage, error) {
	if method != "$/cancelRequest" {
		return nil, errors.New("only $/cancelRequest notification is supported")
	}
	paramsRaw, err := marshalObject(params)
	if err != nil {
		return nil, err
	}
	return json.Marshal(struct {
		JSONRPC string          `json:"jsonrpc"`
		Method  string          `json:"method"`
		Params  json.RawMessage `json:"params"`
	}{JSONRPC: "2.0", Method: method, Params: paramsRaw})
}

func EncodeResult(id string, result any) (json.RawMessage, error) {
	if !validDecimalID(id) {
		return nil, errors.New("JSON-RPC response ID must be a decimal string")
	}
	resultRaw, err := json.Marshal(result)
	if err != nil {
		return nil, fmt.Errorf("encode JSON-RPC result: %w", err)
	}
	return json.Marshal(struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      string          `json:"id"`
		Result  json.RawMessage `json:"result"`
	}{JSONRPC: "2.0", ID: id, Result: resultRaw})
}

func EncodeError(id string, failure RPCError) (json.RawMessage, error) {
	if !validDecimalID(id) {
		return nil, errors.New("JSON-RPC response ID must be a decimal string")
	}
	if failure.Message == "" {
		return nil, errors.New("JSON-RPC error message is empty")
	}
	if _, stable := stableErrorCodes[failure.ErrorCode]; !stable {
		return nil, errors.New("JSON-RPC error code is not stable")
	}
	return json.Marshal(struct {
		JSONRPC string    `json:"jsonrpc"`
		ID      string    `json:"id"`
		Error   wireError `json:"error"`
	}{
		JSONRPC: "2.0",
		ID:      id,
		Error: wireError{
			Code: failure.Code, Message: failure.Message, Data: wireErrorData{Code: failure.ErrorCode},
		},
	})
}

func parseID(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "", errors.New("JSON-RPC message requires an ID")
	}
	var id string
	if err := json.Unmarshal(raw, &id); err != nil || !validDecimalID(id) {
		return "", errors.New("JSON-RPC ID must be a decimal string")
	}
	return id, nil
}

func validDecimalID(id string) bool {
	if id == "" {
		return false
	}
	for i := range len(id) {
		if id[i] < '0' || id[i] > '9' {
			return false
		}
	}
	return true
}

func marshalObject(value any) (json.RawMessage, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encode JSON-RPC params: %w", err)
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) < 2 || trimmed[0] != '{' || trimmed[len(trimmed)-1] != '}' {
		return nil, errors.New("JSON-RPC params must be an object")
	}
	return raw, nil
}

func DecodeParams(raw []byte, target any) error {
	return decodeStrict(raw, target)
}

func decodeStrict(raw []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}
