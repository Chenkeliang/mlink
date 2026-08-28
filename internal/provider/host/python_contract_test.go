package host

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mlink/internal/connection"
	"mlink/internal/model"
	"mlink/internal/provider/manifest"
	"mlink/internal/provider/protocol"
)

func TestPythonProviderLifecycle(t *testing.T) {
	session := startPythonSession(t, "normal", "python-token-normal")
	health, err := session.Health(context.Background())
	if err != nil || health.Backend != "available" {
		t.Fatalf("Health() = %#v, %v", health, err)
	}
	receipt, err := session.CaptureTurn(context.Background(), CallMeta{IdempotencyKey: "python-capture"}, largeTurn("python memory"))
	if err != nil {
		t.Fatalf("CaptureTurn() error = %v", err)
	}
	if receipt.ReceiptID == "" || receipt.State != model.WriteAccepted || receipt.ProviderRefs[0] != "python-ref" {
		t.Fatalf("receipt = %#v", receipt)
	}
	bundle, err := session.Recall(context.Background(), CallMeta{}, model.RecallRequest{
		Identity: testIdentity(false), Query: "python", MaxItems: 5,
	})
	if err != nil || len(bundle.Items) != 1 || bundle.Items[0].ID != "python-memory" {
		t.Fatalf("Recall() = %#v, %v", bundle, err)
	}
	shutdownFixture(t, session)
}

func TestPythonProviderCancellation(t *testing.T) {
	session := startPythonSession(t, "slow-recall", "python-token-cancel")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := session.Recall(ctx, CallMeta{}, model.RecallRequest{
			Identity: testIdentity(false), Query: "block", MaxItems: 5,
		})
		done <- err
	}()
	waitForDiagnostic(t, session, "PYTHON_RECALL_STARTED")
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Recall() error = %v, want context.Canceled", err)
	}
	time.Sleep(50 * time.Millisecond)
	if session.State() != StateReady {
		t.Fatalf("State() = %q after late cancel response", session.State())
	}
	shutdownFixture(t, session)
}

func TestPythonProviderPreservesLargeDecimalStringID(t *testing.T) {
	python := requirePython3(t)
	command := exec.Command(python, pythonFixturePath(t), "normal")
	stdin, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	command.Stderr = os.Stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = command.Process.Kill(); _ = command.Wait() })

	const largeID = "900719925474099312345678901234567890"
	raw, err := protocol.EncodeRequest(largeID, "initialize", protocol.InitializeParams{
		Meta:             protocol.RequestMeta{RequestID: "python-large-id", DeadlineUnixMS: time.Now().Add(time.Second).UnixMilli()},
		ProtocolVersions: []string{"1.0"},
		Route: connection.RouteKey{
			ConnectionID: "connection-test", ProviderID: "dev.mlink.fixture",
			ProviderVersion: "0.1.0", ConfigRevision: "revision-test",
		},
		Config: json.RawMessage(`{}`), Secrets: map[string]string{"token": "python-large-id-token"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := protocol.NewEncoder(stdin).WriteFrame(raw); err != nil {
		t.Fatal(err)
	}
	responseRaw, err := protocol.NewDecoder(bufio.NewReader(stdout)).ReadFrame()
	if err != nil {
		t.Fatal(err)
	}
	response, err := protocol.ParseMessage(responseRaw)
	if err != nil {
		t.Fatal(err)
	}
	if response.ID != largeID {
		t.Fatalf("response ID = %q, want %q", response.ID, largeID)
	}
}

func TestPythonProviderAdversarialInitialization(t *testing.T) {
	tests := []string{"garbage", "oversized", "wrong-provider", "capability-escalation", "echo-initialize-secret"}
	for _, mode := range tests {
		t.Run(mode, func(t *testing.T) {
			secret := "python-secret-" + mode
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_, err := startSession(ctx, pythonStartConfig(t, mode, secret))
			if err == nil {
				t.Fatal("startSession() error = nil, want adversarial rejection")
			}
			if strings.Contains(err.Error(), secret) {
				t.Fatalf("startSession() leaked secret: %v", err)
			}
		})
	}
}

func TestPythonProviderSplitStderrSecretIsRedacted(t *testing.T) {
	const secret = "python-split-stderr-secret"
	session := startPythonSession(t, "split-stderr-secret", secret)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		diagnostic := session.stderr.String()
		if strings.Contains(diagnostic, "[REDACTED]") {
			if strings.Contains(diagnostic, secret) {
				t.Fatalf("stderr leaked secret: %q", diagnostic)
			}
			shutdownFixture(t, session)
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("redacted stderr marker not observed: %q", session.stderr.String())
}

func startPythonSession(t *testing.T, mode, secret string) *processSession {
	t.Helper()
	session, err := startSession(context.Background(), pythonStartConfig(t, mode, secret))
	if err != nil {
		t.Fatalf("startSession() error = %v", err)
	}
	return session
}

func pythonStartConfig(t *testing.T, mode, secret string) startConfig {
	t.Helper()
	snapshot, err := connection.NewSnapshot(
		"connection-test", connection.ProviderRef{ID: "dev.mlink.fixture", Version: "0.1.0"},
		"revision-test", json.RawMessage(`{"fixture":"python"}`), map[string]string{"token": "keychain://python/token"},
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
		Argv:              []string{requirePython3(t), pythonFixturePath(t), mode},
		WorkingDirectory:  filepath.Dir(pythonFixturePath(t)),
		RuntimeDirectory:  filepath.Join(t.TempDir(), "runtime"),
		Secrets:           map[string]string{"token": secret},
		ParentEnvironment: os.Environ(),
	}
}

func requirePython3(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("python3")
	if err != nil {
		t.Fatalf("python3 is required for the cross-language Provider contract; install Python 3: %v", err)
	}
	return path
}

func pythonFixturePath(t *testing.T) string {
	t.Helper()
	return filepath.Join(moduleRoot(t), "internal/provider/host/testdata/python_provider.py")
}
