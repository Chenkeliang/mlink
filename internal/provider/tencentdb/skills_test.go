package tencentdb

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"mlink/internal/connection"
	"mlink/internal/model"
	"mlink/internal/provider/protocol"
)

func TestSkillLifecycleCapturesTurnAndArchivesAfterIdle(t *testing.T) {
	archived := make(chan map[string]any, 1)
	var mu sync.Mutex
	var captured map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v3/conversation/add":
			_, _ = w.Write([]byte(`{"code":0,"data":{"accepted_ids":["message-a"]}}`))
		case "/v3/skill/conversation/add":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			mu.Lock()
			captured = body
			mu.Unlock()
			_, _ = w.Write([]byte(`{"code":0,"data":{"status":"ok"}}`))
		case "/v3/skill/conversation/force-archive":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			archived <- body
			_, _ = w.Write([]byte(`{"code":0,"data":{"status":"archived","task_id":"task-a"}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	provider := newTestProvider(t, server.URL)
	provider.enableSkills(10 * time.Millisecond)
	receipt, err := provider.CaptureTurn(context.Background(), model.Turn{
		Identity: model.IdentityScope{
			TenantID: "team-a", UserID: "user-a", AgentID: "agent-a",
			SessionID: "session-a", TurnID: "turn-a",
		},
		Messages: []model.Message{
			{Role: "user", Content: "记住这个流程"},
			{Role: "assistant", Content: "先核验，再执行"},
		},
	})
	if err != nil || len(receipt.Warnings) != 0 {
		t.Fatalf("CaptureTurn() receipt/error = %#v/%v", receipt, err)
	}
	mu.Lock()
	if captured["team_id"] != "team-a" || captured["user_id"] != "user-a" || captured["agent_id"] != "agent-a" || captured["session_id"] != "session-a" {
		t.Fatalf("skill capture scope = %#v", captured)
	}
	if messages, _ := captured["messages"].([]any); len(messages) != 2 {
		t.Fatalf("skill capture messages = %#v", captured["messages"])
	}
	mu.Unlock()

	select {
	case body := <-archived:
		if body["session_id"] != "session-a" || body["agent_id"] != "agent-a" {
			t.Fatalf("archive scope = %#v", body)
		}
	case <-time.After(time.Second):
		t.Fatal("idle archive was not requested")
	}
	if err := provider.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestSkillCaptureFailsOpenAfterConversationWasAccepted(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v3/conversation/add" {
			_, _ = w.Write([]byte(`{"code":0,"data":{"accepted_ids":["message-a"]}}`))
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"code":503,"message":"unavailable"}`))
	}))
	defer server.Close()

	provider := newTestProvider(t, server.URL)
	provider.enableSkills(time.Minute)
	receipt, err := provider.CaptureTurn(context.Background(), model.Turn{
		Identity: model.IdentityScope{TenantID: "team-a", UserID: "user-a", AgentID: "agent-a", SessionID: "session-a", TurnID: "turn-a"},
		Messages: []model.Message{{Role: "user", Content: "hello"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(receipt.ProviderRefs) != 1 || len(receipt.Warnings) != 1 || receipt.Warnings[0] != "MemoryCore Skill capture unavailable" {
		t.Fatalf("receipt = %#v", receipt)
	}
}

func TestSkillLifecycleShutdownArchivesPendingSession(t *testing.T) {
	archived := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v3/conversation/add":
			_, _ = w.Write([]byte(`{"code":0,"data":{"accepted_ids":["message-a"]}}`))
		case "/v3/skill/conversation/add":
			_, _ = w.Write([]byte(`{"code":0,"data":{"status":"ok"}}`))
		case "/v3/skill/conversation/force-archive":
			archived <- struct{}{}
			_, _ = w.Write([]byte(`{"code":0,"data":{"status":"archived"}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	provider := newTestProvider(t, server.URL)
	provider.enableSkills(time.Hour)
	_, err := provider.CaptureTurn(context.Background(), model.Turn{
		Identity: model.IdentityScope{TenantID: "team-a", UserID: "user-a", AgentID: "agent-a", SessionID: "session-a", TurnID: "turn-a"},
		Messages: []model.Message{{Role: "user", Content: "hello"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := provider.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-archived:
	default:
		t.Fatal("shutdown did not archive pending Skill conversation")
	}
}

func TestSkillLifecycleIgnoresSupersededIdleTimer(t *testing.T) {
	var archiveCalls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v3/skill/conversation/force-archive" {
			http.NotFound(w, r)
			return
		}
		archiveCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"data":{"status":"archived"}}`))
	}))
	defer server.Close()

	provider := newTestProvider(t, server.URL)
	provider.enableSkills(time.Hour)
	identity := model.IdentityScope{TenantID: "team-a", UserID: "user-a", AgentID: "agent-a", SessionID: "session-a"}
	provider.skills.schedule(identity)
	key := skillSessionKey(identity)
	provider.skills.mu.Lock()
	oldVersion := provider.skills.versions[key]
	provider.skills.mu.Unlock()
	provider.skills.schedule(identity)
	provider.skills.mu.Lock()
	currentVersion := provider.skills.versions[key]
	provider.skills.mu.Unlock()

	provider.skills.archiveOnTimer(key, identity, oldVersion)
	if archiveCalls.Load() != 0 {
		t.Fatalf("superseded timer archived session %d time(s)", archiveCalls.Load())
	}
	provider.skills.archiveOnTimer(key, identity, currentVersion)
	if archiveCalls.Load() != 1 {
		t.Fatalf("current timer archive calls = %d, want 1", archiveCalls.Load())
	}
	if err := provider.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestSkillRecallReturnsFullAgentOwnedContentBeforeL1(t *testing.T) {
	var mu sync.Mutex
	var searchBody, getBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v3/skill/search":
			if err := json.NewDecoder(r.Body).Decode(&searchBody); err != nil {
				t.Fatal(err)
			}
			_, _ = w.Write([]byte(`{"code":0,"data":{"items":[{"skill_id":"skill-a","name":"workflow","version":2,"created_at_ms":1789200000000,"updated_at_ms":1789280000000,"score":0.95}]}}`))
		case "/v3/skill/get":
			if err := json.NewDecoder(r.Body).Decode(&getBody); err != nil {
				t.Fatal(err)
			}
			_, _ = w.Write([]byte(`{"code":0,"data":{"skill_id":"skill-a","version":2,"content":"---\nname: workflow\ndescription: confirmed\n---\nUse /ops/exec after confirmation."}}`))
		case "/v3/atomic/search":
			_, _ = w.Write([]byte(`{"code":0,"data":{"items":[{"id":"memory-a","type":"instruction","content":"ordinary memory"}]}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	provider := newTestProvider(t, server.URL)
	provider.enableSkills(time.Minute)
	bundle, err := provider.Recall(context.Background(), model.RecallRequest{
		Identity: model.IdentityScope{TenantID: "team-a", UserID: "user-a", AgentID: "agent-a", SessionID: "session-current"},
		Query:    "confirmed workflow", MaxItems: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if bundle.Partial || len(bundle.Items) != 2 || bundle.Items[0].ID != "skill:skill-a@v2" || bundle.Items[0].Kind != "skill" || bundle.Items[0].Scope != model.ScopeAgent || bundle.Items[1].ID != "l1:memory-a" {
		t.Fatalf("bundle = %#v", bundle)
	}
	if bundle.Items[0].Text != "---\nname: workflow\ndescription: confirmed\n---\nUse /ops/exec after confirmation." {
		t.Fatalf("skill content = %q", bundle.Items[0].Text)
	}
	mu.Lock()
	defer mu.Unlock()
	if searchBody["team_id"] != "team-a" || searchBody["user_id"] != "user-a" || searchBody["agent_id"] != "agent-a" {
		t.Fatalf("search scope = %#v", searchBody)
	}
	if _, exists := searchBody["session_id"]; exists {
		t.Fatalf("skill search was narrowed to current session: %#v", searchBody)
	}
	if getBody["skill_id"] != "skill-a" || getBody["version"] != float64(2) || getBody["include_content"] != true {
		t.Fatalf("skill get body = %#v", getBody)
	}
}

func TestSkillRecallFailsOpenToOrdinaryMemory(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v3/skill/search":
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"code":404,"message":"unsupported"}`))
		case "/v3/atomic/search":
			_, _ = w.Write([]byte(`{"code":0,"data":{"items":[{"id":"memory-a","type":"instruction","content":"ordinary memory"}]}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	provider := newTestProvider(t, server.URL)
	provider.enableSkills(time.Minute)
	bundle, err := provider.Recall(context.Background(), model.RecallRequest{
		Identity: model.IdentityScope{TenantID: "team-a", UserID: "user-a", AgentID: "agent-a"},
		Query:    "ordinary", MaxItems: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !bundle.Partial || len(bundle.Warnings) != 1 || bundle.Warnings[0] != "Skill recall unavailable" || len(bundle.Items) != 1 || bundle.Items[0].ID != "l1:memory-a" {
		t.Fatalf("bundle = %#v", bundle)
	}
}

func TestServerHandlerEnablesSkillLifecycleByDefault(t *testing.T) {
	var skillCaptures int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v3/conversation/add":
			_, _ = w.Write([]byte(`{"code":0,"data":{"accepted_ids":["message-a"]}}`))
		case "/v3/skill/conversation/add":
			skillCaptures++
			_, _ = w.Write([]byte(`{"code":0,"data":{"status":"archived"}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	handler := NewServerHandler()
	_, err := handler.Initialize(context.Background(), protocol.InitializeParams{
		Route:   connection.RouteKey{ProviderID: providerID, ProviderVersion: providerVersion},
		Config:  json.RawMessage(`{"base_url":` + quoteJSON(server.URL) + `,"service_id":"service-a","timeout_ms":1500}`),
		Secrets: map[string]string{"token": "memory-token"},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = handler.CaptureTurn(context.Background(), protocol.CaptureParams{Turn: model.Turn{
		Identity: model.IdentityScope{TenantID: "team-a", UserID: "user-a", AgentID: "agent-a", SessionID: "session-a", TurnID: "turn-a"},
		Messages: []model.Message{{Role: "user", Content: "hello"}},
	}})
	if err != nil || skillCaptures != 1 {
		t.Fatalf("CaptureTurn()/skill captures = %v/%d", err, skillCaptures)
	}
	if err := handler.Shutdown(context.Background(), protocol.ShutdownParams{}); err != nil {
		t.Fatal(err)
	}
}
