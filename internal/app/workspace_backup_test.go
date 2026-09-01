package app

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"mlink/internal/controlplane"
	"mlink/internal/install"
	"mlink/internal/journal"
	"mlink/internal/layout"
	"mlink/internal/panel"
	"mlink/internal/provider/lifecycle"
	"mlink/internal/provider/tencentdb"
	"mlink/internal/workspacebackup"
)

type backupSnapshotDriver struct {
	target      install.Target
	streamCalls []workspacebackup.Section
	planCalls   int
}

func (driver *backupSnapshotDriver) Detect(context.Context) (lifecycle.SnapshotSource, error) {
	return lifecycle.SnapshotSource{
		ProviderID: "dev.mlink.tencentdb", DriverVersion: "1", InstanceID: "default", Owned: true,
		CoreContainer: tencentdb.MemoryCoreContainerName, HubContainer: panel.ContainerName,
		CoreImage: tencentdb.MemoryCoreImageReference, HubImage: panel.ImageReference, CoreRunning: true, HubRunning: true,
		Volumes: []lifecycle.SnapshotVolume{
			{Kind: workspacebackup.VolumeCore, Name: tencentdb.MemoryCoreVolumeName},
			{Kind: workspacebackup.VolumeKnowledge, Name: panel.VolumeName},
		},
	}, nil
}
func (driver *backupSnapshotDriver) PlanBackup(context.Context, lifecycle.BackupRequest) (install.ChangeSet, error) {
	driver.planCalls++
	return install.BuildChangeSet(driver.target, []install.DesiredResource{
		{OwnerID: "snapshot", Target: "service:stop:hub", Action: install.ActionService, Command: []string{"docker", "stop", "hub"}, RollbackCommand: []string{"docker", "start", "hub"}},
		{OwnerID: "snapshot", Target: "service:stop:core", Action: install.ActionService, Command: []string{"docker", "stop", "core"}, RollbackCommand: []string{"docker", "start", "core"}},
	})
}
func (driver *backupSnapshotDriver) StreamSection(_ context.Context, _ lifecycle.BackupRequest, section workspacebackup.Section, destination io.Writer) (workspacebackup.VolumeManifest, error) {
	driver.streamCalls = append(driver.streamCalls, section)
	_, _ = io.WriteString(destination, string(section)+"-tar")
	kind, name := workspacebackup.VolumeCore, tencentdb.MemoryCoreVolumeName
	if section == workspacebackup.SectionKnowledge {
		kind, name = workspacebackup.VolumeKnowledge, panel.VolumeName
	}
	return workspacebackup.VolumeManifest{Kind: kind, Name: name, LogicalBytes: 10, FileCount: 2, SHA256: strings.Repeat("a", 64)}, nil
}
func (*backupSnapshotDriver) PlanRestore(context.Context, lifecycle.RestoreRequest, workspacebackup.ProviderManifest) (install.ChangeSet, error) {
	return install.ChangeSet{}, errors.New("not used")
}
func (*backupSnapshotDriver) ApplySection(context.Context, lifecycle.RestoreRequest, workspacebackup.Section, io.Reader) error {
	return errors.New("not used")
}
func (*backupSnapshotDriver) VerifyRestore(context.Context, lifecycle.RestoreRequest, workspacebackup.Manifest) error {
	return errors.New("not used")
}

type backupPacker struct {
	calls    int
	fail     bool
	manifest workspacebackup.Manifest
	sections map[workspacebackup.Section][]byte
}

func (packer *backupPacker) Pack(ctx context.Context, output string, _ []byte, manifest *workspacebackup.Manifest, sources ...workspacebackup.SectionSource) error {
	packer.calls++
	packer.sections = map[workspacebackup.Section][]byte{}
	for _, source := range sources {
		reader, err := source.Open(ctx)
		if err != nil {
			return err
		}
		content, readErr := io.ReadAll(reader)
		_ = reader.Close()
		if readErr != nil {
			return readErr
		}
		packer.sections[source.Name] = content
	}
	packer.manifest = *manifest
	packer.manifest.Provider.Volumes = append([]workspacebackup.VolumeManifest(nil), manifest.Provider.Volumes...)
	if packer.fail {
		return errors.New("injected pack failure")
	}
	return os.WriteFile(output, []byte("encrypted-bundle"), 0o600)
}

func (*backupPacker) Open(context.Context, string, []byte, func(workspacebackup.Section, io.Reader) error) (workspacebackup.Manifest, error) {
	return workspacebackup.Manifest{}, errors.New("not used")
}

type backupStateArchiver struct{ calls int }

func (archiver *backupStateArchiver) Open(context.Context, layout.Paths) (io.ReadCloser, error) {
	archiver.calls++
	return io.NopCloser(strings.NewReader("mlink-state-tar")), nil
}

func TestWorkspaceBackupPreviewIsZeroWriteAndSecretIndependent(t *testing.T) {
	service, target, secrets := workspaceBackupFixture(t)
	output := filepath.Join(t.TempDir(), "workspace.mlink-backup")
	request := WorkspaceBackupRequest{OutputPath: output, Passphrase: []byte("first-passphrase")}
	beforePuts, beforeRuns, beforeWrites := secrets.puts, target.runs, target.writes
	first, err := service.PlanWorkspaceBackup(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	request.Passphrase = []byte("different-passphrase")
	second, err := service.PlanWorkspaceBackup(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if first.PlanID != second.PlanID || secrets.puts != beforePuts || target.runs != beforeRuns || target.writes != beforeWrites || service.WorkspacePacker.(*backupPacker).calls != 0 {
		t.Fatalf("preview side effects/identity = %s/%s puts=%d runs=%d writes=%d pack=%d", first.PlanID, second.PlanID, secrets.puts-beforePuts, target.runs-beforeRuns, target.writes-beforeWrites, service.WorkspacePacker.(*backupPacker).calls)
	}
	if _, err := os.Stat(output); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("preview created output: %v", err)
	}
	rendered, _ := install.RenderJSON(first)
	if bytes.Contains(rendered, []byte("first-passphrase")) || bytes.Contains(rendered, []byte(filepath.Dir(output))) {
		t.Fatalf("Plan leaked protected input/path: %s", rendered)
	}
}

func TestWorkspaceBackupApplyPacksAllSectionsAndRestartsServices(t *testing.T) {
	service, target, _ := workspaceBackupFixture(t)
	output := filepath.Join(t.TempDir(), "workspace.mlink-backup")
	request := WorkspaceBackupRequest{OutputPath: output, Passphrase: []byte("twelve-byte-passphrase")}
	plan, err := service.PlanWorkspaceBackup(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.ApplyWorkspaceBackup(context.Background(), plan.PlanID, request); err != nil {
		t.Fatal(err)
	}
	packer := service.WorkspacePacker.(*backupPacker)
	for _, section := range []workspacebackup.Section{workspacebackup.SectionCore, workspacebackup.SectionKnowledge, workspacebackup.SectionMLink, workspacebackup.SectionIdentity, workspacebackup.SectionSecrets, workspacebackup.SectionAgents} {
		if len(packer.sections[section]) == 0 {
			t.Fatalf("section %q is missing", section)
		}
	}
	if packer.manifest.ControlPlane.OwnerAgentID == "" || len(packer.manifest.Provider.Volumes) != 2 || target.runs != 6 {
		t.Fatalf("manifest/runs = %#v/%d", packer.manifest, target.runs)
	}
	if info, err := os.Stat(output); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("output info/error = %#v/%v", info, err)
	}
}

func TestWorkspaceBackupFailureRestartsOriginalServicesAndLeavesNoOutput(t *testing.T) {
	service, target, _ := workspaceBackupFixture(t)
	service.WorkspacePacker.(*backupPacker).fail = true
	output := filepath.Join(t.TempDir(), "workspace.mlink-backup")
	request := WorkspaceBackupRequest{OutputPath: output, Passphrase: []byte("twelve-byte-passphrase")}
	plan, _ := service.PlanWorkspaceBackup(context.Background(), request)
	if err := service.ApplyWorkspaceBackup(context.Background(), plan.PlanID, request); err == nil {
		t.Fatal("ApplyWorkspaceBackup() error = nil")
	}
	if target.runs != 6 {
		t.Fatalf("service run count = %d, want stop+restart for Broker/Hub/Core", target.runs)
	}
	if _, err := os.Stat(output); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed backup left output: %v", err)
	}
}

func TestWorkspaceBackupMissingSecretStillRestartsOriginalServices(t *testing.T) {
	service, target, secrets := workspaceBackupFixture(t)
	delete(secrets.values, "provider/tencentdb/llm-api-key")
	output := filepath.Join(t.TempDir(), "workspace.mlink-backup")
	request := WorkspaceBackupRequest{OutputPath: output, Passphrase: []byte("twelve-byte-passphrase")}
	plan, _ := service.PlanWorkspaceBackup(context.Background(), request)
	if err := service.ApplyWorkspaceBackup(context.Background(), plan.PlanID, request); err == nil {
		t.Fatal("missing backup secret was accepted")
	}
	if target.runs != 6 {
		t.Fatalf("service run count = %d, want stop+restart", target.runs)
	}
}

func TestWorkspaceBackupRequestFormattingRedactsPassphraseAndPath(t *testing.T) {
	request := WorkspaceBackupRequest{OutputPath: "/Users/private/workspace.mlink-backup", Passphrase: []byte("secret-passphrase")}
	rendered := request.String() + " " + request.GoString()
	if strings.Contains(rendered, "secret-passphrase") || strings.Contains(rendered, "/Users/private") || !strings.Contains(rendered, "<redacted>") || !strings.Contains(rendered, "workspace.mlink-backup") {
		t.Fatalf("request = %s", rendered)
	}
	request.Wipe()
	if len(request.Passphrase) != 0 {
		t.Fatal("passphrase was not wiped")
	}
}

func TestLocalWorkspaceArchiverStreamsOnlyExpectedRegularFiles(t *testing.T) {
	home := t.TempDir()
	paths, err := layout.FromHome(home, filepath.Join(home, "candidate"))
	if err != nil {
		t.Fatal(err)
	}
	for path, content := range map[string]string{
		paths.Config: "config", paths.Journal: "journal", paths.PanelRegistry: "registry",
		filepath.Join(paths.Home, "memorycore", "tdai-gateway.yaml"): "gateway-config",
		filepath.Join(paths.Backups, "plan", "file.json"):            "backup",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	reader, err := (LocalWorkspaceArchiver{}).Open(context.Background(), paths)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	archive := tar.NewReader(reader)
	var names []string
	for {
		header, err := archive.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, header.Name)
	}
	sort.Strings(names)
	want := []string{"backups/plan/file.json", "config.yaml", "journal.db", "memorycore/tdai-gateway.yaml", "panel/metadata-instances.json"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("archive names = %#v", names)
	}

	unsafe := filepath.Join(paths.Backups, "unsafe")
	if err := os.Symlink(paths.Config, unsafe); err != nil {
		t.Fatal(err)
	}
	reader, err = (LocalWorkspaceArchiver{}).Open(context.Background(), paths)
	if err != nil {
		t.Fatal(err)
	}
	_, err = io.Copy(io.Discard, reader)
	_ = reader.Close()
	if err == nil {
		t.Fatal("archive accepted symlink backup artifact")
	}
}

func workspaceBackupFixture(t *testing.T) (*Service, *memoryTarget, *memorySecrets) {
	t.Helper()
	service, target, secrets := installedIdentityFixture(t)
	secrets.values[controlplane.AdminUserKeyAccount] = []byte("admin-key")
	secrets.values[controlplane.OwnerUserKeyAccount] = []byte("owner-key")
	secrets.values["provider/tencentdb/llm-api-key"] = []byte("llm-key")
	service.RandomSource = bytes.NewReader(bytes.Repeat([]byte{0x42}, 128))
	state, err := service.ControlPlaneStates.LoadControlPlane(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	identityStore := &identityControlStore{state: state, mappings: map[string]journal.PrincipalAgent{}}
	service.ControlPlaneStates, service.PrincipalAgentStates = identityStore, identityStore
	driver := &backupSnapshotDriver{target: target}
	service.SnapshotDriver = driver
	service.WorkspacePacker = &backupPacker{}
	service.WorkspaceArchiver = &backupStateArchiver{}
	target.runs, target.writes = 0, 0
	return service, target, secrets
}
