package broker

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"mlink/internal/connection"
	"mlink/internal/identity"
)

func TestServeUnixCreatesUserOnlySocketAndAcceptsFixedGrant(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "mlink-socket-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "mlink.sock")
	server := Server{Authorizer: Authorizer{
		Resolver: identity.Resolver{NamespaceID: "personal", Key: make([]byte, 32)},
		Grants: []Grant{{
			AdapterID: "codex", Mode: IdentityFixed,
			Route:    connection.RouteKey{ConnectionID: "local", ProviderID: "dev.mlink.tencentdb", ProviderVersion: "0.1.0", ConfigRevision: "rev-1"},
			TenantID: "personal", AgentID: "codex", UserID: "usr_codex",
		}},
	}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- server.ServeUnix(ctx, socket) }()
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(socket); err == nil {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("ServeUnix() exited early: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("Unix socket was not created")
		}
		time.Sleep(10 * time.Millisecond)
	}
	info, err := os.Stat(socket)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("socket mode = %o, want 600", got)
	}
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}
	client := &http.Client{Transport: transport}
	request, _ := http.NewRequest(http.MethodGet, "http://mlink/v1/health?adapter_id=codex", nil)
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", response.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("ServeUnix() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ServeUnix did not stop")
	}
}
