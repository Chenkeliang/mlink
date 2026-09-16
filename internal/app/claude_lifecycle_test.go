package app

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"gopkg.in/yaml.v3"

	"mlink/internal/config"
	"mlink/internal/layout"
)

func TestPlanClaudeEnableRoutesToOwnerAndOwnsOnlyHooks(t *testing.T) {
	service, target := claudeEnableFixture(t)
	plan, err := service.PlanClaudeEnable(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if target.writes != 0 || len(plan.Operations) != 3 || plan.Operations[2].Target != "service:kickstart:dev.mlink.broker" {
		t.Fatalf("writes/operations = %d/%#v", target.writes, plan.Operations)
	}
	var proposedConfig, proposedSettings []byte
	for _, operation := range plan.Operations {
		switch operation.Target {
		case service.Paths.Config:
			proposedConfig = operation.Content
		case "/Users/test/.claude/settings.json":
			proposedSettings = operation.Content
		}
	}
	if !bytes.Contains(proposedConfig, []byte("claude:")) || !bytes.Contains(proposedConfig, []byte("space_id: owner")) || !bytes.Contains(proposedConfig, []byte("codex:")) {
		t.Fatalf("proposed config = %s", proposedConfig)
	}
	var settings map[string]json.RawMessage
	if err := json.Unmarshal(proposedSettings, &settings); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(settings["model"], []byte(`"opus[1m]"`)) {
		t.Fatalf("model setting changed: %s", settings["model"])
	}
	if !bytes.Contains(proposedSettings, []byte("hook claude UserPromptSubmit")) {
		t.Fatalf("MLink Hooks missing: %s", proposedSettings)
	}
	if err := service.ApplyClaudeEnable(context.Background(), plan.PlanID); err != nil {
		t.Fatal(err)
	}
	if target.writes != 2 || target.runs != 1 {
		t.Fatalf("apply writes/runs = %d/%d", target.writes, target.runs)
	}
}

func TestApplyClaudeEnableRejectsStalePlan(t *testing.T) {
	service, target := claudeEnableFixture(t)
	plan, err := service.PlanClaudeEnable(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	target.files[service.Paths.Config] = memoryFile{content: append(target.files[service.Paths.Config].content, []byte("\n# changed")...), mode: 0o600}
	if err := service.ApplyClaudeEnable(context.Background(), plan.PlanID); err == nil {
		t.Fatal("expected stale plan")
	}
}

func claudeEnableFixture(t *testing.T) (*Service, *memoryTarget) {
	t.Helper()
	configuration := config.Config{
		SchemaVersion: 3, NamespaceID: "install", ActiveConnectionID: "local",
		Connections:  map[string]config.Connection{"local": {ID: "local", ProviderID: "dev.mlink.tencentdb", ProviderVersion: "0.1.0", ConfigRevision: "rev-3"}},
		Principals:   map[string]config.Principal{"owner": {ID: "owner", CanonicalUserID: "usr-owner", Kind: config.PrincipalPerson}},
		Adapters:     map[string]config.Adapter{"codex": {ID: "codex", Enabled: true, SpaceID: "owner"}},
		ControlPlane: &config.ControlPlane{ProviderID: "dev.mlink.tencentdb", InstanceID: "default", OwnerUserID: "user", OwnerTeamID: "team", OwnerAgentID: "agent", OwnerAssetID: "asset", DynamicAgentLimit: 500},
	}
	configData, _ := yaml.Marshal(configuration)
	target := newMemoryTarget(map[string]memoryFile{
		"/Users/test/.mlink/config.yaml":    {content: configData, mode: 0o600},
		"/Users/test/.claude/settings.json": {content: []byte(`{"model":"opus[1m]","env":{"KEEP":"me"}}`), mode: 0o600},
	})
	service := &Service{Paths: layout.Paths{Home: "/Users/test/.mlink", Binary: "/Users/test/.local/bin/mlink", Config: "/Users/test/.mlink/config.yaml"}, Target: target, Ledger: newMemoryLedger()}
	return service, target
}
