package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"runtime"
	"strings"
	"testing"
	"time"

	"mlink/internal/install"
	"mlink/internal/journal"
	"mlink/internal/panel"
	"mlink/internal/provider/lifecycle"
	"mlink/internal/provider/tencentdb"
	"mlink/internal/version"
	"mlink/internal/workspacebackup"
)

type restoreBundle struct {
	manifest workspacebackup.Manifest
	sections map[workspacebackup.Section]string
	err      error
	opens    int
}

func (*restoreBundle) Pack(context.Context, string, []byte, *workspacebackup.Manifest, ...workspacebackup.SectionSource) error {
	return errors.New("not used")
}
func (bundle *restoreBundle) Open(_ context.Context, _ string, _ []byte, visitor func(workspacebackup.Section, io.Reader) error) (workspacebackup.Manifest, error) {
	bundle.opens++
	if bundle.err != nil {
		return workspacebackup.Manifest{}, bundle.err
	}
	for _, section := range []workspacebackup.Section{workspacebackup.SectionMLink, workspacebackup.SectionIdentity, workspacebackup.SectionSecrets, workspacebackup.SectionAgents, workspacebackup.SectionCore, workspacebackup.SectionKnowledge} {
		if content, exists := bundle.sections[section]; exists {
			if err := visitor(section, strings.NewReader(content)); err != nil {
				return workspacebackup.Manifest{}, err
			}
		}
	}
	return bundle.manifest, nil
}

type restoreSnapshotDriver struct {
	target    install.Target
	applied   []workspacebackup.Section
	verified  int
	verifyErr error
	events    *[]string
}

func (*restoreSnapshotDriver) Detect(context.Context) (lifecycle.SnapshotSource, error) {
	return lifecycle.SnapshotSource{}, nil
}
func (*restoreSnapshotDriver) PlanBackup(context.Context, lifecycle.BackupRequest) (install.ChangeSet, error) {
	return install.ChangeSet{}, errors.New("not used")
}
func (*restoreSnapshotDriver) StreamSection(context.Context, lifecycle.BackupRequest, workspacebackup.Section, io.Writer) (workspacebackup.VolumeManifest, error) {
	return workspacebackup.VolumeManifest{}, errors.New("not used")
}
func (driver *restoreSnapshotDriver) PlanRestore(context.Context, lifecycle.RestoreRequest, workspacebackup.ProviderManifest) (install.ChangeSet, error) {
	return install.BuildChangeSet(driver.target, []install.DesiredResource{{OwnerID: "restore", Target: "service:create:volumes", Action: install.ActionService, Command: []string{"create", "volumes"}, RollbackCommand: []string{"remove", "volumes"}}})
}
func (driver *restoreSnapshotDriver) ApplySection(_ context.Context, _ lifecycle.RestoreRequest, section workspacebackup.Section, _ io.Reader) error {
	driver.applied = append(driver.applied, section)
	if driver.events != nil {
		*driver.events = append(*driver.events, "provider:"+string(section))
	}
	return nil
}
func (driver *restoreSnapshotDriver) VerifyRestore(context.Context, lifecycle.RestoreRequest, workspacebackup.Manifest) error {
	driver.verified++
	if driver.events != nil {
		*driver.events = append(*driver.events, "provider:verify")
	}
	return driver.verifyErr
}

type restoreLocal struct {
	target      install.Target
	applied     []workspacebackup.Section
	verified    int
	rollbacks   int
	provider    lifecycle.RestoreRequest
	failSection workspacebackup.Section
	events      *[]string
}

type restoreOperationStore struct{ values []journal.RestoreOperation }

func (store *restoreOperationStore) SaveRestoreOperation(_ context.Context, value journal.RestoreOperation) error {
	store.values = append(store.values, value)
	return nil
}

func (local *restoreLocal) PlanRestore(context.Context, WorkspaceRestoreRequest, workspacebackup.Manifest) (install.ChangeSet, error) {
	return install.BuildChangeSet(local.target, []install.DesiredResource{{OwnerID: "restore", Target: "service:restore:local", Action: install.ActionService, Command: []string{"restore", "local"}, RollbackCommand: []string{"rollback", "local"}}})
}
func (local *restoreLocal) StageSection(_ context.Context, section workspacebackup.Section, _ io.Reader) error {
	if local.events != nil {
		*local.events = append(*local.events, "local:stage-"+string(section))
	}
	return nil
}
func (local *restoreLocal) ApplySection(_ context.Context, section workspacebackup.Section, _ io.Reader) error {
	local.applied = append(local.applied, section)
	if local.events != nil {
		*local.events = append(*local.events, "local:"+string(section))
	}
	if section == local.failSection {
		return errors.New("injected local restore failure")
	}
	return nil
}
func (local *restoreLocal) ProviderRequest(context.Context, workspacebackup.Manifest) (lifecycle.RestoreRequest, error) {
	return local.provider, nil
}
func (local *restoreLocal) VerifyRestore(context.Context, workspacebackup.Manifest) error {
	local.verified++
	if local.events != nil {
		*local.events = append(*local.events, "local:verify")
	}
	return nil
}
func (local *restoreLocal) RollbackRestore(context.Context) error { local.rollbacks++; return nil }

func TestWorkspaceRestorePreviewIsZeroWriteAndSecretIndependent(t *testing.T) {
	service, target, bundle, _, _ := workspaceRestoreFixture(t)
	request := WorkspaceRestoreRequest{BundlePath: "/tmp/workspace.mlink-backup", Passphrase: []byte("first-passphrase"), SelectedAgents: []Agent{Codex, Hermes}}
	first, err := service.PlanWorkspaceRestore(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	request.Passphrase = []byte("other-passphrase")
	second, err := service.PlanWorkspaceRestore(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if first.PlanID != second.PlanID || target.runs != 0 || target.writes != 0 || bundle.opens != 2 {
		t.Fatalf("preview = %s/%s runs=%d writes=%d opens=%d", first.PlanID, second.PlanID, target.runs, target.writes, bundle.opens)
	}
	rendered, _ := install.RenderJSON(first)
	if strings.Contains(string(rendered), "first-passphrase") || strings.Contains(string(rendered), "/tmp/workspace") {
		t.Fatalf("Plan leaked protected input: %s", rendered)
	}
}

func TestWorkspaceRestoreDefaultsToAgentsRecordedInBundle(t *testing.T) {
	service, _, bundle, _, _ := workspaceRestoreFixture(t)
	bundle.manifest.Agents = []string{"codex", "pi"}
	request := WorkspaceRestoreRequest{BundlePath: "/tmp/workspace.mlink-backup", Passphrase: []byte("twelve-byte-passphrase")}
	plan, err := service.PlanWorkspaceRestore(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	rendered, _ := install.RenderJSON(plan)
	if !strings.Contains(string(rendered), "codex,pi") {
		t.Fatalf("bundle Agent selection missing from Plan: %s", rendered)
	}
}

func TestWorkspaceRestoreAppliesSectionsThenVerifiesIdentity(t *testing.T) {
	service, target, _, snapshot, local := workspaceRestoreFixture(t)
	operations := &restoreOperationStore{}
	service.RestoreOperations = operations
	var events []string
	snapshot.events, local.events = &events, &events
	request := WorkspaceRestoreRequest{BundlePath: "/tmp/workspace.mlink-backup", Passphrase: []byte("twelve-byte-passphrase"), SelectedAgents: []Agent{Codex, Cursor}}
	plan, _ := service.PlanWorkspaceRestore(context.Background(), request)
	if err := service.ApplyWorkspaceRestore(context.Background(), plan.PlanID, request); err != nil {
		t.Fatal(err)
	}
	if strings.Join(sectionStrings(snapshot.applied), ",") != "core,knowledge" || len(local.applied) != 4 || snapshot.verified != 1 || local.verified != 1 || local.rollbacks != 0 {
		t.Fatalf("snapshot/local = %#v/%#v verifies=%d/%d rollbacks=%d", snapshot.applied, local.applied, snapshot.verified, local.verified, local.rollbacks)
	}
	if target.runs != 2 {
		t.Fatalf("transaction service runs = %d", target.runs)
	}
	want := "local:stage-secrets,provider:core,provider:knowledge,provider:verify,local:mlink,local:identity,local:secrets,local:agents,local:verify"
	if got := strings.Join(events, ","); got != want {
		t.Fatalf("restore order = %s, want %s", got, want)
	}
	var phases []string
	for _, operation := range operations.values {
		phases = append(phases, string(operation.Phase))
	}
	if strings.Join(phases, ",") != "planned,applying,verifying,installing_agents,complete" {
		t.Fatalf("restore phases = %v", phases)
	}
}

func TestWorkspaceRestoreFailureRollsBackOnlyCreatedResources(t *testing.T) {
	service, target, _, snapshot, local := workspaceRestoreFixture(t)
	operations := &restoreOperationStore{}
	service.RestoreOperations = operations
	snapshot.verifyErr = errors.New("fixed ID mismatch")
	request := WorkspaceRestoreRequest{BundlePath: "/tmp/workspace.mlink-backup", Passphrase: []byte("twelve-byte-passphrase"), SelectedAgents: []Agent{Codex}}
	plan, _ := service.PlanWorkspaceRestore(context.Background(), request)
	if err := service.ApplyWorkspaceRestore(context.Background(), plan.PlanID, request); err == nil {
		t.Fatal("restore mismatch was accepted")
	}
	if local.rollbacks != 1 || target.runs != 4 {
		t.Fatalf("rollback/runs = %d/%d", local.rollbacks, target.runs)
	}
	if got := operations.values[len(operations.values)-1].Phase; got != journal.RestorePhaseRolledBack {
		t.Fatalf("final restore phase = %s", got)
	}
}

func TestWorkspaceRestoreRejectsWrongPassphraseAndIncompatibleManifest(t *testing.T) {
	service, target, bundle, _, _ := workspaceRestoreFixture(t)
	bundle.err = workspacebackup.ErrAuthentication
	request := WorkspaceRestoreRequest{BundlePath: "/tmp/workspace.mlink-backup", Passphrase: []byte("wrong-passphrase"), SelectedAgents: []Agent{Codex}}
	if _, err := service.PlanWorkspaceRestore(context.Background(), request); !errors.Is(err, workspacebackup.ErrAuthentication) {
		t.Fatalf("error = %v", err)
	}
	if target.runs+target.writes != 0 {
		t.Fatal("wrong passphrase mutated target")
	}
	bundle.err = nil
	bundle.manifest.MLink.GOARCH = "amd64"
	if _, err := service.PlanWorkspaceRestore(context.Background(), request); err == nil || !strings.Contains(err.Error(), "platform") {
		t.Fatalf("platform error = %v", err)
	}
}

func TestWorkspaceRestoreRequestFormattingRedactsPassphraseAndPath(t *testing.T) {
	request := WorkspaceRestoreRequest{BundlePath: "/Users/private/workspace.mlink-backup", Passphrase: []byte("secret-passphrase"), SelectedAgents: []Agent{Codex}}
	rendered := fmt.Sprintf("%s %#v", request, request)
	if strings.Contains(rendered, "secret-passphrase") || strings.Contains(rendered, "/Users/private") || !strings.Contains(rendered, "<redacted>") {
		t.Fatalf("unsafe request = %s", rendered)
	}
	request.Wipe()
	if len(request.Passphrase) != 0 {
		t.Fatal("passphrase not wiped")
	}
}

func workspaceRestoreFixture(t *testing.T) (*Service, *memoryTarget, *restoreBundle, *restoreSnapshotDriver, *restoreLocal) {
	t.Helper()
	target := newMemoryTarget(nil)
	bundle := &restoreBundle{manifest: fixtureRestoreManifest(), sections: map[workspacebackup.Section]string{
		workspacebackup.SectionCore: "core", workspacebackup.SectionKnowledge: "knowledge", workspacebackup.SectionMLink: "mlink",
		workspacebackup.SectionIdentity: "identity", workspacebackup.SectionSecrets: "secrets", workspacebackup.SectionAgents: "agents",
	}}
	snapshot := &restoreSnapshotDriver{target: target}
	local := &restoreLocal{target: target, provider: lifecycle.RestoreRequest{ProviderID: "dev.mlink.tencentdb", CoreVolume: "core", KnowledgeVolume: "knowledge", OwnerUserKey: []byte("owner")}}
	service := &Service{Target: target, Ledger: newMemoryLedger(), WorkspacePacker: bundle, SnapshotDriver: snapshot, WorkspaceRestorer: local}
	return service, target, bundle, snapshot, local
}

func fixtureRestoreManifest() workspacebackup.Manifest {
	manifest := fixtureManifestForWorkspaceRestore()
	return manifest
}

func fixtureManifestForWorkspaceRestore() workspacebackup.Manifest {
	return workspacebackup.Manifest{
		Format: workspacebackup.FormatV1, CreatedAt: time.Unix(1, 0).UTC(),
		MLink:        version.Info{Version: "0.1.0", GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, SchemaMin: 2, SchemaMax: 3},
		Provider:     workspacebackup.ProviderManifest{ProviderID: "dev.mlink.tencentdb", DriverVersion: "1", InstanceID: "default", CoreImageDigest: imageDigest(tencentdb.MemoryCoreImageReference), HubImageDigest: imageDigest(panel.ImageReference)},
		ControlPlane: workspacebackup.ControlPlaneManifest{InstallationID: "personal", InstanceID: "default", OwnerUserID: "usr", OwnerTeamID: "team", OwnerAgentID: "agent", OwnerAssetID: "asset", DynamicAgentLimit: 500},
	}
}

func sectionStrings(values []workspacebackup.Section) []string {
	result := make([]string, len(values))
	for i, v := range values {
		result[i] = string(v)
	}
	return result
}
