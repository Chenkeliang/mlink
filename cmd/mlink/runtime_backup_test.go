package main

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"mlink/internal/app"
	"mlink/internal/config"
	"mlink/internal/controlplane"
	"mlink/internal/identity"
	"mlink/internal/journal"
	"mlink/internal/layout"
	"mlink/internal/workspacebackup"
)

type restoreTestAgents struct {
	applied  []app.Agent
	verified int
	rolled   int
}

func (agents *restoreTestAgents) Apply(_ context.Context, selected []app.Agent) error {
	agents.applied = append([]app.Agent(nil), selected...)
	return nil
}
func (agents *restoreTestAgents) Verify(context.Context, []app.Agent) error {
	agents.verified++
	return nil
}
func (agents *restoreTestAgents) Rollback(context.Context) error { agents.rolled++; return nil }

type restoreTestSecrets struct{ values map[string][]byte }

func (store *restoreTestSecrets) Get(_ context.Context, account string) ([]byte, error) {
	value, exists := store.values[account]
	if !exists {
		return nil, fs.ErrNotExist
	}
	return append([]byte(nil), value...), nil
}
func (store *restoreTestSecrets) Put(_ context.Context, account string, value []byte) error {
	store.values[account] = append([]byte(nil), value...)
	return nil
}
func (store *restoreTestSecrets) Delete(_ context.Context, account string) error {
	if _, exists := store.values[account]; !exists {
		return fs.ErrNotExist
	}
	delete(store.values, account)
	return nil
}

func TestLocalWorkspaceRestorerStagesSecretsForProviderWithoutPersisting(t *testing.T) {
	restorer := restoreTestLocalRestorer(t)
	section := []byte(`{"gateway_token":"Z2F0ZXdheQ==","memory_llm_api_key":"bGxt","admin_user_key":"YWRtaW4=","owner_user_key":"b3duZXI="}`)
	if err := restorer.StageSection(context.Background(), workspacebackup.SectionSecrets, bytes.NewReader(section)); err != nil {
		t.Fatal(err)
	}
	request, err := restorer.ProviderRequest(context.Background(), workspacebackup.Manifest{Provider: workspacebackup.ProviderManifest{ProviderID: "dev.mlink.tencentdb"}})
	if err != nil {
		t.Fatal(err)
	}
	defer request.Wipe()
	if string(request.GatewayToken) != "gateway" || string(request.LLMAPIKey) != "llm" || string(request.OwnerUserKey) != "owner" {
		t.Fatalf("provider secrets were not staged")
	}
	if len(restorer.secrets.(*restoreTestSecrets).values) != 0 {
		t.Fatal("staging wrote Keychain values")
	}
}

func TestLocalWorkspaceRestorerExtractsOnlyKnownMLinkFiles(t *testing.T) {
	restorer := restoreTestLocalRestorer(t)
	archive := restoreTar(t, map[string]string{"config.yaml": "schema_version: 3\n", "journal.db": "sqlite", "memorycore/tdai-gateway.yaml": "gateway", "panel/metadata-instances.json": "[]"})
	if err := restorer.ApplySection(context.Background(), workspacebackup.SectionMLink, bytes.NewReader(archive)); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{restorer.paths.Config: "schema_version: 3\n", restorer.paths.Journal: "sqlite", filepath.Join(restorer.paths.Home, "memorycore", "tdai-gateway.yaml"): "gateway", restorer.paths.PanelRegistry: "[]"} {
		content, err := os.ReadFile(path)
		if err != nil || string(content) != want {
			t.Fatalf("restored %s = %q/%v", path, content, err)
		}
	}
	outside := filepath.Join(filepath.Dir(restorer.paths.Home), "escape")
	if err := restorer.ApplySection(context.Background(), workspacebackup.SectionMLink, bytes.NewReader(restoreTar(t, map[string]string{"../escape": "bad"}))); err == nil {
		t.Fatal("archive traversal was accepted")
	}
	if _, err := os.Stat(outside); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("archive escaped restore root: %v", err)
	}
}

func TestLocalWorkspaceRestorerPlanRejectsExistingStateAndRollbackRemovesCreatedState(t *testing.T) {
	restorer := restoreTestLocalRestorer(t)
	if err := os.MkdirAll(filepath.Dir(restorer.paths.Config), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(restorer.paths.Config, []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}
	request := app.WorkspaceRestoreRequest{BundlePath: "/tmp/full.mlink-backup", Passphrase: []byte("correct-passphrase"), SelectedAgents: []app.Agent{app.Codex}}
	if _, err := restorer.PlanRestore(context.Background(), request, workspacebackup.Manifest{}); err == nil {
		t.Fatal("existing MLink state was accepted")
	}
	if err := os.Remove(restorer.paths.Config); err != nil {
		t.Fatal(err)
	}
	restorer.secrets.(*restoreTestSecrets).values[controlplane.OwnerUserKeyAccount] = []byte("collision")
	if _, err := restorer.PlanRestore(context.Background(), request, workspacebackup.Manifest{}); err == nil {
		t.Fatal("existing Keychain state was accepted")
	}
	delete(restorer.secrets.(*restoreTestSecrets).values, controlplane.OwnerUserKeyAccount)
	archive := restoreTar(t, map[string]string{"config.yaml": "restored", "journal.db": "sqlite", "memorycore/tdai-gateway.yaml": "gateway"})
	if err := restorer.ApplySection(context.Background(), workspacebackup.SectionMLink, bytes.NewReader(archive)); err != nil {
		t.Fatal(err)
	}
	if err := restorer.RollbackRestore(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(restorer.paths.Config); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("rollback retained config: %v", err)
	}
}

func TestLocalWorkspaceRestorerRestoresIdentitySecretsAndSelectedAgents(t *testing.T) {
	restorer := restoreTestLocalRestorer(t)
	agents := &restoreTestAgents{}
	restorer.agents = agents
	restorer.selectedAgents = []app.Agent{app.Codex, app.Pi}
	manifest := restoreTestManifest()
	restorer.manifest = manifest
	configuration := restoreTestConfig(manifest)
	configData, err := yaml.Marshal(configuration)
	if err != nil {
		t.Fatal(err)
	}
	if err := restorer.ApplySection(context.Background(), workspacebackup.SectionMLink, bytes.NewReader(restoreTar(t, map[string]string{"config.yaml": string(configData), "journal.db": "sqlite", "memorycore/tdai-gateway.yaml": "gateway"}))); err != nil {
		t.Fatal(err)
	}
	secretSection := []byte(`{"gateway_token":"Z2F0ZXdheQ==","memory_llm_api_key":"bGxt","hermes_grant":"aGVybWVz","admin_user_key":"YWRtaW4=","owner_user_key":"b3duZXI="}`)
	if err := restorer.StageSection(context.Background(), workspacebackup.SectionSecrets, bytes.NewReader(secretSection)); err != nil {
		t.Fatal(err)
	}
	bundle := identity.BundleV1{
		SchemaVersion: 2,
		Principals:    map[string]config.Principal{"owner": configuration.Principals["owner"]},
		IdentityKey:   bytes.Repeat([]byte{0x2a}, 32), AdminUserKey: []byte("admin"), OwnerUserKey: []byte("owner"),
		Bindings: []identity.BindingExport{{Ref: configuration.Bindings["owner-feishu-union-1"], Value: []byte("on_owner")}},
		ControlPlane: &identity.BundleControlPlane{
			InstallationID: "personal", InstanceID: "default", OwnerUserID: manifest.ControlPlane.OwnerUserID,
			OwnerTeamID: manifest.ControlPlane.OwnerTeamID, OwnerAgentID: manifest.ControlPlane.OwnerAgentID, OwnerAssetID: manifest.ControlPlane.OwnerAssetID,
			PanelContainer: "tdai-memory-hub", PanelImage: "pinned", State: "active",
		},
		CreatedAt: time.Unix(1, 0).UTC(),
	}
	identitySection, err := identity.EncryptBundle(bundle, restorer.passphrase, bytes.NewReader(bytes.Repeat([]byte{0x44}, 64)))
	if err != nil {
		t.Fatal(err)
	}
	if err := restorer.ApplySection(context.Background(), workspacebackup.SectionIdentity, bytes.NewReader(identitySection)); err != nil {
		t.Fatal(err)
	}
	if err := restorer.ApplySection(context.Background(), workspacebackup.SectionSecrets, bytes.NewReader(secretSection)); err != nil {
		t.Fatal(err)
	}
	if err := restorer.ApplySection(context.Background(), workspacebackup.SectionAgents, strings.NewReader(`{"agents":["codex","pi","hermes"]}`)); err != nil {
		t.Fatal(err)
	}
	values := restorer.secrets.(*restoreTestSecrets).values
	for account, want := range map[string]string{
		"identity/hmac-key": string(bytes.Repeat([]byte{0x2a}, 32)), controlplane.AdminUserKeyAccount: "admin",
		controlplane.OwnerUserKeyAccount: "owner", "identity/binding/owner-feishu-union-1": "on_owner",
		"connection/local/token": "gateway", "provider/tencentdb/llm-api-key": "llm", "adapter/hermes/token": "hermes",
	} {
		if string(values[account]) != want {
			t.Fatalf("secret %s was not restored", account)
		}
	}
	if strings.Join(agentNames(agents.applied), ",") != "codex,pi" {
		t.Fatalf("restored Agents = %#v", agents.applied)
	}
	if err := restorer.VerifyRestore(context.Background(), manifest); err != nil || agents.verified != 1 {
		t.Fatalf("verify = %v/%d", err, agents.verified)
	}
}

func TestLocalWorkspaceRestorerRejectsIdentityRewrite(t *testing.T) {
	restorer := restoreTestLocalRestorer(t)
	manifest := restoreTestManifest()
	bundle := identity.BundleV1{
		SchemaVersion: 2, Principals: map[string]config.Principal{"owner": {ID: "owner", CanonicalUserID: "usr-other", Kind: config.PrincipalPerson}},
		IdentityKey: bytes.Repeat([]byte{0x2a}, 32), AdminUserKey: []byte("admin"), OwnerUserKey: []byte("owner"),
		ControlPlane: &identity.BundleControlPlane{InstallationID: "personal", InstanceID: "default", OwnerUserID: "usr-other", OwnerTeamID: "team", OwnerAgentID: "agent", OwnerAssetID: "asset", PanelContainer: "tdai-memory-hub", PanelImage: "pinned", State: "active"},
		CreatedAt:    time.Unix(1, 0).UTC(),
	}
	section, err := identity.EncryptBundle(bundle, restorer.passphrase, bytes.NewReader(bytes.Repeat([]byte{0x44}, 64)))
	if err != nil {
		t.Fatal(err)
	}
	restorer.manifest = manifest
	if err := restorer.ApplySection(context.Background(), workspacebackup.SectionIdentity, bytes.NewReader(section)); err == nil {
		t.Fatal("identity rewrite was accepted")
	}
	if len(restorer.secrets.(*restoreTestSecrets).values) != 0 {
		t.Fatal("identity mismatch wrote Keychain values")
	}
}

func TestWorkspaceLifecycleDiagnosticsReportVerifiedBackupAndRestore(t *testing.T) {
	restorer := restoreTestLocalRestorer(t)
	store, err := journal.Open(context.Background(), restorer.paths.Journal)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RecordWorkspaceBackupEvidence(context.Background(), journal.WorkspaceBackupEvidence{BundleFingerprint: "0123456789abcdef", Format: workspacebackup.FormatV1}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveRestoreOperation(context.Background(), journal.RestoreOperation{
		OperationID: "restore_0123456789ab", BundleFingerprint: "0123456789abcdef", PlanID: "plan_0123456789abcdef", Phase: journal.RestorePhaseComplete,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	runtime := &runtimeApplication{paths: restorer.paths}
	checks := runtime.checkWorkspaceLifecycle(context.Background())
	if len(checks) != 3 || checks[0].Code != "verified" || checks[1].Code != "complete" || checks[2].Code != "preserved" {
		t.Fatalf("workspace lifecycle checks = %#v", checks)
	}
}

func TestDeferredRestoreOperationStorePersistsRedactedCrashEvidenceUntilJournalExists(t *testing.T) {
	home := t.TempDir()
	paths, err := layout.FromHome(home, filepath.Join(home, "mlink"))
	if err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(paths.Run, "restore-operation.json")
	store := &deferredRestoreOperationStore{path: paths.Journal, statePath: statePath}
	operation := journal.RestoreOperation{
		OperationID: "restore_0123456789ab", BundleFingerprint: "0123456789abcdef", PlanID: "plan_0123456789abcdef", Phase: journal.RestorePhaseApplying,
	}
	if err := store.SaveRestoreOperation(context.Background(), operation); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(statePath)
	if err != nil || bytes.Contains(content, []byte("/Users/")) || bytes.Contains(content, []byte("secret")) {
		t.Fatalf("sidecar = %s/%v", content, err)
	}
	journalStore, err := journal.Open(context.Background(), paths.Journal)
	if err != nil {
		t.Fatal(err)
	}
	if err := journalStore.Close(); err != nil {
		t.Fatal(err)
	}
	operation.Phase = journal.RestorePhaseComplete
	if err := store.SaveRestoreOperation(context.Background(), operation); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(statePath); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("terminal restore retained sidecar: %v", err)
	}
}

func restoreTestLocalRestorer(t *testing.T) *localWorkspaceRestorer {
	t.Helper()
	home := t.TempDir()
	paths, err := layout.FromHome(home, filepath.Join(home, "source-mlink"))
	if err != nil {
		t.Fatal(err)
	}
	return &localWorkspaceRestorer{paths: paths, secrets: &restoreTestSecrets{values: map[string][]byte{}}, passphrase: []byte("correct-passphrase")}
}

func restoreTestManifest() workspacebackup.Manifest {
	return workspacebackup.Manifest{ControlPlane: workspacebackup.ControlPlaneManifest{
		InstallationID: "personal", InstanceID: "default", OwnerUserID: "usr-owner", OwnerTeamID: "team-owner",
		OwnerAgentID: "agt-owner", OwnerAssetID: "asset-owner", DynamicAgentLimit: 500,
	}}
}

func restoreTestConfig(manifest workspacebackup.Manifest) config.Config {
	return config.Config{
		SchemaVersion: 3, NamespaceID: "personal", ActiveConnectionID: "local",
		Connections: map[string]config.Connection{"local": {
			ID: "local", ProviderID: "dev.mlink.tencentdb", ProviderVersion: "0.1.0", ConfigRevision: "rev-1",
			ProviderConfig: map[string]any{"base_url": "http://127.0.0.1:8420", "service_id": "default", "timeout_ms": 5000},
			SecretRefs:     map[string]string{"token": "keychain://dev.mlink/connection/local/token"},
		}},
		Principals: map[string]config.Principal{"owner": {ID: "owner", CanonicalUserID: manifest.ControlPlane.OwnerUserID, Kind: config.PrincipalPerson}},
		Adapters:   map[string]config.Adapter{"codex": {ID: "codex", Enabled: true, SpaceID: "owner"}, "pi": {ID: "pi", Enabled: true, SpaceID: "owner"}},
		Bindings: map[string]config.BindingRef{"owner-feishu-union-1": {
			ID: "owner-feishu-union-1", Source: "feishu", Kind: "union_id", PrincipalID: "owner",
			SecretRef: "keychain://dev.mlink/identity/binding/owner-feishu-union-1", Status: config.BindingActive,
		}},
		ControlPlane: &config.ControlPlane{
			ProviderID: "dev.mlink.tencentdb", InstanceID: "default", PanelURL: "http://127.0.0.1:8125",
			OwnerUserID: manifest.ControlPlane.OwnerUserID, OwnerTeamID: manifest.ControlPlane.OwnerTeamID,
			OwnerAgentID: manifest.ControlPlane.OwnerAgentID, OwnerAssetID: manifest.ControlPlane.OwnerAssetID, DynamicAgentLimit: 500,
		},
		RoutingPolicies: map[string]config.RoutingPolicy{
			"owner":          {ID: "owner", Layers: []config.MemoryLayer{config.LayerL1, config.LayerL2, config.LayerL3}, AgentPolicy: config.AgentFixed},
			"hermes-private": {ID: "hermes-private", Layers: []config.MemoryLayer{config.LayerL1}, AgentPolicy: config.AgentDynamicPrincipal},
			"hermes-groups":  {ID: "hermes-groups", Layers: []config.MemoryLayer{config.LayerL1}, AgentPolicy: config.AgentDynamicGroup, SessionPolicy: config.SessionPerTopic},
		},
	}
}

func agentNames(values []app.Agent) []string {
	result := make([]string, len(values))
	for index, value := range values {
		result[index] = string(value)
	}
	return result
}

func restoreTar(t *testing.T, entries map[string]string) []byte {
	t.Helper()
	var output bytes.Buffer
	archive := tar.NewWriter(&output)
	for name, content := range entries {
		if err := archive.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := archive.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}
