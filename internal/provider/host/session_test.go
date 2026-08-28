package host

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"mlink/internal/connection"
	"mlink/internal/model"
	"mlink/internal/provider/manifest"
	"mlink/internal/provider/protocol"
)

func TestSessionRunsProviderLifecycle(t *testing.T) {
	session := startFixtureSession(t, "normal", "token-session-normal")
	if session.State() != StateReady {
		t.Fatalf("State() = %q, want ready", session.State())
	}
	if session.RouteKey().ConnectionID != "connection-test" {
		t.Fatalf("RouteKey() = %#v", session.RouteKey())
	}
	capabilities := session.Capabilities()
	delete(capabilities, "health")
	if _, exists := session.Capabilities()["health"]; !exists {
		t.Fatal("Capabilities() returned an aliased map")
	}

	health, err := session.Health(context.Background())
	if err != nil {
		t.Fatalf("Health() error = %v", err)
	}
	if health.Process != "ready" || health.Config != "valid" || health.Backend != "available" {
		t.Fatalf("Health() = %#v", health)
	}

	turn := model.Turn{
		Identity: testIdentity(true),
		Messages: []model.Message{
			{Role: "user", Content: "请记住项目代号"},
			{Role: "assistant", Content: "已记住"},
		},
	}
	receipt, err := session.CaptureTurn(context.Background(), CallMeta{IdempotencyKey: "turn-a"}, turn)
	if err != nil {
		t.Fatalf("CaptureTurn() error = %v", err)
	}
	if receipt.ReceiptID == "" || receipt.State != model.WriteAccepted || receipt.ReplaySafe {
		t.Fatalf("CaptureTurn() receipt = %#v", receipt)
	}
	if len(receipt.ProviderRefs) != 1 || receipt.ProviderRefs[0] != "provider-ref-a" {
		t.Fatalf("ProviderRefs = %#v", receipt.ProviderRefs)
	}

	bundle, err := session.Recall(context.Background(), CallMeta{}, model.RecallRequest{
		Identity: testIdentity(false), Query: "项目偏好", MaxItems: 5,
	})
	if err != nil {
		t.Fatalf("Recall() error = %v", err)
	}
	if len(bundle.Items) != 1 || bundle.Items[0].ID != "memory-a" {
		t.Fatalf("Recall() = %#v", bundle)
	}

	doneA, doneB := session.Done(), session.Done()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := session.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	for index, done := range []<-chan struct{}{doneA, doneB} {
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatalf("Done observer %d was not notified", index)
		}
	}
	eventA, ok := session.ExitEvent()
	if !ok || !eventA.Expected || eventA.ExitCode != 0 {
		t.Fatalf("ExitEvent() = %#v/%v", eventA, ok)
	}
	eventB, _ := session.ExitEvent()
	if eventA != eventB {
		t.Fatalf("ExitEvent changed: %#v != %#v", eventA, eventB)
	}
}

func TestSessionRejectsProviderIdentityMismatch(t *testing.T) {
	helper := buildFixtureProvider(t)
	_, err := startSession(context.Background(), fixtureStartConfig(t, helper, "wrong-provider", "token-identity"))
	if err == nil || !strings.Contains(err.Error(), "provider identity") {
		t.Fatalf("startSession() error = %v, want provider identity error", err)
	}
}

func TestSessionFaultsWhenInitializeEchoesSecret(t *testing.T) {
	helper := buildFixtureProvider(t)
	const secret = "token-init-echo-canary"
	_, err := startSession(context.Background(), fixtureStartConfig(t, helper, "echo-initialize-secret", secret))
	if err == nil {
		t.Fatal("startSession() error = nil, want secret echo rejection")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("startSession() error leaked secret: %v", err)
	}
}

func TestSessionFaultsWhenBusinessResponseEchoesSecret(t *testing.T) {
	const secret = "token-health-echo-canary"
	session := startFixtureSession(t, "echo-health-secret", secret)
	_, err := session.Health(context.Background())
	if err == nil {
		t.Fatal("Health() error = nil, want secret echo rejection")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("Health() error leaked secret: %v", err)
	}
	select {
	case <-session.Done():
	case <-time.After(time.Second):
		t.Fatal("Session did not fault after secret echo")
	}
	if session.State() != StateFaulted {
		t.Fatalf("State() = %q, want faulted", session.State())
	}
}

func TestSessionDoesNotExposeProviderErrorMessage(t *testing.T) {
	session := startFixtureSession(t, "private-health-error", "token-safe-error")
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = session.Shutdown(ctx)
	})

	_, err := session.Health(context.Background())
	var callErr *CallError
	if !errors.As(err, &callErr) {
		t.Fatalf("Health() error = %v, want CallError", err)
	}
	if callErr.Code != protocol.ErrorTemporarilyUnavailable {
		t.Fatalf("CallError.Code = %q", callErr.Code)
	}
	if strings.Contains(callErr.Message, "private.memory.invalid") || len(callErr.Message) > 128 {
		t.Fatalf("CallError.Message exposed Provider text: %q", callErr.Message)
	}
}

func TestSessionValidatesCallsBeforeSending(t *testing.T) {
	session := startFixtureSession(t, "normal", "token-validation")
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = session.Shutdown(ctx)
	})

	invalidConnection := testIdentity(false)
	invalidConnection.ConnectionID = "other-connection"
	if _, err := session.Recall(context.Background(), CallMeta{}, model.RecallRequest{
		Identity: invalidConnection, Query: "probe",
	}); err == nil {
		t.Fatal("Recall() accepted another connection ID")
	}
	if _, err := session.CaptureTurn(context.Background(), CallMeta{}, model.Turn{
		Identity: testIdentity(true), Messages: []model.Message{{Role: "user", Content: "probe"}},
	}); err == nil {
		t.Fatal("CaptureTurn() accepted an empty idempotency key")
	}
	expired, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	if _, err := session.Health(expired); err == nil {
		t.Fatal("Health() accepted an expired context")
	}
}

func TestSessionEnforcesNegotiatedCaptureRoles(t *testing.T) {
	session := startFixtureSession(t, "user-role-only", "token-role-boundary")
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = session.Shutdown(ctx)
	})

	_, err := session.CaptureTurn(context.Background(), CallMeta{IdempotencyKey: "role-boundary"}, model.Turn{
		Identity: testIdentity(true),
		Messages: []model.Message{
			{Role: "user", Content: "remember"},
			{Role: "assistant", Content: "acknowledged"},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "role") {
		t.Fatalf("CaptureTurn() error = %v, want negotiated role rejection", err)
	}
}

func TestSessionEnforcesNegotiatedRecallRequestScopes(t *testing.T) {
	t.Run("user scope required", func(t *testing.T) {
		session := startFixtureSession(t, "agent-scope-only", "token-user-scope")
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			_ = session.Shutdown(ctx)
		})

		_, err := session.Recall(context.Background(), CallMeta{}, model.RecallRequest{
			Identity: testIdentity(false), Query: "private memory", MaxItems: 1,
		})
		if err == nil || !strings.Contains(err.Error(), "user") {
			t.Fatalf("Recall() error = %v, want missing user scope rejection", err)
		}
	})

	t.Run("agent scope required for shared recall", func(t *testing.T) {
		session := startFixtureSession(t, "normal", "token-agent-scope")
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			_ = session.Shutdown(ctx)
		})

		_, err := session.Recall(context.Background(), CallMeta{}, model.RecallRequest{
			Identity: testIdentity(false), Query: "shared memory", MaxItems: 1, IncludeAgentShared: true,
		})
		if err == nil || !strings.Contains(err.Error(), "agent") {
			t.Fatalf("Recall() error = %v, want missing agent scope rejection", err)
		}
	})
}

func TestSessionFaultsWhenRecallResultExceedsNegotiatedScopes(t *testing.T) {
	session := startFixtureSession(t, "user-only-return-agent", "token-response-scope")
	_, err := session.Recall(context.Background(), CallMeta{}, model.RecallRequest{
		Identity: testIdentity(false), Query: "private memory", MaxItems: 1,
	})
	if err == nil {
		t.Fatal("Recall() error = nil, want response scope rejection")
	}
	select {
	case <-session.Done():
	case <-time.After(time.Second):
		t.Fatal("Session did not fault after out-of-scope recall result")
	}
	if session.State() != StateFaulted {
		t.Fatalf("State() = %q, want faulted", session.State())
	}
}

func TestSessionFaultsWhenDefaultRecallReturnsAgentScope(t *testing.T) {
	session := startFixtureSession(t, "shared-capable-return-agent", "token-default-scope")
	_, err := session.Recall(context.Background(), CallMeta{}, model.RecallRequest{
		Identity: testIdentity(false), Query: "private memory", MaxItems: 1,
	})
	if err == nil {
		t.Fatal("Recall() error = nil, want default recall agent-scope rejection")
	}
	select {
	case <-session.Done():
	case <-time.After(time.Second):
		t.Fatal("Session did not fault after default recall returned Agent-shared context")
	}
}

func TestSessionAllowsAgentScopeOnlyForOptInRecall(t *testing.T) {
	session := startFixtureSession(t, "shared-capable-return-agent", "token-opt-in-scope")
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = session.Shutdown(ctx)
	})
	bundle, err := session.Recall(context.Background(), CallMeta{}, model.RecallRequest{
		Identity: testIdentity(false), Query: "shared memory", MaxItems: 1, IncludeAgentShared: true,
	})
	if err != nil {
		t.Fatalf("Recall() error = %v", err)
	}
	if len(bundle.Items) != 1 || bundle.Items[0].Scope != model.ScopeAgent {
		t.Fatalf("Recall() = %#v", bundle)
	}
}

func TestValidContextBundleEnforcesFieldByteLimits(t *testing.T) {
	valid := model.ContextBundle{
		Items: []model.ContextItem{{
			ID: strings.Repeat("i", 512), Kind: strings.Repeat("k", 128), Scope: model.ScopeUser,
			Text: strings.Repeat("t", 256<<10), Source: strings.Repeat("s", 1024),
		}},
		Warnings: make([]string, 8),
	}
	for index := range valid.Warnings {
		valid.Warnings[index] = strings.Repeat("w", 512)
	}
	if !validContextBundle(valid, 1, []string{"user"}) {
		t.Fatal("validContextBundle() rejected values at field limits")
	}

	tests := []struct {
		name   string
		mutate func(*model.ContextBundle)
	}{
		{name: "id", mutate: func(bundle *model.ContextBundle) { bundle.Items[0].ID += "x" }},
		{name: "kind", mutate: func(bundle *model.ContextBundle) { bundle.Items[0].Kind += "x" }},
		{name: "source", mutate: func(bundle *model.ContextBundle) { bundle.Items[0].Source += "x" }},
		{name: "text", mutate: func(bundle *model.ContextBundle) { bundle.Items[0].Text += "x" }},
		{name: "warning", mutate: func(bundle *model.ContextBundle) { bundle.Warnings[0] += "x" }},
		{name: "warning count", mutate: func(bundle *model.ContextBundle) { bundle.Warnings = append(bundle.Warnings, "x") }},
		{name: "utf8 bytes", mutate: func(bundle *model.ContextBundle) { bundle.Items[0].Kind = strings.Repeat("记", 43) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			candidate := valid
			candidate.Items = append([]model.ContextItem(nil), valid.Items...)
			candidate.Warnings = append([]string(nil), valid.Warnings...)
			tt.mutate(&candidate)
			if validContextBundle(candidate, 1, []string{"user"}) {
				t.Fatal("validContextBundle() accepted an oversized field")
			}
		})
	}
}

func TestSessionFaultsOnOversizedRecallResult(t *testing.T) {
	session := startFixtureSession(t, "oversized-context-text", "token-context-size")
	_, err := session.Recall(context.Background(), CallMeta{}, model.RecallRequest{
		Identity: testIdentity(false), Query: "private memory", MaxItems: 1,
	})
	if err == nil {
		t.Fatal("Recall() error = nil, want oversized result rejection")
	}
	select {
	case <-session.Done():
	case <-time.After(time.Second):
		t.Fatal("Session did not fault after oversized recall result")
	}
}

var fixtureBuild struct {
	sync.Mutex
	path string
	err  error
}

func buildFixtureProvider(t *testing.T) string {
	t.Helper()
	fixtureBuild.Lock()
	defer fixtureBuild.Unlock()
	if fixtureBuild.path != "" || fixtureBuild.err != nil {
		if fixtureBuild.err != nil {
			t.Fatalf("build fixture Provider: %v", fixtureBuild.err)
		}
		return fixtureBuild.path
	}
	dir, err := os.MkdirTemp("", "mlink-helper-provider-")
	if err != nil {
		fixtureBuild.err = err
		t.Fatal(err)
	}
	path := filepath.Join(dir, "helper-provider")
	goBinary := filepath.Join(runtime.GOROOT(), "bin", "go")
	command := exec.Command(goBinary, "build", "-o", path, "./internal/provider/host/testdata/helper_provider")
	command.Dir = moduleRoot(t)
	output, err := command.CombinedOutput()
	if err != nil {
		fixtureBuild.err = err
		t.Fatalf("build fixture Provider: %v: %s", err, output)
	}
	fixtureBuild.path = path
	return path
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "../../.."))
}

func startFixtureSession(t *testing.T, mode, secret string) *processSession {
	t.Helper()
	helper := buildFixtureProvider(t)
	session, err := startSession(context.Background(), fixtureStartConfig(t, helper, mode, secret))
	if err != nil {
		t.Fatalf("startSession() error = %v", err)
	}
	return session
}

func fixtureStartConfig(t *testing.T, helper, mode, secret string) startConfig {
	t.Helper()
	snapshot, err := connection.NewSnapshot(
		"connection-test",
		connection.ProviderRef{ID: "dev.mlink.fixture", Version: "0.1.0"},
		"revision-test",
		json.RawMessage(`{"fixture":true}`),
		map[string]string{"token": "keychain://fixture/token"},
	)
	if err != nil {
		t.Fatal(err)
	}
	return startConfig{
		Snapshot: snapshot,
		Manifest: manifest.Manifest{
			ProviderID: "dev.mlink.fixture", Version: "0.1.0", ProtocolVersions: []string{"1.0"},
			DeclaredCapabilities: fixtureDeclaredCapabilities(),
		},
		Argv:             []string{helper, mode},
		WorkingDirectory: filepath.Dir(helper),
		RuntimeDirectory: filepath.Join(t.TempDir(), "runtime"),
		Secrets:          map[string]string{"token": secret},
		ParentEnvironment: append(os.Environ(),
			"OPENAI_API_KEY=agent-openai-canary", "ANTHROPIC_BASE_URL=https://agent-model.invalid",
		),
	}
}

func fixtureDeclaredCapabilities() map[string]manifest.CapabilityDescriptor {
	return map[string]manifest.CapabilityDescriptor{
		"health": {Version: 1, MaxInFlight: 4},
		"capture_turn": {
			Version: 1, Roles: []string{"user", "assistant"}, MaxRequestBytes: 3800 << 10,
			MaxInFlight: 4, ReplaySafe: false, Ordering: "turn",
		},
		"recall": {
			Version: 1, Scopes: []string{"user", "agent"}, MaxRequestBytes: 3800 << 10,
			MaxResultItems: 10, MaxInFlight: 4,
		},
	}
}

func testIdentity(capture bool) model.IdentityScope {
	identity := model.IdentityScope{
		ConnectionID: "connection-test", TenantID: "team-a", UserID: "user-a", AgentID: "agent-a",
	}
	if capture {
		identity.SessionID = "session-a"
		identity.TurnID = "turn-a"
	}
	return identity
}
