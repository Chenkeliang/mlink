package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"mlink/internal/app"
	"mlink/internal/broker"
	"mlink/internal/cli"
	"mlink/internal/config"
	"mlink/internal/connection"
	"mlink/internal/doctor"
	"mlink/internal/identity"
	"mlink/internal/install"
	"mlink/internal/journal"
	"mlink/internal/layout"
	"mlink/internal/model"
	"mlink/internal/version"
)

func TestRuntimeJournalMaintenanceUsesReadOnlyPreviewAndWritableApply(t *testing.T) {
	ctx := context.Background()
	home := t.TempDir()
	paths, err := layout.FromHome(home, "/tmp/mlink-candidate")
	if err != nil {
		t.Fatal(err)
	}
	store, err := journal.Open(ctx, paths.Journal)
	if err != nil {
		t.Fatal(err)
	}
	event, _, err := store.EnqueueTurn(ctx, journal.Envelope{
		AdapterID: "codex",
		Route:     connection.RouteKey{ConnectionID: "local", ProviderID: "dev.mlink.tencentdb", ProviderVersion: "0.1.0", ConfigRevision: "rev-3"},
		Turn:      model.Turn{Identity: model.IdentityScope{TenantID: "team", AgentID: "agent", UserID: "user", SessionID: "session", TurnID: "turn"}, Messages: []model.Message{{Role: "user", Content: "private"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MarkAmbiguous(ctx, event.ID, "response_lost"); err != nil {
		t.Fatal(err)
	}
	_ = store.Close()
	runtime := &runtimeApplication{paths: paths, uid: 501}
	descriptors, err := runtime.ListUnresolvedJournalEvents(ctx)
	if err != nil || len(descriptors) != 1 {
		t.Fatalf("descriptors/error = %#v/%v", descriptors, err)
	}
	request := app.JournalResolutionRequest{EventRef: descriptors[0].EventSuffix, Resolution: journal.ResolutionDiscarded, Reason: "legacy scope inactive"}
	plan, err := runtime.PlanJournalResolution(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.ApplyJournalResolution(ctx, plan.PlanID, request); err != nil {
		t.Fatal(err)
	}
	store, err = journal.OpenReadOnly(ctx, paths.Journal)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	remaining, err := store.ListUnresolvedEvents(ctx)
	if err != nil || len(remaining) != 0 {
		t.Fatalf("remaining/error = %#v/%v", remaining, err)
	}
}

func TestHTTPHermesGrantVerifierRequiresNewAcceptedAndOldRejected(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.Header.Get("Authorization") {
		case "Bearer new-grant":
			writer.WriteHeader(http.StatusOK)
		case "Bearer old-grant":
			writer.WriteHeader(http.StatusUnauthorized)
		default:
			writer.WriteHeader(http.StatusForbidden)
		}
	}))
	defer server.Close()
	verifier := httpHermesGrantVerifier{Client: server.Client()}
	if err := verifier.VerifyHermesGrant(context.Background(), server.URL, []byte("new-grant"), []byte("old-grant")); err != nil {
		t.Fatal(err)
	}
	if err := verifier.VerifyHermesGrant(context.Background(), server.URL, []byte("old-grant"), []byte("new-grant")); err == nil {
		t.Fatal("accepted inverted grants")
	}
}

func TestLocalUpgradeLoaderRejectsSymlinkAndUnsafeMode(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "mlink-new")
	testExecutable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	executableData, err := os.ReadFile(testExecutable)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, executableData, 0o700); err != nil {
		t.Fatal(err)
	}
	runnerCalls := 0
	loader := localUpgradeLoader{RunVersion: func(context.Context, string) ([]byte, error) {
		runnerCalls++
		return []byte(`{"version":"1.0.0","commit":"abc","build_date":"now","go_version":"go1.27.0","goos":"` + runtime.GOOS + `","goarch":"` + runtime.GOARCH + `","schema_min":2,"schema_max":3}`), nil
	}}
	candidate, err := loader.LoadUpgradeCandidate(context.Background(), path, os.Getuid())
	if err != nil || candidate.Info.Version != "1.0.0" || runnerCalls != 1 {
		t.Fatalf("candidate/error/calls = %#v/%v/%d", candidate, err, runnerCalls)
	}
	if err := os.Chmod(path, 0o722); err != nil {
		t.Fatal(err)
	}
	if _, err := loader.LoadUpgradeCandidate(context.Background(), path, os.Getuid()); err == nil {
		t.Fatal("accepted group/world-writable candidate")
	}
	link := filepath.Join(directory, "mlink-link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := loader.LoadUpgradeCandidate(context.Background(), link, os.Getuid()); err == nil {
		t.Fatal("accepted symlink candidate")
	}
}

func TestLocalInstalledVerifierChecksHashAndVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mlink")
	testExecutable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(testExecutable)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, content, 0o700); err != nil {
		t.Fatal(err)
	}
	verifier := localInstalledVerifier{Loader: localUpgradeLoader{RunVersion: func(context.Context, string) ([]byte, error) {
		data, _ := json.Marshal(version.Info{Version: "1.0.0", GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, SchemaMin: 2, SchemaMax: 3})
		return data, nil
	}}}
	digest := sha256.Sum256(content)
	if err := verifier.VerifyInstalledBinary(context.Background(), path, hex.EncodeToString(digest[:])); err != nil {
		t.Fatal(err)
	}
	if err := verifier.VerifyInstalledBinary(context.Background(), path, strings.Repeat("0", 64)); err == nil {
		t.Fatal("accepted wrong installed hash")
	}
}

func TestInspectHubContainerChecksPinnedImageArgsAndVolume(t *testing.T) {
	fixture := []byte(`[{
		"Config":{"Image":"agentmemory/memory-hub@sha256:7be68305b9ab279407584ffe44300605a5833df57a1730ca5bd41bbbe4b3f104","Cmd":null,"Labels":{"dev.mlink.component":"memory-hub"}},
		"HostConfig":{"RestartPolicy":{"Name":"unless-stopped"}},
		"Mounts":[{"Name":"tdai-panel-data","Destination":"/data/knowledge"}]
	}]`)
	checks := inspectHubContainer(fixture)
	if len(checks) != 3 || checks[0].State != doctor.StatePassed || checks[1].State != doctor.StatePassed || checks[2].State != doctor.StatePassed {
		t.Fatalf("checks = %#v", checks)
	}
	checks = inspectHubContainer(bytes.ReplaceAll(fixture, []byte("tdai-panel-data"), []byte("wrong-volume")))
	if checks[2].Code != "volume_mismatch" {
		t.Fatalf("volume checks = %#v", checks)
	}
}

func TestCompareHermesGrantUsesFingerprintWithoutReturningSecret(t *testing.T) {
	check := compareHermesGrant([]byte(`{"endpoint":"http://host.internal:8097","token":"same-secret"}`), []byte("same-secret"))
	if check.State != doctor.StatePassed || check.Code != "fingerprint_match" || strings.Contains(check.Message, "same-secret") {
		t.Fatalf("check = %#v", check)
	}
	check = compareHermesGrant([]byte(`{"endpoint":"http://host.internal:8097","token":"other"}`), []byte("same-secret"))
	if check.Code != "fingerprint_mismatch" {
		t.Fatalf("mismatch = %#v", check)
	}
}

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

func TestRuntimeAuthorizerV3UsesGeneratedOwnerControlPlane(t *testing.T) {
	ctx := context.Background()
	configuration := fixtureRuntimeConfigV3()
	secrets := runtimeSecretStore{
		"identity/hmac-key":                     bytes.Repeat([]byte{0x2a}, 32),
		"identity/binding/owner-feishu-union-1": []byte("on_owner"),
		"adapter/hermes/token":                  []byte("hermes-token"),
		"connection/local/token":                []byte("gateway-token"),
		"control/tencentdb/owner-user-key":      []byte("owner-key"),
	}
	router, grants, hermesEnabled, err := runtimeRouter(ctx, configuration, secrets)
	if err != nil {
		t.Fatal(err)
	}
	store, err := journal.Open(ctx, t.TempDir()+"/journal.db")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	authorizer, err := runtimeAuthorizer(ctx, configuration, secrets, store, router, grants, hermesEnabled)
	if err != nil {
		t.Fatal(err)
	}
	if authorizer.ControlPlane != configuration.ControlPlane || authorizer.DynamicAgents == nil {
		t.Fatalf("authorizer = %#v", authorizer)
	}
	fixed, err := authorizer.Resolve(ctx, grants[0], identity.ExternalContext{}, "", "codex-session")
	if err != nil {
		t.Fatal(err)
	}
	if fixed.Identity.UserID != "usr-owner-generated" || fixed.Identity.TenantID != "team-owner-generated" ||
		fixed.Identity.AgentID != "agt-owner-generated" || !fixed.IncludeAgentShared {
		t.Fatalf("fixed authorization = %#v", fixed)
	}
	owner, err := authorizer.Resolve(ctx, grants[2], identity.ExternalContext{
		Source: "feishu", ChatType: "dm", ChatID: "oc_dm", AlternateSubject: "on_owner",
	}, "", "")
	if err != nil || owner.Identity.UserID != fixed.Identity.UserID || owner.Identity.AgentID != fixed.Identity.AgentID || !owner.IncludeAgentShared {
		t.Fatalf("Owner Hermes authorization = %#v, %v", owner, err)
	}
}

func TestRuntimeRouterGrantsCursorFixedOwnerIdentity(t *testing.T) {
	configuration := fixtureRuntimeConfigV3()
	configuration.Adapters["cursor"] = config.Adapter{ID: "cursor", Enabled: true, SpaceID: "owner"}
	_, grants, _, err := runtimeRouter(context.Background(), configuration, runtimeSecretStore{
		"identity/hmac-key":                     bytes.Repeat([]byte{0x2a}, 32),
		"identity/binding/owner-feishu-union-1": []byte("on_owner"),
		"adapter/hermes/token":                  []byte("hermes-token"),
	})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, grant := range grants {
		found = found || grant.AdapterID == "cursor" && grant.Mode == broker.IdentityFixed && grant.FixedSpaceID == "owner"
	}
	if !found {
		t.Fatalf("Cursor grant missing: %#v", grants)
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

func fixtureRuntimeConfigV3() config.Config {
	return config.Config{
		SchemaVersion: 3, NamespaceID: "installation-1", ActiveConnectionID: "local",
		Connections: map[string]config.Connection{"local": {
			ID: "local", ProviderID: "dev.mlink.tencentdb", ProviderVersion: "0.1.0", ConfigRevision: "rev-3",
			ProviderConfig: map[string]any{"base_url": "http://127.0.0.1:8420", "service_id": "default", "timeout_ms": 5000},
			SecretRefs:     map[string]string{"token": "keychain://dev.mlink/connection/local/token"},
		}},
		Principals: map[string]config.Principal{"owner": {ID: "owner", CanonicalUserID: "usr-owner-generated", Kind: config.PrincipalPerson}},
		Adapters: map[string]config.Adapter{
			"codex": {ID: "codex", Enabled: true, SpaceID: "owner"},
			"pi":    {ID: "pi", Enabled: true, SpaceID: "owner"},
			"hermes": {ID: "hermes", Enabled: true, HermesRouting: &config.HermesRouting{
				OwnerSpaceID: "owner", PrivateSpaceID: "hermes-private", GroupSpaceID: "hermes-groups",
			}},
		},
		Bindings: map[string]config.BindingRef{"owner-feishu-union-1": {
			ID: "owner-feishu-union-1", Source: "feishu", Kind: "union_id", PrincipalID: "owner",
			SecretRef: "keychain://dev.mlink/identity/binding/owner-feishu-union-1", Status: config.BindingActive,
		}},
		ControlPlane: &config.ControlPlane{
			ProviderID: "dev.mlink.tencentdb", InstanceID: "default", PanelURL: "http://127.0.0.1:8125",
			OwnerUserID: "usr-owner-generated", OwnerTeamID: "team-owner-generated", OwnerAgentID: "agt-owner-generated",
			OwnerAssetID: "chat_memory-team-owner-generated-agt-owner-generated", DynamicAgentLimit: 500,
		},
		RoutingPolicies: map[string]config.RoutingPolicy{
			"owner":          {ID: "owner", Layers: []config.MemoryLayer{config.LayerL1, config.LayerL2, config.LayerL3}, AgentPolicy: config.AgentFixed},
			"hermes-private": {ID: "hermes-private", Layers: []config.MemoryLayer{config.LayerL1}, AgentPolicy: config.AgentDynamicPrincipal},
			"hermes-groups":  {ID: "hermes-groups", Layers: []config.MemoryLayer{config.LayerL1}, AgentPolicy: config.AgentDynamicGroup, SessionPolicy: config.SessionPerTopic},
		},
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
	decoded, err := base64.RawURLEncoding.DecodeString(string(first))
	if err != nil || len(first) != 43 || len(decoded) != 32 {
		t.Fatalf("grant encoding/length = %q/%d/%d, %v", first, len(first), len(decoded), err)
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
