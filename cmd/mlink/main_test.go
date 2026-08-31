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
	"mlink/internal/doctor"
	"mlink/internal/identity"
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
	configuration := fixtureRuntimeConfigV2()
	router, grants, hermesEnabled, err := runtimeRouter(context.Background(), configuration, runtimeSecretStore{
		"identity/hmac-key":                     bytes.Repeat([]byte{0x2a}, 32),
		"identity/binding/owner-feishu-union-1": []byte("on_owner"),
		"adapter/hermes/token":                  []byte("hermes-token"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !hermesEnabled || router == nil || len(grants) != 3 || grants[0].FixedSpaceID != "personal-owner" || grants[2].Source != "feishu" || len(grants[2].TokenDigest) != 32 {
		t.Fatalf("router/grants = %#v %#v", router, grants)
	}
	resolved, err := router.ResolveHermes(identity.ExternalContext{Source: "feishu", ChatType: "dm", ChatID: "oc_dm", AlternateSubject: "on_owner"})
	if err != nil || resolved.UserID != "usr_owner_keliang" {
		t.Fatalf("owner = %#v %v", resolved, err)
	}
}

func fixtureRuntimeConfigV2() config.Config {
	return config.Config{
		SchemaVersion: 2, NamespaceID: "personal", ActiveConnectionID: "local",
		Connections: map[string]config.Connection{"local": {ID: "local", ProviderID: "dev.mlink.tencentdb", ProviderVersion: "0.1.0", ConfigRevision: "rev-2"}},
		Principals:  map[string]config.Principal{"owner": {ID: "owner", CanonicalUserID: "usr_owner_keliang", Kind: config.PrincipalPerson}},
		Spaces: map[string]config.MemorySpace{
			"personal-owner": {ID: "personal-owner", ConnectionID: "local", TenantID: "personal", AgentID: "keliang-personal", IncludeAgentShared: true, PrincipalPolicy: config.PolicyFixed, PrincipalID: "owner"},
			"hermes-private": {ID: "hermes-private", ConnectionID: "local", TenantID: "personal", AgentID: "hermes-private", PrincipalPolicy: config.PolicyExternalHMAC},
			"hermes-groups":  {ID: "hermes-groups", ConnectionID: "local", TenantID: "personal", AgentID: "hermes-groups", PrincipalPolicy: config.PolicyGroupHMAC},
		},
		Adapters: map[string]config.Adapter{
			"codex":  {ID: "codex", Enabled: true, SpaceID: "personal-owner"},
			"pi":     {ID: "pi", Enabled: true, SpaceID: "personal-owner"},
			"hermes": {ID: "hermes", Enabled: true, HermesRouting: &config.HermesRouting{OwnerSpaceID: "personal-owner", PrivateSpaceID: "hermes-private", GroupSpaceID: "hermes-groups"}},
		},
		Bindings: map[string]config.BindingRef{"owner-feishu-union-1": {
			ID: "owner-feishu-union-1", Source: "feishu", Kind: "union_id", PrincipalID: "owner",
			SecretRef: "keychain://dev.mlink/identity/binding/owner-feishu-union-1", Status: config.BindingActive,
		}},
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

func TestInspectHermesSessionPolicyRequiresBothSharedFlags(t *testing.T) {
	active := inspectHermesSessionPolicy([]byte("group_sessions_per_user: false\nthread_sessions_per_user: false\n"))
	if active.State != doctor.StatePassed || active.Code != "active" {
		t.Fatalf("active = %#v", active)
	}
	groupSplit := inspectHermesSessionPolicy([]byte("group_sessions_per_user: true\nthread_sessions_per_user: false\n"))
	if groupSplit.Code != "group_session_split" {
		t.Fatalf("group = %#v", groupSplit)
	}
	threadSplit := inspectHermesSessionPolicy([]byte("group_sessions_per_user: false\nthread_sessions_per_user: true\n"))
	if threadSplit.Code != "thread_session_split" {
		t.Fatalf("thread = %#v", threadSplit)
	}
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
