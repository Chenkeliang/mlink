package e2e

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"reflect"
	"sort"
	"testing"

	"mlink/internal/app"
	"mlink/internal/config"
	"mlink/internal/controlplane"
	"mlink/internal/install"
	"mlink/internal/journal"
	"mlink/internal/layout"
)

type file struct {
	content []byte
	mode    fs.FileMode
}

type target struct {
	files map[string]file
}

func (target *target) Read(_ context.Context, path string) ([]byte, fs.FileMode, error) {
	value, exists := target.files[path]
	if !exists {
		return nil, 0, fs.ErrNotExist
	}
	return append([]byte(nil), value.content...), value.mode, nil
}

func (target *target) WriteAtomic(_ context.Context, path string, content []byte, mode fs.FileMode) error {
	target.files[path] = file{content: append([]byte(nil), content...), mode: mode}
	return nil
}

func (target *target) Remove(_ context.Context, path string) error {
	delete(target.files, path)
	return nil
}

func (target *target) Run(context.Context, []string, io.Reader) ([]byte, error) { return nil, nil }

func (target *target) snapshot() map[string]file {
	result := make(map[string]file, len(target.files))
	for path, value := range target.files {
		result[path] = file{content: append([]byte(nil), value.content...), mode: value.mode}
	}
	return result
}

type ledger struct {
	backups map[string]install.Backup
	active  string
}

func (ledger *ledger) SaveBackup(_ context.Context, backup install.Backup) error {
	backup.Content = append([]byte(nil), backup.Content...)
	ledger.backups[backup.PlanID+"\x00"+backup.Target] = backup
	return nil
}

func (ledger *ledger) LoadBackup(_ context.Context, planID, path string) (install.Backup, error) {
	backup, exists := ledger.backups[planID+"\x00"+path]
	if !exists {
		return install.Backup{}, fs.ErrNotExist
	}
	backup.Content = append([]byte(nil), backup.Content...)
	return backup, nil
}

func (ledger *ledger) RecordOwned(context.Context, install.OwnedResource) error { return nil }

func (ledger *ledger) RecordInstallPlan(_ context.Context, planID string, _ []string) error {
	ledger.active = planID
	return nil
}

func (ledger *ledger) ActiveInstallPlan(context.Context) (string, error) {
	if ledger.active == "" {
		return "", fs.ErrNotExist
	}
	return ledger.active, nil
}

func (ledger *ledger) MarkInstallRemoved(context.Context) error {
	ledger.active = ""
	return nil
}

func (ledger *ledger) ListBackups(_ context.Context, planID string) ([]install.Backup, error) {
	var result []install.Backup
	for key, backup := range ledger.backups {
		if len(key) > len(planID) && key[:len(planID)+1] == planID+"\x00" {
			backup.Content = append([]byte(nil), backup.Content...)
			result = append(result, backup)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Target < result[j].Target })
	return result, nil
}

type secrets map[string][]byte

func (store secrets) Put(_ context.Context, account string, value []byte) error {
	store[account] = append([]byte(nil), value...)
	return nil
}

type e2eControlStore struct {
	state    journal.ControlPlaneState
	mappings map[string]journal.PrincipalAgent
}

func (store *e2eControlStore) LoadControlPlane(context.Context) (journal.ControlPlaneState, error) {
	if store.state.State == "" {
		return journal.ControlPlaneState{}, fs.ErrNotExist
	}
	return store.state, nil
}
func (store *e2eControlStore) MarkControlPlaneState(_ context.Context, state string) error {
	store.state.State = state
	return nil
}
func (store *e2eControlStore) GetPrincipalAgent(_ context.Context, fingerprint string) (journal.PrincipalAgent, error) {
	value, ok := store.mappings[fingerprint]
	if !ok {
		return journal.PrincipalAgent{}, fs.ErrNotExist
	}
	return value, nil
}
func (store *e2eControlStore) PutPrincipalAgent(_ context.Context, value journal.PrincipalAgent) error {
	store.mappings[value.Fingerprint] = value
	return nil
}
func (store *e2eControlStore) ListPrincipalAgents(context.Context) ([]journal.PrincipalAgent, error) {
	result := make([]journal.PrincipalAgent, 0, len(store.mappings))
	for _, value := range store.mappings {
		result = append(result, value)
	}
	return result, nil
}

func (store secrets) Get(_ context.Context, account string) ([]byte, error) {
	value, exists := store[account]
	if !exists {
		return nil, fs.ErrNotExist
	}
	return append([]byte(nil), value...), nil
}

func (store secrets) Delete(_ context.Context, account string) error {
	delete(store, account)
	return nil
}

func TestInstallAndUninstallRoundTripPreservesOriginalFiles(t *testing.T) {
	service, target, request := fixture(t)
	before := target.snapshot()
	plan, err := service.PlanInstall(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, target.snapshot()) {
		t.Fatal("installation preview changed target files")
	}
	if err := service.ApplyInstall(context.Background(), plan.PlanID, request); err != nil {
		t.Fatal(err)
	}
	if _, exists := target.files[service.Paths.Config]; !exists {
		t.Fatal("MLink config was not installed")
	}
	uninstall, err := service.PlanUninstall(context.Background(), app.UninstallRequest{Agents: []app.Agent{app.Codex, app.Pi, app.Hermes}})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.ApplyUninstall(context.Background(), uninstall.PlanID, app.UninstallRequest{Agents: []app.Agent{app.Codex, app.Pi, app.Hermes}}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, target.snapshot()) {
		t.Fatalf("uninstall did not restore the exact original file set\nbefore=%#v\nafter=%#v", before, target.snapshot())
	}
}

func TestStalePreviewCannotApply(t *testing.T) {
	service, target, request := fixture(t)
	plan, err := service.PlanInstall(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	path := "/Users/test/.codex/hooks.json"
	target.files[path] = file{content: []byte(`{"user_edit":true}`), mode: 0o600}
	if err := service.ApplyInstall(context.Background(), plan.PlanID, request); !errors.Is(err, install.ErrPlanStale) {
		t.Fatalf("error = %v", err)
	}
}

func TestUninstallRefusesChangedOwnedPiExtension(t *testing.T) {
	service, target, request := fixture(t)
	plan, err := service.PlanInstall(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.ApplyInstall(context.Background(), plan.PlanID, request); err != nil {
		t.Fatal(err)
	}
	path := "/Users/test/.pi/agent/extensions/mlink.ts"
	target.files[path] = file{content: []byte("user changed owned file"), mode: 0o600}
	_, err = service.PlanUninstall(context.Background(), app.UninstallRequest{Agents: []app.Agent{app.Codex, app.Pi, app.Hermes}})
	if !errors.Is(err, app.ErrOwnedResourceChanged) {
		t.Fatalf("error = %v", err)
	}
}

func fixture(t *testing.T) (*app.Service, *target, app.InstallRequest) {
	t.Helper()
	paths, err := layout.FromHome("/Users/test", "/tmp/mlink-candidate")
	if err != nil {
		t.Fatal(err)
	}
	target := &target{files: map[string]file{
		paths.SourceExecutable:          {content: []byte("candidate-binary"), mode: 0o700},
		"/Users/test/.codex/hooks.json": {content: []byte("{\n  \"custom\": true\n}\n"), mode: 0o600},
		"/home/test/.hermes/config.yaml": {
			content: []byte("group_sessions_per_user: true\nmodel:\n  provider: subscription\n  base_url: https://models.example.invalid\nmemory:\n  memory_enabled: true\n  user_profile_enabled: true\n  provider: hy-memory\nagent:\n  disabled_toolsets:\n    - browser\n"),
			mode:    0o600,
		},
	}}
	ledger := &ledger{backups: make(map[string]install.Backup)}
	secretStore := secrets{
		controlplane.AdminUserKeyAccount: []byte("admin-key"),
		controlplane.OwnerUserKeyAccount: []byte("owner-key"),
	}
	controlStore := &e2eControlStore{state: journal.ControlPlaneState{
		InstallationID: "personal", InstanceID: "default", OwnerUserID: "usr-owner-generated", OwnerTeamID: "team-owner-generated",
		OwnerAgentID: "agt-owner-generated", OwnerAssetID: "chat_memory-team-owner-generated-agt-owner-generated", State: "provisioned",
	}, mappings: map[string]journal.PrincipalAgent{}}
	service := &app.Service{
		Paths: paths, UID: 501, Target: target, Ledger: ledger, Secrets: secretStore,
		HermesEndpoint: "http://192.168.139.3:8097", HermesListenAddress: "192.168.139.3:8097",
		HermesGrantToken: []byte("hermes-grant"), IdentityKey: bytes.Repeat([]byte{0x2a}, 32),
		ControlPlaneStates: controlStore, PrincipalAgentStates: controlStore,
	}
	request := app.InstallRequest{
		Agents: []app.Agent{app.Codex, app.Pi, app.Hermes},
		Connection: config.Connection{
			ID: "local", ProviderID: "dev.mlink.tencentdb", ProviderVersion: "0.1.0", ConfigRevision: "rev-local-1",
			ProviderConfig: map[string]any{"base_url": "http://127.0.0.1:8420", "service_id": "default", "timeout_ms": 5000},
			TenantID:       "personal", AgentID: "default", UserID: "local-user",
		},
		SecretInputs: map[string][]byte{app.MemoryCoreTokenSecret: []byte("memorycore-token"), app.OwnerBindingSecret: []byte("on_owner")},
		OwnerSlug:    "keliang",
		OwnerBindingSlot: config.BindingRef{
			ID: "owner-feishu-union-1", Source: "feishu", Kind: "union_id", PrincipalID: "owner",
			SecretRef: "keychain://dev.mlink/identity/binding/owner-feishu-union-1", Status: config.BindingActive,
		},
		HermesMachine: "hermes-agent-env", HermesHome: "/home/test/.hermes",
		DynamicAgentLimit: 500,
	}
	return service, target, request
}
