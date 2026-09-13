package host

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"mlink/internal/connection"
	"mlink/internal/model"
	"mlink/internal/provider/manifest"
)

func TestBundledTencentDBProviderProcess(t *testing.T) {
	const token = "bundled-memory-token"
	var mu sync.Mutex
	var paths []string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		paths = append(paths, r.Method+" "+r.URL.Path)
		mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer "+token || r.Header.Get("x-tdai-service-id") != "service-bundled" {
			t.Errorf("backend request has incorrect authentication headers")
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/health":
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case "/v3/conversation/add":
			var request struct {
				TeamID, AgentID, UserID, SessionID string
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode capture request: %v", err)
			}
			_, _ = w.Write([]byte(`{"code":0,"data":{"accepted_ids":["bundled-ref"]}}`))
		case "/v3/skill/conversation/add":
			_, _ = w.Write([]byte(`{"code":0,"data":{"status":"archived"}}`))
		case "/v3/skill/search":
			_, _ = w.Write([]byte(`{"code":0,"data":{"items":[]}}`))
		case "/v3/atomic/search":
			_, _ = w.Write([]byte(`{"code":0,"data":{"items":[{"id":"bundled-memory","type":"instruction","content":"MLink bundled process"}]}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer backend.Close()

	executable := buildMLinkBinary(t)
	manifestPath := filepath.Join(moduleRoot(t), "internal/provider/host/testdata/tencentdb-provider.yaml")
	providerManifest, err := manifest.Load(manifestPath)
	if err != nil {
		t.Fatalf("manifest.Load() error = %v", err)
	}
	snapshot, err := connection.NewSnapshot(
		"connection-bundled",
		connection.ProviderRef{ID: "dev.mlink.tencentdb", Version: "0.1.0"},
		"revision-bundled",
		json.RawMessage(`{"base_url":`+quoteForTest(backend.URL)+`,"service_id":"service-bundled","timeout_ms":1500}`),
		map[string]string{"token": "keychain://bundled/token"},
	)
	if err != nil {
		t.Fatal(err)
	}
	session, err := startBundledSession(context.Background(), Config{
		Snapshot: snapshot, Manifest: providerManifest,
		PackageDirectory: filepath.Dir(manifestPath), RuntimeDirectory: filepath.Join(t.TempDir(), "runtime"),
		Secrets: map[string]string{"token": token},
		ParentEnvironment: append(os.Environ(),
			"OPENAI_API_KEY=agent-key-must-not-cross", "ANTHROPIC_BASE_URL=https://agent-model.invalid",
		),
	}, executable)
	if err != nil {
		t.Fatalf("startBundledSession() error = %v", err)
	}

	health, err := session.Health(context.Background())
	if err != nil || health.Backend != "available" {
		t.Fatalf("Health() = %#v, %v", health, err)
	}
	identity := model.IdentityScope{
		ConnectionID: "connection-bundled", TenantID: "team-a", UserID: "user-a", AgentID: "agent-a",
		SessionID: "session-a", TurnID: "turn-a",
	}
	receipt, err := session.CaptureTurn(context.Background(), CallMeta{IdempotencyKey: "bundled-turn"}, model.Turn{
		Identity: identity,
		Messages: []model.Message{{Role: "user", Content: "remember"}, {Role: "assistant", Content: "remembered"}},
	})
	if err != nil || receipt.ReceiptID == "" || receipt.ProviderRefs[0] != "bundled-ref" || receipt.ReplaySafe {
		t.Fatalf("CaptureTurn() = %#v, %v", receipt, err)
	}
	identity.SessionID = ""
	identity.TurnID = ""
	bundle, err := session.Recall(context.Background(), CallMeta{}, model.RecallRequest{
		Identity: identity, Query: "MLink", MaxItems: 5,
	})
	if err != nil || len(bundle.Items) != 1 || bundle.Items[0].ID != "l1:bundled-memory" {
		t.Fatalf("Recall() = %#v, %v", bundle, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := session.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	mu.Lock()
	gotPaths := append([]string(nil), paths...)
	mu.Unlock()
	wantPaths := []string{"GET /health", "POST /v3/conversation/add", "POST /v3/skill/conversation/add", "POST /v3/skill/search", "POST /v3/atomic/search"}
	if len(gotPaths) != len(wantPaths) {
		t.Fatalf("backend paths = %#v, want %#v", gotPaths, wantPaths)
	}
	for index := range wantPaths {
		if gotPaths[index] != wantPaths[index] {
			t.Fatalf("backend path[%d] = %q, want %q", index, gotPaths[index], wantPaths[index])
		}
	}
}

func buildMLinkBinary(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "mlink")
	command := exec.Command(filepath.Join(runtime.GOROOT(), "bin", "go"), "build", "-o", path, "./cmd/mlink")
	command.Dir = moduleRoot(t)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("build mlink: %v: %s", err, output)
	}
	return path
}

func quoteForTest(value string) string {
	raw, _ := json.Marshal(value)
	return string(raw)
}
