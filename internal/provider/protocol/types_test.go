package protocol

import (
	"encoding/json"
	"strings"
	"testing"

	"mlink/internal/model"
)

func TestParseMessagePreservesLargeDecimalStringID(t *testing.T) {
	const id = "900719925474099312345678901234567890"
	raw, err := EncodeRequest(id, "health", HealthParams{Meta: RequestMeta{RequestID: "nonce-1"}})
	if err != nil {
		t.Fatalf("EncodeRequest() error = %v", err)
	}
	message, err := ParseMessage(raw)
	if err != nil {
		t.Fatalf("ParseMessage() error = %v", err)
	}
	if message.ID != id || message.Kind != MessageRequest || message.Method != "health" {
		t.Fatalf("message = %#v", message)
	}
}

func TestRequestIDHasByteLimit(t *testing.T) {
	id := strings.Repeat("9", 65)
	if _, err := EncodeRequest(id, "health", HealthParams{}); err == nil {
		t.Fatal("EncodeRequest() accepted a 65-byte request ID")
	}
	raw := []byte(`{"jsonrpc":"2.0","id":"` + id + `","method":"health","params":{}}`)
	if _, err := ParseMessage(raw); err == nil {
		t.Fatal("ParseMessage() accepted a 65-byte request ID")
	}
}

func TestParseMessageRejectsInvalidJSONRPC(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{name: "batch", raw: `[{"jsonrpc":"2.0","id":"1","method":"health","params":{}}]`},
		{name: "wrong version", raw: `{"jsonrpc":"1.0","id":"1","method":"health","params":{}}`},
		{name: "numeric id", raw: `{"jsonrpc":"2.0","id":1,"method":"health","params":{}}`},
		{name: "non decimal id", raw: `{"jsonrpc":"2.0","id":"request-1","method":"health","params":{}}`},
		{name: "request has result", raw: `{"jsonrpc":"2.0","id":"1","method":"health","params":{},"result":{}}`},
		{name: "response both result and error", raw: `{"jsonrpc":"2.0","id":"1","result":{},"error":{"code":-32000,"message":"failed","data":{"code":"permanent_failure"}}}`},
		{name: "response neither result nor error", raw: `{"jsonrpc":"2.0","id":"1"}`},
		{name: "cancel with id", raw: `{"jsonrpc":"2.0","id":"1","method":"$/cancelRequest","params":{"id":"1"}}`},
		{name: "business notification", raw: `{"jsonrpc":"2.0","method":"health","params":{}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := ParseMessage([]byte(tt.raw)); err == nil {
				t.Fatal("ParseMessage() error = nil, want validation error")
			}
		})
	}
}

func TestParseMessageAcceptsUnknownRequestMethodForServerDispatch(t *testing.T) {
	message, err := ParseMessage([]byte(`{"jsonrpc":"2.0","id":"2","method":"future_recall","params":{}}`))
	if err != nil {
		t.Fatalf("ParseMessage() error = %v", err)
	}
	if message.Kind != MessageRequest || message.ID != "2" || message.Method != "future_recall" {
		t.Fatalf("message = %#v", message)
	}
	if _, err := EncodeRequest("2", "future_recall", map[string]string{}); err == nil {
		t.Fatal("EncodeRequest() accepted a method unsupported by this Host")
	}
}

func TestEncodeAndParseResponseResultOrError(t *testing.T) {
	resultRaw, err := EncodeResult("7", map[string]string{"status": "ready"})
	if err != nil {
		t.Fatalf("EncodeResult() error = %v", err)
	}
	result, err := ParseMessage(resultRaw)
	if err != nil {
		t.Fatalf("ParseMessage(result) error = %v", err)
	}
	if result.Kind != MessageResponse || result.Error != nil || len(result.Result) == 0 {
		t.Fatalf("result message = %#v", result)
	}

	errorRaw, err := EncodeError("8", RPCError{
		Code: -32000, Message: "backend unavailable", ErrorCode: ErrorTemporarilyUnavailable,
	})
	if err != nil {
		t.Fatalf("EncodeError() error = %v", err)
	}
	failure, err := ParseMessage(errorRaw)
	if err != nil {
		t.Fatalf("ParseMessage(error) error = %v", err)
	}
	if failure.Error == nil || failure.Error.ErrorCode != ErrorTemporarilyUnavailable {
		t.Fatalf("error message = %#v", failure)
	}
	if !json.Valid(failure.Result) && len(failure.Result) != 0 {
		t.Fatalf("unexpected result = %s", failure.Result)
	}
}

func TestEncodeArchiveSessionRequest(t *testing.T) {
	raw, err := EncodeRequest("4", "archive_session", ArchiveSessionParams{
		Meta: RequestMeta{RequestID: "archive-4"},
		Identity: model.IdentityScope{
			ConnectionID: "local", TenantID: "team-a", AgentID: "agent-a",
			UserID: "user-a", SessionID: "session-a",
		},
	})
	if err != nil {
		t.Fatalf("EncodeRequest() error = %v", err)
	}
	message, err := ParseMessage(raw)
	if err != nil || message.Method != "archive_session" || message.ID != "4" {
		t.Fatalf("message = %#v, %v", message, err)
	}
}

func TestEncodeNotificationAllowsOnlyCancel(t *testing.T) {
	raw, err := EncodeNotification("$/cancelRequest", CancelParams{ID: "9"})
	if err != nil {
		t.Fatalf("EncodeNotification() error = %v", err)
	}
	message, err := ParseMessage(raw)
	if err != nil {
		t.Fatalf("ParseMessage() error = %v", err)
	}
	if message.Kind != MessageNotification || message.Method != "$/cancelRequest" {
		t.Fatalf("notification = %#v", message)
	}
	if _, err := EncodeNotification("health", HealthParams{}); err == nil {
		t.Fatal("EncodeNotification(health) error = nil, want rejection")
	}
}
