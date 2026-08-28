package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"reflect"
	"testing"

	"mlink/internal/app"
	"mlink/internal/cli"
	"mlink/internal/config"
	"mlink/internal/install"
)

type runtimeSecretStore map[string][]byte

func (store runtimeSecretStore) Put(context.Context, string, []byte) error { return nil }
func (store runtimeSecretStore) Delete(context.Context, string) error      { return nil }
func (store runtimeSecretStore) Get(_ context.Context, account string) ([]byte, error) {
	value, exists := store[account]
	if !exists {
		return nil, errors.New("missing")
	}
	return append([]byte(nil), value...), nil
}

func TestRunDependenciesStartTencentDBProvider(t *testing.T) {
	calls := 0
	deps := runDependencies(&bytes.Buffer{}, &bytes.Buffer{}, func(context.Context) error {
		calls++
		return nil
	})
	if code := cli.Run(context.Background(), []string{"provider", "run", "tencentdb"}, deps); code != 0 {
		t.Fatalf("Run() code = %d", code)
	}
	if calls != 1 {
		t.Fatalf("serve calls = %d, want 1", calls)
	}
}

func TestBrokerGrantsSeparateFixedAndDelegatedIdentity(t *testing.T) {
	runtime := &runtimeApplication{}
	connectionConfig := config.Connection{
		ID: "local", ProviderID: "dev.mlink.tencentdb", ProviderVersion: "0.1.0", ConfigRevision: "rev-1",
		TenantID: "personal", AgentID: "default", UserID: "local-user",
	}
	configuration := config.Config{Adapters: map[string]config.Adapter{
		"codex":  {ID: "codex", Enabled: true},
		"hermes": {ID: "hermes", Enabled: true},
	}}
	grants, hermesEnabled, err := runtime.brokerGrants(context.Background(), configuration, connectionConfig, runtimeSecretStore{
		"adapter/hermes/token": []byte("hermes-token"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !hermesEnabled || len(grants) != 2 || grants[0].UserID != "local-user" || grants[1].Source != "feishu" || len(grants[1].TokenDigest) != 32 {
		t.Fatalf("grants = %#v", grants)
	}
}

func TestPrivateListenerRejectsUnspecifiedAddress(t *testing.T) {
	if _, err := privateListener("0.0.0.0:0"); err == nil {
		t.Fatal("privateListener() error = nil")
	}
	listener, err := privateListener("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_ = listener.Close()
}

func TestDeriveHermesGrantIsStableAndDomainSeparated(t *testing.T) {
	first := deriveHermesGrant([]byte("memorycore-token"))
	second := deriveHermesGrant([]byte("memorycore-token"))
	different := deriveHermesGrant([]byte("other-memorycore-token"))
	if len(first) != 32 {
		t.Fatalf("grant length = %d, want 32", len(first))
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatal("same MemoryCore token produced different Hermes grants")
	}
	if reflect.DeepEqual(first, different) || bytes.Equal(first, []byte("memorycore-token")) {
		t.Fatal("Hermes grant was not domain-separated from the MemoryCore token")
	}
}

func TestDefaultInstallPreviewDoesNotCreateUserState(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dependencies, err := defaultDependencies(&bytes.Buffer{}, &bytes.Buffer{}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	request := dependencies.InstallRequest
	request.Agents = []app.Agent{app.Codex}
	request.SecretInputs = map[string][]byte{app.MemoryCoreTokenSecret: []byte("test-token")}
	if _, err := dependencies.App.PlanInstall(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("preview created user state: %#v", entries)
	}
}

func TestStaleRuntimeApplyDoesNotCreateUserState(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dependencies, err := defaultDependencies(&bytes.Buffer{}, &bytes.Buffer{}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	request := dependencies.InstallRequest
	request.Agents = []app.Agent{app.Codex}
	request.SecretInputs = map[string][]byte{app.MemoryCoreTokenSecret: []byte("test-token")}
	err = dependencies.App.ApplyInstall(context.Background(), "plan_stale", request)
	if !errors.Is(err, install.ErrPlanStale) {
		t.Fatalf("error = %v", err)
	}
	entries, err := os.ReadDir(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("stale apply created user state: %#v", entries)
	}
}
