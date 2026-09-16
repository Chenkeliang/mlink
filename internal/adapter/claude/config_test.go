package claude

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestPlanHooksPreservesExistingEventsAndIsIdempotent(t *testing.T) {
	existing := readFixture(t, "existing-settings.json")
	first, err := PlanHooks(existing, "/Users/test/.local/bin/mlink")
	if err != nil {
		t.Fatal(err)
	}
	second, err := PlanHooks(first, "/Users/test/.local/bin/mlink")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("second install changed settings")
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
	for _, event := range claudeEvents {
		if countOwnedHooks(document.Hooks[event], event) != 1 {
			t.Fatalf("MLink hook count for %s is not 1", event)
		}
	}
	if !bytes.Contains(second, []byte(`"matcher": "startup|resume|clear|compact|fork"`)) {
		t.Fatal("SessionStart matcher does not cover current official sources")
	}
	if bytes.Contains(second, []byte("additionalContextLimit")) {
		t.Fatal("Claude Code has no additionalContextLimit Hook field")
	}
}

func TestPlanHooksLeavesProtectedSettingsUnchanged(t *testing.T) {
	existing := readFixture(t, "existing-settings.json")
	planned, err := PlanHooks(existing, "/Users/test/.local/bin/mlink")
	if err != nil {
		t.Fatal(err)
	}
	before, err := ProtectedSettingsHash(existing)
	if err != nil {
		t.Fatal(err)
	}
	after, err := ProtectedSettingsHash(planned)
	if err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatal("settings outside hooks changed")
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(planned, &document); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"model", "theme", "env", "permissions", "enabledPlugins"} {
		if len(document[key]) == 0 {
			t.Fatalf("protected setting %q was dropped", key)
		}
	}
	if !bytes.Contains(document["env"], []byte("must-not-move")) {
		t.Fatal("protected env value was rewritten")
	}
}

func TestDesiredHooksResourceReportsPreservedInvariant(t *testing.T) {
	resource, err := DesiredHooksResource(readFixture(t, "existing-settings.json"), "/Users/test/.claude/settings.json", "/Users/test/.local/bin/mlink")
	if err != nil {
		t.Fatal(err)
	}
	if len(resource.ProtectedInvariants) != 1 || !resource.ProtectedInvariants[0].Preserved {
		t.Fatalf("invariants = %#v", resource.ProtectedInvariants)
	}
	if err := resource.Verify(resource.Content); err != nil {
		t.Fatal(err)
	}
	tampered := bytes.Replace(resource.Content, []byte(`"opus[1m]"`), []byte(`"other-model"`), 1)
	if err := resource.Verify(tampered); err == nil {
		t.Fatal("Verify() accepted a changed protected setting")
	}
}

func TestRemoveOwnedHooksPreservesOtherHooksAndSettings(t *testing.T) {
	planned, err := PlanHooks(readFixture(t, "existing-settings.json"), "/Users/test/.local/bin/mlink")
	if err != nil {
		t.Fatal(err)
	}
	removed, err := RemoveOwnedHooks(planned)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(removed, []byte("hook claude")) {
		t.Fatal("owned Hooks remain")
	}
	if !bytes.Contains(removed, []byte("existing-prompt-hook")) || !bytes.Contains(removed, []byte("check-command")) {
		t.Fatal("user Hooks were removed")
	}
	if !bytes.Contains(removed, []byte("opus[1m]")) {
		t.Fatal("protected settings were removed")
	}
}

func TestRemoveOwnedHooksDropsEmptyHooksKey(t *testing.T) {
	planned, err := PlanHooks([]byte(`{"model":"opus[1m]"}`), "/Users/test/.local/bin/mlink")
	if err != nil {
		t.Fatal(err)
	}
	removed, err := RemoveOwnedHooks(planned)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(removed, &document); err != nil {
		t.Fatal(err)
	}
	if _, exists := document["hooks"]; exists {
		t.Fatalf("empty hooks key survived uninstall: %s", removed)
	}
}

func TestPlanHooksRejectsRelativeBinary(t *testing.T) {
	if _, err := PlanHooks(nil, "mlink"); err == nil {
		t.Fatal("PlanHooks() error = nil")
	}
}

func TestHasOwnedHooksDistinguishesUntouchedSettings(t *testing.T) {
	existing := readFixture(t, "existing-settings.json")
	owned, err := HasOwnedHooks(existing)
	if err != nil {
		t.Fatal(err)
	}
	if owned {
		t.Fatal("settings MLink never touched reported as owned")
	}
	planned, err := PlanHooks(existing, "/Users/test/.local/bin/mlink")
	if err != nil {
		t.Fatal(err)
	}
	owned, err = HasOwnedHooks(planned)
	if err != nil {
		t.Fatal(err)
	}
	if !owned {
		t.Fatal("installed MLink Hooks not reported as owned")
	}
}
