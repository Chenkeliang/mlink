package cursor

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPlanHooksPreservesUnrelatedCursorHooksAndIsIdempotent(t *testing.T) {
	existing := []byte(`{"version":1,"custom":{"keep":true},"hooks":{"afterFileEdit":[{"command":"./format.sh"}],"beforeSubmitPrompt":[{"command":"./audit.sh"}]}}`)
	first, err := PlanHooks(existing, "/Users/test/.local/bin/mlink")
	if err != nil {
		t.Fatal(err)
	}
	second, err := PlanHooks(first, "/Users/test/.local/bin/mlink")
	if err != nil || string(first) != string(second) {
		t.Fatalf("idempotence = %v\n%s\n%s", err, first, second)
	}
	var document map[string]any
	if err := json.Unmarshal(first, &document); err != nil {
		t.Fatal(err)
	}
	serialized := string(first)
	for _, want := range []string{"./format.sh", "./audit.sh", "hook cursor sessionStart", "hook cursor beforeSubmitPrompt", "hook cursor afterAgentResponse", "hook cursor sessionEnd"} {
		if !strings.Contains(serialized, want) {
			t.Fatalf("hooks missing %q: %s", want, serialized)
		}
	}
	if strings.Contains(serialized, "failClosed") || !strings.Contains(serialized, `"version": 1`) {
		t.Fatalf("unexpected hook safety/schema: %s", serialized)
	}
}

func TestRemoveOwnedHooksKeepsUnrelatedEntries(t *testing.T) {
	installed, _ := PlanHooks([]byte(`{"version":1,"hooks":{"beforeSubmitPrompt":[{"command":"./audit.sh"}]}}`), "/Users/test/.local/bin/mlink")
	removed, err := RemoveOwnedHooks(installed)
	if err != nil || !strings.Contains(string(removed), "./audit.sh") || strings.Contains(string(removed), "hook cursor") {
		t.Fatalf("removed/error = %s/%v", removed, err)
	}
}
