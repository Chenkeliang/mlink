package journal

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestCheckpointAndVerifyFlushesJournalWAL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "journal.db")
	store, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	operation := RestoreOperation{OperationID: "restore_0123456789ab", BundleFingerprint: "0123456789abcdef", PlanID: "plan_0123456789abcdef", Phase: RestorePhasePlanned}
	if err := store.SaveRestoreOperation(context.Background(), operation); err != nil {
		t.Fatal(err)
	}
	if err := CheckpointAndVerify(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(path + "-wal"); err == nil && info.Size() != 0 {
		t.Fatalf("Journal WAL size after checkpoint = %d", info.Size())
	}
}
