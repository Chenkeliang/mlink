package tencentdb

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"mlink/internal/connection"
	"mlink/internal/model"
	"mlink/internal/provider/protocol"
)

func TestServerHandlerLifecycle(t *testing.T) {
	var healthCalls, captureCalls, recallCalls int
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer memory-token" || r.Header.Get("x-tdai-service-id") != "service-a" {
			t.Errorf("backend auth headers are missing")
		}
		switch r.URL.Path {
		case "/health":
			healthCalls++
			if r.Method != http.MethodGet {
				t.Errorf("health method = %s", r.Method)
			}
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case "/v3/conversation/add":
			captureCalls++
			_, _ = w.Write([]byte(`{"code":0,"data":{"accepted_ids":["memory-a"]}}`))
		case "/v3/atomic/search":
			recallCalls++
			_, _ = w.Write([]byte(`{"code":0,"data":{"items":[{"id":"memory-a","type":"instruction","content":"先给结论"}]}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer backend.Close()

	handler := NewServerHandler()
	initialized, err := handler.Initialize(context.Background(), protocol.InitializeParams{
		Route:   connection.RouteKey{ProviderID: "dev.mlink.tencentdb", ProviderVersion: "0.1.0"},
		Config:  json.RawMessage(`{"base_url":` + quoteJSON(backend.URL) + `,"service_id":"service-a","timeout_ms":1500}`),
		Secrets: map[string]string{"token": "memory-token"},
	})
	if err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}
	if initialized.ProviderID != "dev.mlink.tencentdb" || initialized.ProviderVersion != "0.1.0" || initialized.ProtocolVersion != "1.0" {
		t.Fatalf("Initialize() = %#v", initialized)
	}
	if initialized.Capabilities["capture_turn"].ReplaySafe {
		t.Fatal("TencentDB capture_turn must not claim replay safety")
	}

	health, err := handler.Health(context.Background(), protocol.HealthParams{})
	if err != nil || health.Process != "ready" || health.Config != "valid" || health.Backend != "available" {
		t.Fatalf("Health() = %#v, %v", health, err)
	}
	receipt, err := handler.CaptureTurn(context.Background(), protocol.CaptureParams{Turn: model.Turn{
		Identity: model.IdentityScope{
			TenantID: "team-a", UserID: "user-a", AgentID: "agent-a", SessionID: "session-a", TurnID: "turn-a",
		},
		Messages: []model.Message{{Role: "user", Content: "hello"}},
	}})
	if err != nil || receipt.State != model.WriteAccepted || receipt.ProviderRefs[0] != "memory-a" {
		t.Fatalf("CaptureTurn() = %#v, %v", receipt, err)
	}
	bundle, err := handler.Recall(context.Background(), protocol.RecallParams{Request: model.RecallRequest{
		Identity: model.IdentityScope{TenantID: "team-a", UserID: "user-a", AgentID: "agent-a"},
		Query:    "偏好", MaxItems: 5,
	}})
	if err != nil || len(bundle.Items) != 1 || bundle.Items[0].Text != "先给结论" {
		t.Fatalf("Recall() = %#v, %v", bundle, err)
	}
	if err := handler.Shutdown(context.Background(), protocol.ShutdownParams{}); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	if healthCalls != 1 || captureCalls != 1 || recallCalls != 1 {
		t.Fatalf("backend calls health/capture/recall = %d/%d/%d", healthCalls, captureCalls, recallCalls)
	}
}

func TestServerHandlerRejectsUnsafeOrUnknownConfiguration(t *testing.T) {
	tests := []struct {
		name    string
		config  string
		secrets map[string]string
	}{
		{name: "external plaintext", config: `{"base_url":"http://memory.example.com","service_id":"service-a","timeout_ms":1000}`, secrets: map[string]string{"token": "token"}},
		{name: "timeout too short", config: `{"base_url":"https://memory.example.com","service_id":"service-a","timeout_ms":99}`, secrets: map[string]string{"token": "token"}},
		{name: "timeout too long", config: `{"base_url":"https://memory.example.com","service_id":"service-a","timeout_ms":30001}`, secrets: map[string]string{"token": "token"}},
		{name: "unknown config", config: `{"base_url":"https://memory.example.com","service_id":"service-a","timeout_ms":1000,"agent_model":"forbidden"}`, secrets: map[string]string{"token": "token"}},
		{name: "missing token", config: `{"base_url":"https://memory.example.com","service_id":"service-a","timeout_ms":1000}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := NewServerHandler()
			_, err := handler.Initialize(context.Background(), protocol.InitializeParams{
				Config: json.RawMessage(tt.config), Secrets: tt.secrets,
			})
			if err == nil {
				t.Fatal("Initialize() error = nil, want configuration rejection")
			}
		})
	}
}

func TestServerHandlerHealthDoesNotExposeBackendBody(t *testing.T) {
	const secretBody = "backend-private-diagnostic"
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(secretBody))
	}))
	defer backend.Close()
	handler := NewServerHandler()
	_, err := handler.Initialize(context.Background(), protocol.InitializeParams{
		Config:  json.RawMessage(`{"base_url":` + quoteJSON(backend.URL) + `,"service_id":"service-a","timeout_ms":1000}`),
		Secrets: map[string]string{"token": "token"},
	})
	if err != nil {
		t.Fatal(err)
	}
	health, err := handler.Health(context.Background(), protocol.HealthParams{})
	if err != nil {
		t.Fatalf("Health() error = %v", err)
	}
	if health.Backend != "unavailable" || len(health.Diagnostics) != 1 || health.Diagnostics[0] == secretBody {
		t.Fatalf("Health() = %#v", health)
	}
}

func TestServerHandlerAcceptsHTTPSAndLoopbackHTTP(t *testing.T) {
	for _, endpoint := range []string{"https://memory.example.com", "http://127.0.0.1:8420", "http://[::1]:8420", "http://localhost:8420"} {
		t.Run(endpoint, func(t *testing.T) {
			handler := NewServerHandler()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			_, err := handler.Initialize(ctx, protocol.InitializeParams{
				Config:  json.RawMessage(`{"base_url":` + quoteJSON(endpoint) + `,"service_id":"service-a","timeout_ms":1000}`),
				Secrets: map[string]string{"token": "token"},
			})
			if err != nil {
				t.Fatalf("Initialize() error = %v", err)
			}
		})
	}
}

func quoteJSON(value string) string {
	raw, _ := json.Marshal(value)
	return string(raw)
}
