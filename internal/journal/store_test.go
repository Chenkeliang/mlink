package journal

import (
	"context"
	"path/filepath"
	"testing"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(context.Background(), filepath.Join(t.TempDir(), "journal.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})
	return store
}

func TestOpenAppliesMigrationAndEnablesWAL(t *testing.T) {
	store := openTestStore(t)
	var version int
	if err := store.db.QueryRow("SELECT max(version) FROM schema_migrations").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 4 {
		t.Fatalf("migration version = %d, want 4", version)
	}
	var mode string
	if err := store.db.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != "wal" {
		t.Fatalf("journal mode = %q, want wal", mode)
	}
}
