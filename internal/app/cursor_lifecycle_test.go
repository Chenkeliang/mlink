package app

import (
	"bytes"
	"context"
	"testing"

	"gopkg.in/yaml.v3"

	"mlink/internal/config"
	"mlink/internal/install"
	"mlink/internal/layout"
)

func TestPlanCursorEnablePreservesSchemaV3AndAddsOnlyOwnedCursorConfig(t *testing.T) {
	configuration := config.Config{
		SchemaVersion: 3, NamespaceID: "install", ActiveConnectionID: "local",
		Connections:     map[string]config.Connection{"local": {ID: "local", ProviderID: "dev.mlink.tencentdb", ProviderVersion: "0.1.0", ConfigRevision: "rev-3"}},
		Principals:      map[string]config.Principal{"owner": {ID: "owner", CanonicalUserID: "usr-owner", Kind: config.PrincipalPerson}},
		Adapters:        map[string]config.Adapter{"codex": {ID: "codex", Enabled: true, SpaceID: "owner"}},
		ControlPlane:    &config.ControlPlane{ProviderID: "dev.mlink.tencentdb", InstanceID: "default", OwnerUserID: "user", OwnerTeamID: "team", OwnerAgentID: "agent", OwnerAssetID: "asset", DynamicAgentLimit: 500},
		RoutingPolicies: map[string]config.RoutingPolicy{"owner": {ID: "owner", Layers: []config.MemoryLayer{config.LayerL1, config.LayerL2, config.LayerL3}, AgentPolicy: config.AgentFixed}},
	}
	configData, _ := yaml.Marshal(configuration)
	target := newMemoryTarget(map[string]memoryFile{
		"/Users/test/.mlink/config.yaml": {content: configData, mode: 0o600},
		"/Users/test/.cursor/hooks.json": {content: []byte(`{"version":1,"hooks":{"afterFileEdit":[{"command":"./format.sh"}]}}`), mode: 0o600},
		"/Users/test/.cursor/mcp.json":   {content: []byte(`{"mcpServers":{"postgres":{"command":"npx"}}}`), mode: 0o600},
	})
	service := Service{Paths: layout.Paths{Home: "/Users/test/.mlink", Binary: "/Users/test/.local/bin/mlink", Config: "/Users/test/.mlink/config.yaml"}, Target: target, Ledger: newMemoryLedger()}
	plan, err := service.PlanCursorEnable(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if target.writes != 0 || len(plan.Operations) != 4 || plan.Operations[3].Target != "service:kickstart:dev.mlink.broker" {
		t.Fatalf("writes/operations = %d/%#v", target.writes, plan.Operations)
	}
	var proposed []byte
	for _, operation := range plan.Operations {
		if operation.Target == service.Paths.Config {
			proposed = operation.Content
		}
	}
	if !bytes.Contains(proposed, []byte("schema_version: 3")) || !bytes.Contains(proposed, []byte("cursor:")) || !bytes.Contains(proposed, []byte("space_id: owner")) || !bytes.Contains(proposed, []byte("codex:")) {
		t.Fatalf("proposed config = %s", proposed)
	}
	if err := service.ApplyCursorEnable(context.Background(), plan.PlanID); err != nil {
		t.Fatal(err)
	}
	if target.writes != 3 || target.runs != 1 {
		t.Fatalf("apply writes/runs = %d/%d", target.writes, target.runs)
	}
}

func TestApplyCursorEnableRejectsStalePlan(t *testing.T) {
	service, target, _ := cursorEnableFixture(t)
	plan, _ := service.PlanCursorEnable(context.Background())
	target.files[service.Paths.Config] = memoryFile{content: append(target.files[service.Paths.Config].content, []byte("\n# changed")...), mode: 0o600}
	if err := service.ApplyCursorEnable(context.Background(), plan.PlanID); err == nil {
		t.Fatal("expected stale plan")
	}
}

func cursorEnableFixture(t *testing.T) (*Service, *memoryTarget, *memoryLedger) {
	t.Helper()
	configuration := config.Config{SchemaVersion: 2, NamespaceID: "install", ActiveConnectionID: "local", Connections: map[string]config.Connection{"local": {ID: "local", ProviderID: "dev.mlink.tencentdb", ProviderVersion: "0.1.0", ConfigRevision: "rev"}}, Principals: map[string]config.Principal{"owner": {ID: "owner", CanonicalUserID: "usr-owner", Kind: config.PrincipalPerson}}, Spaces: map[string]config.MemorySpace{"personal-owner": {ID: "personal-owner", ConnectionID: "local", TenantID: "team", AgentID: "agent", PrincipalPolicy: config.PolicyFixed, PrincipalID: "owner"}}, Adapters: map[string]config.Adapter{}}
	data, _ := yaml.Marshal(configuration)
	target := newMemoryTarget(map[string]memoryFile{"/Users/test/.mlink/config.yaml": {content: data, mode: 0o600}})
	ledger := newMemoryLedger()
	service := &Service{Paths: layout.Paths{Home: "/Users/test/.mlink", Binary: "/Users/test/.local/bin/mlink", Config: "/Users/test/.mlink/config.yaml"}, Target: target, Ledger: ledger}
	return service, target, ledger
}

var _ install.Ledger = (*memoryLedger)(nil)
