package host

import (
	"context"
	"encoding/json"
	"mlink/internal/connection"
	"mlink/internal/model"
	"mlink/internal/provider/manifest"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func TestBundledProcessObservesCorrectionWithoutCompletedTurn(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/health":
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case "/v3/skill/search":
			_, _ = w.Write([]byte(`{"code":0,"data":{"items":[{"skill_id":"old-rule","version":2}]}}`))
		case "/v3/skill/get":
			_, _ = w.Write([]byte(`{"code":0,"data":{"content":"old rule"}}`))
		case "/v3/skill/conversation/add":
			_, _ = w.Write([]byte(`{"code":0,"data":{"status":"buffered"}}`))
		case "/v3/skill/conversation/force-archive":
			_, _ = w.Write([]byte(`{"code":0,"data":{"task_id":"immediate-correction"}}`))
		default:
			t.Errorf("unexpected request before turn completion: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer backend.Close()
	manifestPath := filepath.Join(moduleRoot(t), "internal/provider/host/testdata/tencentdb-provider.yaml")
	m, err := manifest.Load(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	config, _ := json.Marshal(map[string]any{"base_url": backend.URL, "service_id": "test", "timeout_ms": 1500})
	snapshot, err := connection.NewSnapshot("connection", connection.ProviderRef{ID: "dev.mlink.tencentdb", Version: "0.1.0"}, "revision", config, map[string]string{"token": "keychain://test"})
	if err != nil {
		t.Fatal(err)
	}
	s, err := startBundledSession(context.Background(), Config{Snapshot: snapshot, Manifest: m, PackageDirectory: filepath.Dir(manifestPath), RuntimeDirectory: filepath.Join(t.TempDir(), "runtime"), Secrets: map[string]string{"token": "test"}}, buildMLinkBinary(t))
	if err != nil {
		t.Fatal(err)
	}
	turn := model.UserTurn{Identity: model.IdentityScope{ConnectionID: "connection", TenantID: "team", AgentID: "agent", UserID: "user", SessionID: "session", TurnID: "turn"}, Message: model.Message{Role: "user", Content: "纠正：应使用新规则"}, Correction: true}
	receipt, err := s.ObserveUserTurn(context.Background(), CallMeta{IdempotencyKey: "observation"}, turn)
	if err != nil || !receipt.Paused || receipt.TaskID != "immediate-correction" || len(receipt.RelatedSkillRefs) != 1 {
		t.Fatalf("receipt=%+v err=%v", receipt, err)
	}
	if err := s.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}
