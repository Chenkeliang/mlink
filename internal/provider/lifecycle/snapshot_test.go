package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"mlink/internal/install"
	"mlink/internal/workspacebackup"
)

type fakeSnapshotDriver struct{ source SnapshotSource }

func (driver *fakeSnapshotDriver) Detect(context.Context) (SnapshotSource, error) {
	return driver.source.Clone(), nil
}
func (*fakeSnapshotDriver) PlanBackup(context.Context, BackupRequest) (install.ChangeSet, error) {
	return install.ChangeSet{PlanID: "plan_backup"}, nil
}
func (*fakeSnapshotDriver) StreamSection(context.Context, BackupRequest, workspacebackup.Section, io.Writer) (workspacebackup.VolumeManifest, error) {
	return workspacebackup.VolumeManifest{}, nil
}
func (*fakeSnapshotDriver) PlanRestore(context.Context, RestoreRequest, workspacebackup.ProviderManifest) (install.ChangeSet, error) {
	return install.ChangeSet{PlanID: "plan_restore"}, nil
}
func (*fakeSnapshotDriver) ApplySection(context.Context, RestoreRequest, workspacebackup.Section, io.Reader) error {
	return nil
}
func (*fakeSnapshotDriver) VerifyRestore(context.Context, RestoreRequest, workspacebackup.Manifest) error {
	return nil
}

func TestSnapshotRegistryRequiresUniqueExactProviderIDs(t *testing.T) {
	driver := &fakeSnapshotDriver{source: SnapshotSource{ProviderID: "dev.mlink.tencentdb", DriverVersion: "1"}}
	registry, err := NewSnapshotRegistry(SnapshotRegistration{ProviderID: "dev.mlink.tencentdb", Driver: driver})
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := registry.Get("dev.mlink.tencentdb")
	if err != nil || resolved != driver {
		t.Fatalf("resolved/error = %#v/%v", resolved, err)
	}
	if _, err := registry.Get("DEV.MLINK.TENCENTDB"); !errors.Is(err, ErrSnapshotUnavailable) {
		t.Fatalf("case-insensitive lookup error = %v", err)
	}
	if _, err := NewSnapshotRegistry(
		SnapshotRegistration{ProviderID: "dev.mlink.tencentdb", Driver: driver},
		SnapshotRegistration{ProviderID: "dev.mlink.tencentdb", Driver: driver},
	); err == nil {
		t.Fatal("duplicate Snapshot registration accepted")
	}
}

func TestSnapshotRequestsNeverFormatSecretsOrFullPaths(t *testing.T) {
	restore := RestoreRequest{
		ProviderID: "dev.mlink.tencentdb", BundlePath: "/Users/private/secret-workspace.mlink-backup",
		GatewayToken: []byte("gateway-secret"), LLMAPIKey: []byte("llm-secret"), OwnerUserKey: []byte("owner-secret"),
		CoreContainer: "tdai-memory-core", CoreVolume: "tdai-memory-core-data", KnowledgeVolume: "tdai-panel-data",
	}
	backup := BackupRequest{ProviderID: "dev.mlink.tencentdb", ApproveExternalSource: true}
	formatted := fmt.Sprintf("%s %#v %s %#v", restore, restore, backup, backup)
	for _, forbidden := range []string{"gateway-secret", "llm-secret", "owner-secret", "/Users/private"} {
		if strings.Contains(formatted, forbidden) {
			t.Fatalf("request formatting leaked %q: %s", forbidden, formatted)
		}
	}
	if !strings.Contains(formatted, "<redacted>") || !strings.Contains(formatted, "secret-workspace.mlink-backup") {
		t.Fatalf("request formatting = %s", formatted)
	}
	restore.Wipe()
	if len(restore.GatewayToken)+len(restore.LLMAPIKey)+len(restore.OwnerUserKey) != 0 {
		t.Fatalf("restore secrets were not wiped: %#v", restore)
	}
}

func TestSnapshotSourceCloneIsIndependent(t *testing.T) {
	first := SnapshotSource{
		ProviderID: "dev.mlink.tencentdb", DriverVersion: "1", InstanceID: "default", Owned: true,
		Volumes: []SnapshotVolume{{Kind: workspacebackup.VolumeCore, Name: "core", LogicalBytes: 10, FileCount: 2}},
	}
	second := first.Clone()
	second.Volumes[0].Name = "changed"
	if first.Volumes[0].Name != "core" {
		t.Fatalf("SnapshotSource clone aliased volume: %#v", first)
	}
}
