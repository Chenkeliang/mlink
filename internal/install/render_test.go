package install

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRenderersExposeDiffButNeverOperationContent(t *testing.T) {
	changeSet := ChangeSet{
		PlanID:       "plan-1",
		MLinkVersion: "0.1.0",
		Operations: []Operation{{
			ID:           "operation-1",
			OwnerID:      "codex",
			Target:       "/Users/test/.codex/hooks.json",
			Action:       ActionSemanticMerge,
			BeforeHash:   "before",
			ProposedHash: "after",
			SemanticDiff: []SemanticDiff{{Path: "hooks.SessionStart", Before: "absent", After: "owned"}},
			Content:      []byte("actual-secret"),
		}},
		Warnings: []string{"Codex hook trust is required"},
	}
	text, err := RenderText(changeSet)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "/Users/test/.codex/hooks.json") || !strings.Contains(text, "hooks.SessionStart") {
		t.Fatalf("text = %q", text)
	}
	if strings.Contains(text, "actual-secret") {
		t.Fatal("text renderer leaked operation content")
	}
	data, err := RenderJSON(changeSet)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "actual-secret") {
		t.Fatal("JSON renderer leaked operation content")
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
}
