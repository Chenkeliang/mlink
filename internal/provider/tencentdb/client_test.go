package tencentdb

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNewClientRejectsInvalidConfig(t *testing.T) {
	tests := []struct {
		name   string
		config Config
	}{
		{name: "missing base URL", config: Config{Token: "secret", ServiceID: "service-a"}},
		{name: "relative base URL", config: Config{BaseURL: "/v1", Token: "secret", ServiceID: "service-a"}},
		{name: "unsupported scheme", config: Config{BaseURL: "ftp://memory.test", Token: "secret", ServiceID: "service-a"}},
		{name: "missing token", config: Config{BaseURL: "http://memory.test", ServiceID: "service-a"}},
		{name: "missing service ID", config: Config{BaseURL: "http://memory.test", Token: "secret"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := NewClient(tt.config); err == nil {
				t.Fatal("NewClient() error = nil, want configuration error")
			}
		})
	}
}

func TestClientPostSendsAuthenticationAndDecodesData(t *testing.T) {
	type responseData struct {
		Accepted []string `json:"accepted_ids"`
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/v3/conversation/add" {
			t.Errorf("path = %s, want /v3/conversation/add", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("Authorization = %q, want Bearer test-token", got)
		}
		if got := r.Header.Get("x-tdai-service-id"); got != "service-a" {
			t.Errorf("x-tdai-service-id = %q, want service-a", got)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", got)
		}

		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if body["probe"] != "ok" {
			t.Errorf("request probe = %q, want ok", body["probe"])
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"message":"ok","request_id":"req-a","data":{"accepted_ids":["msg-a"]}}`))
	}))
	defer server.Close()

	client, err := NewClient(Config{
		BaseURL:   server.URL + "/",
		Token:     "test-token",
		ServiceID: "service-a",
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	var got responseData
	if err := client.post(context.Background(), "/v3/conversation/add", map[string]string{"probe": "ok"}, &got); err != nil {
		t.Fatalf("post() error = %v", err)
	}
	if len(got.Accepted) != 1 || got.Accepted[0] != "msg-a" {
		t.Fatalf("decoded accepted IDs = %#v, want [msg-a]", got.Accepted)
	}
}

func TestClientPostReturnsSanitizedAPIErrors(t *testing.T) {
	const token = "top-secret-token"

	tests := []struct {
		name       string
		status     int
		body       string
		wantStatus int
		wantCode   int
	}{
		{
			name:       "http authentication failure",
			status:     http.StatusUnauthorized,
			body:       `{"code":401,"message":"unauthorized","request_id":"req-a"}`,
			wantStatus: http.StatusUnauthorized,
			wantCode:   401,
		},
		{
			name:       "business envelope failure",
			status:     http.StatusOK,
			body:       `{"code":422,"message":"missing user_id","request_id":"req-b"}`,
			wantStatus: http.StatusOK,
			wantCode:   422,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()

			client, err := NewClient(Config{BaseURL: server.URL, Token: token, ServiceID: "service-a"})
			if err != nil {
				t.Fatalf("NewClient() error = %v", err)
			}

			err = client.post(context.Background(), "/v3/probe", map[string]string{"probe": "ok"}, &struct{}{})
			var apiErr *APIError
			if !errors.As(err, &apiErr) {
				t.Fatalf("post() error = %T %v, want *APIError", err, err)
			}
			if apiErr.HTTPStatus != tt.wantStatus || apiErr.Code != tt.wantCode {
				t.Fatalf("APIError = %#v, want status=%d code=%d", apiErr, tt.wantStatus, tt.wantCode)
			}
			if strings.Contains(err.Error(), token) {
				t.Fatalf("error leaked token: %q", err)
			}
		})
	}
}

func TestClientPostHonorsCanceledContext(t *testing.T) {
	serverCalled := false
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		serverCalled = true
	}))
	defer server.Close()

	client, err := NewClient(Config{BaseURL: server.URL, Token: "test-token", ServiceID: "service-a"})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = client.post(ctx, "/v3/probe", map[string]string{"probe": "ok"}, &struct{}{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("post() error = %v, want context.Canceled", err)
	}
	if serverCalled {
		t.Fatal("server received request after context was canceled")
	}
}
