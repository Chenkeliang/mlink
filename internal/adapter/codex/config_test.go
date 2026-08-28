package codex

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestPlanHooksPreservesExistingEventsAndIsIdempotent(t *testing.T) {
	existing := readFixture(t, "existing-hooks.json")
	first, err := PlanHooks(existing, "/Users/test/.local/bin/mlink")
	if err != nil {
		t.Fatal(err)
	}
	second, err := PlanHooks(first, "/Users/test/.local/bin/mlink")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("second install changed hooks")
	}
	var document struct {
		Hooks map[string][]json.RawMessage `json:"hooks"`
	}
	if err := json.Unmarshal(second, &document); err != nil {
		t.Fatal(err)
	}
	if len(document.Hooks["PreToolUse"]) != 1 {
		t.Fatal("existing PreToolUse hook was not preserved")
	}
	for _, event := range []string{"SessionStart", "UserPromptSubmit", "Stop", "SessionEnd"} {
		if countOwnedHooks(document.Hooks[event], event) != 1 {
			t.Fatalf("MLink hook count for %s is not 1", event)
		}
	}
	if !bytes.Contains(second, []byte(`"matcher": "startup|resume|clear|compact"`)) {
		t.Fatal("SessionStart matcher does not cover current official sources")
	}
	if !bytes.Contains(second, []byte(`"custom_top_level"`)) {
		t.Fatal("unknown top-level configuration was lost")
	}
}

func TestRemoveOwnedHooksPreservesOtherHooks(t *testing.T) {
	planned, err := PlanHooks(readFixture(t, "existing-hooks.json"), "/Users/test/.local/bin/mlink")
	if err != nil {
		t.Fatal(err)
	}
	removed, err := RemoveOwnedHooks(planned)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(removed, []byte("hook codex")) {
		t.Fatal("owned Hooks remain")
	}
	if !bytes.Contains(removed, []byte("existing-prompt-hook")) || !bytes.Contains(removed, []byte("check-command")) {
		t.Fatal("user Hooks were removed")
	}
}

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}
