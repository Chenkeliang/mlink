package tencentdb

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"mlink/internal/model"
)

func TestProviderCaptureTurnWritesExplicitScopeAndMessages(t *testing.T) {
	occurredAt := time.Date(2026, time.August, 28, 13, 30, 0, 123456000, time.FixedZone("CST", 8*60*60))

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v3/conversation/add" {
			t.Errorf("path = %s, want /v3/conversation/add", r.URL.Path)
		}
		var body struct {
			TeamID    string `json:"team_id"`
			AgentID   string `json:"agent_id"`
			UserID    string `json:"user_id"`
			SessionID string `json:"session_id"`
			Messages  []struct {
				Role      string `json:"role"`
				Content   string `json:"content"`
				Timestamp string `json:"timestamp"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode capture body: %v", err)
		}
		if body.TeamID != "team-a" || body.AgentID != "agent-a" || body.UserID != "user-a" || body.SessionID != "session-a" {
			t.Fatalf("capture scope = %#v, want explicit team/agent/user/session", body)
		}
		if len(body.Messages) != 2 {
			t.Fatalf("message count = %d, want 2", len(body.Messages))
		}
		if body.Messages[0].Role != "user" || body.Messages[0].Content != "你好" {
			t.Fatalf("first message = %#v", body.Messages[0])
		}
		if body.Messages[1].Role != "assistant" || body.Messages[1].Content != "你好，林舟" {
			t.Fatalf("second message = %#v", body.Messages[1])
		}
		if body.Messages[0].Timestamp != "2026-08-28T05:30:00.123456Z" {
			t.Fatalf("timestamp = %q, want UTC RFC3339Nano", body.Messages[0].Timestamp)
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"message":"ok","request_id":"req-a","data":{"accepted_ids":["msg-a","msg-b"]}}`))
	}))
	defer server.Close()

	client, err := NewClient(Config{BaseURL: server.URL, Token: "test-token", ServiceID: "service-a"})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	provider := NewProvider(client)
	receipt, err := provider.CaptureTurn(context.Background(), model.Turn{
		Identity: model.IdentityScope{
			TenantID: "team-a", UserID: "user-a", AgentID: "agent-a",
			SessionID: "session-a", TurnID: "turn-a",
		},
		Messages: []model.Message{
			{Role: "user", Content: "你好", OccurredAt: occurredAt},
			{Role: "assistant", Content: "你好，林舟", OccurredAt: occurredAt.Add(time.Second)},
		},
	})
	if err != nil {
		t.Fatalf("CaptureTurn() error = %v", err)
	}
	if len(receipt.AcceptedIDs) != 2 || receipt.AcceptedIDs[0] != "msg-a" || receipt.AcceptedIDs[1] != "msg-b" {
		t.Fatalf("receipt accepted IDs = %#v, want [msg-a msg-b]", receipt.AcceptedIDs)
	}
}

func TestProviderCaptureTurnRejectsInvalidInputBeforeNetwork(t *testing.T) {
	serverCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		serverCalls++
	}))
	defer server.Close()

	client, err := NewClient(Config{BaseURL: server.URL, Token: "test-token", ServiceID: "service-a"})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	provider := NewProvider(client)
	validIdentity := model.IdentityScope{
		TenantID: "team-a", UserID: "user-a", AgentID: "agent-a",
		SessionID: "session-a", TurnID: "turn-a",
	}

	tests := []struct {
		name string
		turn model.Turn
	}{
		{
			name: "missing identity",
			turn: model.Turn{Messages: []model.Message{{Role: "user", Content: "hello"}}},
		},
		{
			name: "empty messages",
			turn: model.Turn{Identity: validIdentity},
		},
		{
			name: "unsupported system role",
			turn: model.Turn{Identity: validIdentity, Messages: []model.Message{{Role: "system", Content: "hidden"}}},
		},
		{
			name: "blank content",
			turn: model.Turn{Identity: validIdentity, Messages: []model.Message{{Role: "user", Content: "  "}}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := provider.CaptureTurn(context.Background(), tt.turn); err == nil {
				t.Fatal("CaptureTurn() error = nil, want validation error")
			}
		})
	}
	if serverCalls != 0 {
		t.Fatalf("server calls = %d, want 0", serverCalls)
	}
}

func TestProviderRecallDefaultsToUserScopedL1(t *testing.T) {
	var mu sync.Mutex
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()

		if r.URL.Path != "/v3/atomic/search" {
			t.Errorf("unexpected default recall path %s", r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode recall body: %v", err)
		}
		if body["team_id"] != "team-a" || body["agent_id"] != "agent-a" || body["user_id"] != "user-a" {
			t.Fatalf("recall scope = %#v, want explicit team/agent/user", body)
		}
		if body["query"] != "MLink" || body["limit"] != float64(5) {
			t.Fatalf("recall query = %#v, want query=MLink limit=5", body)
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"message":"ok","request_id":"req-a","data":{"items":[{"id":"mem-a","type":"instruction","content":"先给结论","score":0.9},{"id":"mem-a","type":"instruction","content":"先给结论","score":0.9}]}}`))
	}))
	defer server.Close()

	provider := newTestProvider(t, server.URL)
	bundle, err := provider.Recall(context.Background(), model.RecallRequest{
		Identity: model.IdentityScope{TenantID: "team-a", UserID: "user-a", AgentID: "agent-a"},
		Query:    "MLink",
	})
	if err != nil {
		t.Fatalf("Recall() error = %v", err)
	}
	if bundle.Partial || len(bundle.Warnings) != 0 {
		t.Fatalf("bundle partial/warnings = %v/%v, want complete", bundle.Partial, bundle.Warnings)
	}
	if len(bundle.Items) != 1 {
		t.Fatalf("item count = %d, want duplicate stable ID collapsed to 1", len(bundle.Items))
	}
	item := bundle.Items[0]
	if item.ID != "l1:mem-a" || item.Scope != model.ScopeUser || item.Kind != "instruction" || item.Text != "先给结论" {
		t.Fatalf("L1 item = %#v", item)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(paths) != 1 || paths[0] != "/v3/atomic/search" {
		t.Fatalf("default recall paths = %#v, want only atomic search", paths)
	}
}

func TestProviderRecallIncludesAgentSharedLayersOnlyWhenRequested(t *testing.T) {
	var mu sync.Mutex
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.URL.Path)
		mu.Unlock()

		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode %s body: %v", r.URL.Path, err)
		}
		if body["team_id"] != "team-a" || body["agent_id"] != "agent-a" || body["user_id"] != "user-a" {
			t.Errorf("%s scope = %#v, want explicit team/agent/user", r.URL.Path, body)
		}

		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v3/atomic/search":
			_, _ = w.Write([]byte(`{"code":0,"message":"ok","request_id":"req-l1","data":{"items":[{"id":"mem-a","type":"persona","content":"常用中文","score":0.8}]}}`))
		case "/v3/scenario/ls":
			_, _ = w.Write([]byte(`{"code":0,"message":"ok","request_id":"req-l2-list","data":{"entries":[{"path":"scene-a.md","version":"v1","created_at":"2026-08-28T00:00:00Z","updated_at":"2026-08-28T00:00:00Z"}],"total":1}}`))
		case "/v3/scenario/read":
			if body["path"] != "scene-a.md" {
				t.Errorf("scenario path = %v, want scene-a.md", body["path"])
			}
			_, _ = w.Write([]byte(`{"code":0,"message":"ok","request_id":"req-l2-read","data":{"path":"scene-a.md","version":"v1","content":"MLink 场景","created_at":"2026-08-28T00:00:00Z","updated_at":"2026-08-28T00:00:00Z"}}`))
		case "/v3/core/read":
			_, _ = w.Write([]byte(`{"code":0,"message":"ok","request_id":"req-l3","data":{"version":"v1","content":"Agent 共享画像","created_at":"2026-08-28T00:00:00Z","updated_at":"2026-08-28T00:00:00Z"}}`))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	provider := newTestProvider(t, server.URL)
	bundle, err := provider.Recall(context.Background(), model.RecallRequest{
		Identity:           model.IdentityScope{TenantID: "team-a", UserID: "user-a", AgentID: "agent-a"},
		Query:              "MLink",
		MaxItems:           5,
		IncludeAgentShared: true,
	})
	if err != nil {
		t.Fatalf("Recall() error = %v", err)
	}
	if bundle.Partial {
		t.Fatalf("bundle.Partial = true, warnings = %v", bundle.Warnings)
	}
	if len(bundle.Items) != 3 {
		t.Fatalf("item count = %d, want L1+L2+L3", len(bundle.Items))
	}
	items := make(map[string]model.ContextItem, len(bundle.Items))
	for _, item := range bundle.Items {
		items[item.ID] = item
	}
	if items["l1:mem-a"].Scope != model.ScopeUser {
		t.Fatalf("L1 scope = %q, want user", items["l1:mem-a"].Scope)
	}
	if items["l2:scene-a.md"].Scope != model.ScopeAgent || items["l2:scene-a.md"].Text != "MLink 场景" {
		t.Fatalf("L2 item = %#v", items["l2:scene-a.md"])
	}
	if items["l3:persona"].Scope != model.ScopeAgent || items["l3:persona"].Text != "Agent 共享画像" {
		t.Fatalf("L3 item = %#v", items["l3:persona"])
	}

	mu.Lock()
	sort.Strings(paths)
	gotPaths := strings.Join(paths, ",")
	mu.Unlock()
	wantPaths := "/v3/atomic/search,/v3/core/read,/v3/scenario/ls,/v3/scenario/read"
	if gotPaths != wantPaths {
		t.Fatalf("shared recall paths = %q, want %q", gotPaths, wantPaths)
	}
}

func TestProviderRecallReturnsPartialBundleWhenSharedLayerFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v3/atomic/search":
			_, _ = w.Write([]byte(`{"code":0,"message":"ok","request_id":"req-l1","data":{"items":[{"id":"mem-a","type":"instruction","content":"先给结论"}]}}`))
		case "/v3/scenario/ls":
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"code":503,"message":"storage unavailable","request_id":"req-l2"}`))
		case "/v3/core/read":
			_, _ = w.Write([]byte(`{"code":0,"message":"ok","request_id":"req-l3","data":{"version":"v1","content":"共享画像","created_at":"2026-08-28T00:00:00Z","updated_at":"2026-08-28T00:00:00Z"}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	provider := newTestProvider(t, server.URL)
	bundle, err := provider.Recall(context.Background(), model.RecallRequest{
		Identity:           model.IdentityScope{TenantID: "team-a", UserID: "user-a", AgentID: "agent-a"},
		Query:              "偏好",
		IncludeAgentShared: true,
	})
	if err != nil {
		t.Fatalf("Recall() error = %v, want partial success", err)
	}
	if !bundle.Partial || len(bundle.Warnings) != 1 {
		t.Fatalf("partial/warnings = %v/%v, want true and one warning", bundle.Partial, bundle.Warnings)
	}
	if len(bundle.Items) != 2 {
		t.Fatalf("item count = %d, want valid L1 and L3", len(bundle.Items))
	}
}

func TestProviderRecallRejectsInvalidInputBeforeNetwork(t *testing.T) {
	serverCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		serverCalls++
	}))
	defer server.Close()
	provider := newTestProvider(t, server.URL)

	tests := []model.RecallRequest{
		{Query: "MLink"},
		{Identity: model.IdentityScope{TenantID: "team-a", UserID: "user-a", AgentID: "agent-a"}, Query: "  "},
	}
	for _, request := range tests {
		if _, err := provider.Recall(context.Background(), request); err == nil {
			t.Fatal("Recall() error = nil, want validation error")
		}
	}
	if serverCalls != 0 {
		t.Fatalf("server calls = %d, want 0", serverCalls)
	}
}

func newTestProvider(t *testing.T, baseURL string) *Provider {
	t.Helper()
	client, err := NewClient(Config{BaseURL: baseURL, Token: "test-token", ServiceID: "service-a"})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	return NewProvider(client)
}
