package tencentdb

import (
	"context"
	"encoding/json"
	"mlink/internal/model"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestActiveTurnInvalidatesIdleAndOlderCompletion(t *testing.T) {
	var archives atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/v3/skill/conversation/force-archive" {
			archives.Add(1)
		}
		_, _ = w.Write([]byte(`{"code":0,"data":{"status":"ok"}}`))
	}))
	defer srv.Close()
	p := newTestProvider(t, srv.URL)
	p.enableSkills(0)
	s := p.skills
	if s.idleAfter != time.Hour {
		t.Fatalf("idle duration %v", s.idleAfter)
	}
	i := model.IdentityScope{TenantID: "team", AgentID: "agent", UserID: "user", SessionID: "session", TurnID: "old"}
	s.schedule(i)
	key := skillSessionKey(i)
	old := s.versions[key]
	current := i
	current.TurnID = "current"
	if _, err := p.ObserveUserTurn(context.Background(), model.UserTurn{Identity: current, Message: model.Message{Role: "user", Content: "new request"}}); err != nil {
		t.Fatal(err)
	}
	// The old callback may already be queued on the scheduler when stopped.
	s.archiveOnTimer(key, i, old)
	if err := s.captureTurn(context.Background(), i, nil); err != nil {
		t.Fatal(err)
	}
	if archives.Load() != 0 || s.timers[key] != nil {
		t.Fatal("older completion archived or rearmed an active turn")
	}
	if err := s.captureTurn(context.Background(), current, nil); err != nil {
		t.Fatal(err)
	}
	if s.timers[key] == nil || s.versions[key] <= old {
		t.Fatal("completion did not rearm fresh one-hour timer")
	}
	s.archiveOnTimer(key, current, old)
	if archives.Load() != 0 {
		t.Fatal("stale callback archived new buffer")
	}
	// Simulate delivery of the new timer callback after its full idle duration.
	s.archiveOnTimer(key, current, s.versions[key])
	if archives.Load() != 1 {
		t.Fatalf("want one idle archive, got %d", archives.Load())
	}
	_ = p.Shutdown(context.Background())
}

func TestExplicitCorrectionSubmitsBeforeTurnCompletionWithProvenance(t *testing.T) {
	var extracts atomic.Int32
	var submitted map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v3/skill/search":
			_, _ = w.Write([]byte(`{"code":0,"data":{"items":[{"skill_id":"skl-old","version":2}]}}`))
		case "/v3/skill/get":
			_, _ = w.Write([]byte(`{"code":0,"data":{"content":"Historical rule: never use /ops/exec"}}`))
		case "/v3/skill/conversation/add":
			_ = json.NewDecoder(r.Body).Decode(&submitted)
			_, _ = w.Write([]byte(`{"code":0,"data":{"status":"buffered"}}`))
		case "/v3/skill/conversation/force-archive":
			extracts.Add(1)
			_, _ = w.Write([]byte(`{"code":0,"data":{"task_id":"priority-task"}}`))
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	p := newTestProvider(t, srv.URL)
	p.enableSkills(time.Hour)
	turn := model.UserTurn{Identity: model.IdentityScope{TenantID: "team", UserID: "user", AgentID: "agent", SessionID: "session", TurnID: "correction-1"}, Message: model.Message{Role: "user", Content: "纠正：已建单据应该用 /ops/exec，先查询和 dry-run，确认后用系统加密函数修改。"}, Previous: []model.Message{{Role: "user", Content: "如何改地址"}, {Role: "assistant", Content: "不要用 /ops/exec"}}, Correction: true}
	receipt, err := p.ObserveUserTurn(context.Background(), turn)
	if err != nil || !receipt.Paused || receipt.CorrectionStatus != "queued" || receipt.TaskID != "priority-task" {
		t.Fatalf("receipt %+v error %v", receipt, err)
	}
	if len(receipt.RelatedSkillRefs) != 1 || receipt.RelatedSkillRefs[0] != "skill:skl-old@v2" {
		t.Fatalf("missing old version link: %+v", receipt)
	}
	raw, _ := json.Marshal(submitted)
	for _, part := range []string{"correction-1", "skl-old@v2", "不要用 /ops/exec", "纠正：已建单据"} {
		if !strings.Contains(string(raw), part) {
			t.Errorf("missing context %q", part)
		}
	}
	if _, err := p.ObserveUserTurn(context.Background(), turn); err != nil {
		t.Fatal(err)
	}
	if extracts.Load() != 1 {
		t.Fatal("duplicate observation submitted correction twice")
	}
	// Shutdown during active model work must not archive the stale conversation.
	if err := p.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}
