package cursor

import (
	"strings"
	"testing"
)

func TestPlanMCPConfigPreservesUnrelatedServersWithoutSecrets(t *testing.T) {
	existing := []byte(`{"custom":true,"mcpServers":{"postgres":{"command":"npx","args":["server"]},"mlink-memory":{"command":"old","env":{"TOKEN":"secret"}}}}`)
	first, err := PlanMCPConfig(existing, "/Users/test/.local/bin/mlink")
	if err != nil {
		t.Fatal(err)
	}
	second, err := PlanMCPConfig(first, "/Users/test/.local/bin/mlink")
	if err != nil || string(first) != string(second) {
		t.Fatalf("idempotence = %v\n%s\n%s", err, first, second)
	}
	serialized := string(first)
	for _, want := range []string{`"custom": true`, `"postgres"`, `"command": "/Users/test/.local/bin/mlink"`, `"mcp"`, `"serve"`} {
		if !strings.Contains(serialized, want) {
			t.Fatalf("config missing %q: %s", want, serialized)
		}
	}
	if strings.Contains(serialized, "TOKEN") || strings.Contains(serialized, "secret") || strings.Contains(serialized, `"env"`) {
		t.Fatalf("MLink MCP config contains credentials: %s", serialized)
	}
}

func TestRemoveOwnedMCPConfigKeepsUnrelatedServers(t *testing.T) {
	installed, _ := PlanMCPConfig([]byte(`{"mcpServers":{"postgres":{"command":"npx"}}}`), "/Users/test/.local/bin/mlink")
	removed, err := RemoveOwnedMCPConfig(installed)
	if err != nil || !strings.Contains(string(removed), "postgres") || strings.Contains(string(removed), "mlink-memory") {
		t.Fatalf("removed/error = %s/%v", removed, err)
	}
}
