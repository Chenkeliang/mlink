package journal

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestRecordInstallationAndBackup(t *testing.T) {
	store := openTestStore(t)
	now := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	installation := Installation{
		AdapterID:   "codex",
		AgentType:   "codex",
		Version:     "0.1.0",
		Status:      "active",
		Manifest:    json.RawMessage(`{"owned":["hook"]}`),
		InstalledAt: now,
	}
	if err := store.RecordInstallation(context.Background(), installation); err != nil {
		t.Fatal(err)
	}
	backup := Backup{
		BackupID:         "backup-1",
		Target:           "/tmp/config",
		BackupPath:       "/tmp/backups/config",
		BeforeHash:       "before",
		AfterHash:        "after",
		SemanticSnapshot: []byte("snapshot"),
		CreatedAt:        now,
	}
	if err := store.RecordBackup(context.Background(), backup); err != nil {
		t.Fatal(err)
	}
	var status string
	if err := store.db.QueryRow("SELECT status FROM adapter_installations WHERE adapter_id = ?", "codex").Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "active" {
		t.Fatalf("status = %q", status)
	}
	var path string
	if err := store.db.QueryRow("SELECT backup_path FROM backup_artifacts WHERE backup_id = ? AND target = ?", "backup-1", "/tmp/config").Scan(&path); err != nil {
		t.Fatal(err)
	}
	if path != backup.BackupPath {
		t.Fatalf("backup path = %q", path)
	}
}
