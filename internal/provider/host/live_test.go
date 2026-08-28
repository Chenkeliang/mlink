//go:build integration

package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mlink/internal/connection"
	"mlink/internal/model"
	"mlink/internal/provider/manifest"
)

func TestLiveProviderHostRoundTripIsolation(t *testing.T) {
	baseURL := strings.TrimSpace(os.Getenv("MLINK_TEST_MEMORYCORE_URL"))
	token := strings.TrimSpace(os.Getenv("MLINK_TEST_MEMORYCORE_TOKEN"))
	if baseURL == "" || token == "" {
		t.Skip("MLINK_TEST_MEMORYCORE_URL and MLINK_TEST_MEMORYCORE_TOKEN are required")
	}
	runID := fmt.Sprintf("host-live-%d", time.Now().UnixNano())
	serviceID := "mlink-" + runID
	manifestPath := filepath.Join(moduleRoot(t), "internal/provider/host/testdata/tencentdb-provider.yaml")
	providerManifest, err := manifest.Load(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := connection.NewSnapshot(
		"connection-"+runID,
		connection.ProviderRef{ID: "dev.mlink.tencentdb", Version: "0.1.0"},
		"revision-"+runID,
		json.RawMessage(`{"base_url":`+quoteForTest(baseURL)+`,"service_id":`+quoteForTest(serviceID)+`,"timeout_ms":5000}`),
		map[string]string{"token": "keychain://live/token"},
	)
	if err != nil {
		t.Fatal(err)
	}
	executable := buildMLinkBinary(t)
	session, err := startBundledSession(context.Background(), Config{
		Snapshot: snapshot, Manifest: providerManifest,
		PackageDirectory: filepath.Dir(manifestPath), RuntimeDirectory: filepath.Join(t.TempDir(), "runtime"),
		Secrets: map[string]string{"token": token}, ParentEnvironment: os.Environ(),
	}, executable)
	if err != nil {
		t.Fatalf("start live Provider Host: %v", err)
	}
	t.Cleanup(func() {
		if session.State() == StateReady {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_ = session.Shutdown(ctx)
		}
	})

	health, err := session.Health(context.Background())
	if err != nil || health.Backend != "available" {
		t.Fatalf("live Health() = %#v, %v", health, err)
	}
	canary := fmt.Sprintf("MLINK_HOST_LIVE_%d", time.Now().UnixNano())
	identity := model.IdentityScope{
		ConnectionID: snapshot.RouteKey().ConnectionID,
		TenantID:     "team-" + runID, UserID: "user-a-" + runID, AgentID: "agent-" + runID,
		SessionID: "session-" + runID, TurnID: "turn-" + runID,
	}
	messages := make([]model.Message, 0, 10)
	for range 5 {
		messages = append(messages,
			model.Message{Role: "user", Content: "我的长期个人代号是 " + canary + "，以后询问个人代号时请使用它。"},
			model.Message{Role: "assistant", Content: "已记录你的长期个人代号 " + canary + "。"},
		)
	}
	receipt, err := session.CaptureTurn(context.Background(), CallMeta{IdempotencyKey: identity.TurnID}, model.Turn{
		Identity: identity, Messages: messages,
	})
	if err != nil || receipt.State != model.WriteAccepted || receipt.ReplaySafe {
		t.Fatalf("live CaptureTurn() = %#v, %v", receipt, err)
	}

	recallIdentity := identity
	recallIdentity.SessionID = ""
	recallIdentity.TurnID = ""
	bundle := eventuallyRecallHost(t, session, model.RecallRequest{
		Identity: recallIdentity, Query: canary, MaxItems: 20,
	}, canary, 2*time.Minute)
	if !bundleContainsHost(bundle, canary) {
		t.Fatalf("live recall did not contain canary: %#v", bundle)
	}
	otherUser := recallIdentity
	otherUser.UserID = "user-b-" + runID
	otherBundle, err := session.Recall(context.Background(), CallMeta{}, model.RecallRequest{
		Identity: otherUser, Query: canary, MaxItems: 20,
	})
	if err != nil {
		t.Fatalf("live Recall(other user) error = %v", err)
	}
	if bundleContainsHost(otherBundle, canary) {
		t.Fatalf("other user received private canary: %#v", otherBundle)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := session.Shutdown(ctx); err != nil {
		t.Fatalf("live Shutdown() error = %v", err)
	}
	if event, ok := session.ExitEvent(); !ok || !event.Expected || event.ExitCode != 0 {
		t.Fatalf("live ExitEvent() = %#v/%v", event, ok)
	}
}

func eventuallyRecallHost(
	t *testing.T,
	session Session,
	request model.RecallRequest,
	canary string,
	timeout time.Duration,
) model.ContextBundle {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	var last model.ContextBundle
	var lastErr error
	for {
		last, lastErr = session.Recall(ctx, CallMeta{}, request)
		if lastErr == nil && bundleContainsHost(last, canary) {
			return last
		}
		select {
		case <-ctx.Done():
			if lastErr != nil && !errors.Is(lastErr, context.DeadlineExceeded) {
				t.Fatalf("live recall failed: %v", lastErr)
			}
			t.Fatalf("live memory did not become visible within %s", timeout)
		case <-ticker.C:
		}
	}
}

func bundleContainsHost(bundle model.ContextBundle, canary string) bool {
	for _, item := range bundle.Items {
		if item.Scope == model.ScopeUser && strings.Contains(item.Text, canary) {
			return true
		}
	}
	return false
}
