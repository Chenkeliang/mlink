package journal

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestQueueSummaryCountsDiagnosticStates(t *testing.T) {
	store := openTestStore(t)
	queued, _, err := store.EnqueueTurn(context.Background(), fixtureEnvelope("user", "turn-queue", "queued"))
	if err != nil {
		t.Fatal(err)
	}
	failed, _, err := store.EnqueueTurn(context.Background(), fixtureEnvelope("user", "turn-failed", "failed"))
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := store.ClaimReady(context.Background(), time.Now().UTC(), 2)
	if err != nil || len(claimed) != 2 {
		t.Fatalf("claim = %d, %v", len(claimed), err)
	}
	if err := store.MarkRetryable(context.Background(), queued.ID, "timeout", time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkAmbiguous(context.Background(), failed.ID, "response_lost"); err != nil {
		t.Fatal(err)
	}
	summary, err := store.QueueSummary(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if summary.Retrying != 1 || summary.Ambiguous != 1 || summary.Permanent != 0 {
		t.Fatalf("summary = %#v", summary)
	}
}

func TestLatestAdapterActivityReturnsNotExistWhenUnused(t *testing.T) {
	store := openTestStore(t)
	if _, err := store.LatestAdapterActivity(context.Background(), "codex"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("error = %v", err)
	}
}

func TestOpenReadOnlyDoesNotCreateMissingJournal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "state.db")
	if _, err := OpenReadOnly(context.Background(), path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("error = %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("missing journal was created")
	}
}
