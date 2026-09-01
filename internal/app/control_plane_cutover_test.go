package app

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"mlink/internal/config"
	"mlink/internal/install"
	"mlink/internal/journal"
)

type cutoverStateStore struct {
	state journal.ControlPlaneState
	marks []string
}

func (store *cutoverStateStore) LoadControlPlane(context.Context) (journal.ControlPlaneState, error) {
	if store.state.InstallationID == "" {
		return journal.ControlPlaneState{}, fs.ErrNotExist
	}
	return store.state, nil
}

func (store *cutoverStateStore) MarkControlPlaneState(_ context.Context, state string) error {
	store.marks = append(store.marks, state)
	store.state.State = state
	return nil
}

func TestControlPlaneCutoverPreviewIsExactAndDoesNotMigrateMemory(t *testing.T) {
	service, target, states, request := newCutoverFixture(t)
	hooksBefore := append([]byte(nil), target.files["/Users/test/.codex/hooks.json"].content...)
	plan, err := service.PlanControlPlaneCutover(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if target.writes != 0 || target.runs != 0 || len(states.marks) != 0 {
		t.Fatalf("preview mutated writes/runs/marks = %d/%d/%#v", target.writes, target.runs, states.marks)
	}
	if !bytes.Equal(hooksBefore, target.files["/Users/test/.codex/hooks.json"].content) {
		t.Fatal("Codex Hook changed during preview")
	}
	configOperation := operationForTarget(t, plan, service.Paths.Config)
	var proposed config.Config
	if err := yaml.Unmarshal(configOperation.Content, &proposed); err != nil {
		t.Fatal(err)
	}
	if proposed.SchemaVersion != 3 || proposed.ControlPlane.OwnerUserID != "usr-owner-generated" ||
		proposed.ControlPlane.OwnerTeamID != "team-owner-generated" || proposed.ControlPlane.OwnerAgentID != "agt-owner-generated" ||
		proposed.ControlPlane.DynamicAgentLimit != 500 || len(proposed.Spaces) != 0 {
		t.Fatalf("proposed config = %#v", proposed)
	}
	if proposed.Connections["local"].ProviderConfig["base_url"] != "http://127.0.0.1:8420" {
		t.Fatalf("Core endpoint = %#v", proposed.Connections["local"].ProviderConfig)
	}
	wantDiffs := map[string]string{
		"schema_version": "3", "legacy_scope_migration": "disabled; retained backend memory stays inactive",
		"routing:owner": "Core-generated Owner · L1/L2/L3", "routing:hermes-private": "dynamic Agent per principal · L1 only",
		"routing:hermes-groups": "dynamic Agent per group · topic session · L1 only",
	}
	for path, after := range wantDiffs {
		found := false
		for _, diff := range configOperation.SemanticDiff {
			found = found || diff.Path == path && diff.After == after
		}
		if !found {
			t.Fatalf("missing diff %q -> %q: %#v", path, after, configOperation.SemanticDiff)
		}
	}
	for _, operation := range plan.Operations {
		combined := strings.ToLower(operation.Target + " " + strings.Join(operation.Command, " "))
		for _, forbidden := range []string{"conversation/add", "atomic/", "scenario/", "core/read", "memory migration", "8096"} {
			if strings.Contains(combined, forbidden) {
				t.Fatalf("cutover operation migrates or uses legacy memory: %#v", operation)
			}
		}
	}
	if len(plan.ProtectedInvariants) == 0 {
		t.Fatal("protected invariants are missing")
	}
}

func TestControlPlaneCutoverApplyActivatesGeneratedIDsAndRestartsServices(t *testing.T) {
	service, target, states, request := newCutoverFixture(t)
	hermesBefore := append([]byte(nil), target.files["/home/test/.hermes/config.yaml"].content...)
	hooksBefore := append([]byte(nil), target.files["/Users/test/.codex/hooks.json"].content...)
	plan, err := service.PlanControlPlaneCutover(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.ApplyControlPlaneCutover(context.Background(), plan.PlanID, request); err != nil {
		t.Fatal(err)
	}
	got, err := config.Decode(target.files[service.Paths.Config].content)
	if err != nil {
		t.Fatal(err)
	}
	if got.SchemaVersion != 3 || got.Principals["owner"].CanonicalUserID != "usr-owner-generated" || len(got.Spaces) != 0 {
		t.Fatalf("active config = %#v", got)
	}
	if target.runs != 2 || len(states.marks) != 1 || states.marks[0] != "active" {
		t.Fatalf("runs/marks = %d/%#v", target.runs, states.marks)
	}
	if owned := service.Ledger.(*memoryLedger).owned; len(owned) != 1 || owned[0].Target != service.Paths.Config {
		t.Fatalf("cutover ownership = %#v", owned)
	}
	if !bytes.Equal(hermesBefore, target.files["/home/test/.hermes/config.yaml"].content) ||
		!bytes.Equal(hooksBefore, target.files["/Users/test/.codex/hooks.json"].content) {
		t.Fatal("cutover changed Hermes model/auth config or Codex Hook")
	}
}

func TestControlPlaneCutoverRestartFailureRestoresV2Config(t *testing.T) {
	service, target, states, request := newCutoverFixture(t)
	before := append([]byte(nil), target.files[service.Paths.Config].content...)
	target.failAtRun = 2
	plan, err := service.PlanControlPlaneCutover(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	err = service.ApplyControlPlaneCutover(context.Background(), plan.PlanID, request)
	if err == nil {
		t.Fatal("ApplyControlPlaneCutover() error = nil")
	}
	if !bytes.Equal(before, target.files[service.Paths.Config].content) {
		t.Fatalf("config was not restored:\n%s", target.files[service.Paths.Config].content)
	}
	if target.runs != 4 || len(states.marks) != 0 {
		t.Fatalf("rollback runs/marks = %d/%#v", target.runs, states.marks)
	}
	if owned := service.Ledger.(*memoryLedger).owned; len(owned) != 0 {
		t.Fatalf("failed cutover recorded ownership: %#v", owned)
	}
}

func TestControlPlaneCutoverRejectsStalePlanWithoutMutation(t *testing.T) {
	service, target, _, request := newCutoverFixture(t)
	before := append([]byte(nil), target.files[service.Paths.Config].content...)
	err := service.ApplyControlPlaneCutover(context.Background(), "plan_stale", request)
	if !errors.Is(err, install.ErrPlanStale) || !bytes.Equal(before, target.files[service.Paths.Config].content) || target.runs != 0 {
		t.Fatalf("stale apply = %v writes/runs=%d/%d", err, target.writes, target.runs)
	}
}

func newCutoverFixture(t *testing.T) (*Service, *memoryTarget, *cutoverStateStore, ControlPlaneCutoverRequest) {
	t.Helper()
	service, target, _ := newInstallFixture(t)
	legacy, err := service.desiredConfig(fixtureInstallRequest(), []Agent{Codex, Pi, Hermes})
	if err != nil {
		t.Fatal(err)
	}
	data, err := yaml.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	target.files[service.Paths.Config] = memoryFile{content: data, mode: 0o600}
	states := &cutoverStateStore{state: journal.ControlPlaneState{
		InstallationID: legacy.NamespaceID, InstanceID: "default", OwnerUserID: "usr-owner-generated",
		OwnerTeamID: "team-owner-generated", OwnerAgentID: "agt-owner-generated",
		OwnerAssetID: "chat_memory-team-owner-generated-agt-owner-generated", PanelContainer: "tdai-memory-hub",
		PanelImage: "agentmemory/memory-hub@sha256:7be68305b9ab279407584ffe44300605a5833df57a1730ca5bd41bbbe4b3f104", State: "provisioned",
	}}
	service.ControlPlaneStates = states
	return service, target, states, ControlPlaneCutoverRequest{DynamicAgentLimit: 500}
}
