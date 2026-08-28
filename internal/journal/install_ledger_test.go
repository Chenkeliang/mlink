package journal

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"mlink/internal/install"
)

func TestInstallationLedgerPersistsAndListsVerifiedBackup(t *testing.T) {
	store := openTestStore(t)
	directory := filepath.Join(t.TempDir(), "backups")
	ledger, err := NewInstallationLedger(store, directory)
	if err != nil {
		t.Fatal(err)
	}
	want := install.Backup{
		PlanID:       "plan_0123456789",
		OperationID:  "op_0123456789",
		Target:       "/Users/test/.codex/hooks.json",
		Content:      []byte("before"),
		Mode:         0o640,
		Existed:      true,
		BeforeHash:   "before-hash",
		ProposedHash: "after-hash",
	}
	if err := ledger.SaveBackup(context.Background(), want); err != nil {
		t.Fatal(err)
	}
	got, err := ledger.LoadBackup(context.Background(), want.PlanID, want.Target)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("backup = %#v, want %#v", got, want)
	}
	listed, err := ledger.ListBackups(context.Background(), want.PlanID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(listed, []install.Backup{want}) {
		t.Fatalf("listed = %#v", listed)
	}
	summaries, err := ledger.ListBackupSummaries(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(summaries) != 1 || summaries[0].BackupID != want.PlanID || summaries[0].Resources != 1 {
		t.Fatalf("summaries = %#v", summaries)
	}

	var backupPath string
	if err := store.db.QueryRowContext(context.Background(), `
		SELECT backup_path FROM backup_artifacts WHERE backup_id = ? AND target = ?`, want.PlanID, want.Target).Scan(&backupPath); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(backupPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("backup mode = %o", info.Mode().Perm())
	}
	if filepath.Base(backupPath) == filepath.Base(want.Target) {
		t.Fatal("backup filename exposes target basename")
	}
}

func TestInstallationLedgerPersistsOwnedResource(t *testing.T) {
	store := openTestStore(t)
	ledger, err := NewInstallationLedger(store, filepath.Join(t.TempDir(), "backups"))
	if err != nil {
		t.Fatal(err)
	}
	want := install.OwnedResource{
		OwnerID: "dev.mlink.adapter.codex", Target: "/Users/test/.codex/hooks.json",
		SemanticFingerprint: "semantic", PostApplyHash: "after",
	}
	if err := ledger.RecordOwned(context.Background(), want); err != nil {
		t.Fatal(err)
	}
	got, err := ledger.ListOwned(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []install.OwnedResource{want}) {
		t.Fatalf("owned = %#v", got)
	}
}

func TestInstallationLedgerRejectsUnsafePlanID(t *testing.T) {
	store := openTestStore(t)
	ledger, err := NewInstallationLedger(store, filepath.Join(t.TempDir(), "backups"))
	if err != nil {
		t.Fatal(err)
	}
	err = ledger.SaveBackup(context.Background(), install.Backup{
		PlanID: "../../escape", OperationID: "op", Target: "/target", BeforeHash: "before", ProposedHash: "after",
	})
	if err == nil {
		t.Fatal("SaveBackup() error = nil")
	}
}

func TestInstallationLedgerRecordsActiveInstallPlan(t *testing.T) {
	store := openTestStore(t)
	ledger, err := NewInstallationLedger(store, filepath.Join(t.TempDir(), "backups"))
	if err != nil {
		t.Fatal(err)
	}
	if err := ledger.RecordInstallPlan(context.Background(), "plan_active", []string{"codex", "pi", "hermes"}); err != nil {
		t.Fatal(err)
	}
	planID, err := ledger.ActiveInstallPlan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if planID != "plan_active" {
		t.Fatalf("active plan = %q", planID)
	}
	if err := ledger.MarkInstallRemoved(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := ledger.ActiveInstallPlan(context.Background()); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("ActiveInstallPlan() error = %v", err)
	}
}
