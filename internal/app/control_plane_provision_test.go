package app

import (
	"context"
	"io/fs"
	"strings"
	"testing"

	"mlink/internal/controlplane"
	"mlink/internal/install"
	"mlink/internal/journal"
	"mlink/internal/panel"
)

type appControlProvisioner struct {
	plan       install.ChangeSet
	result     controlplane.ProvisionResult
	applyCalls int
}

func (provisioner *appControlProvisioner) PlanProvision(context.Context, controlplane.ProvisionRequest) (install.ChangeSet, error) {
	return provisioner.plan, nil
}
func (provisioner *appControlProvisioner) ApplyProvision(_ context.Context, _ string, _ controlplane.ProvisionRequest) (controlplane.ProvisionResult, error) {
	provisioner.applyCalls++
	return provisioner.result, nil
}

type fixedControlState struct{ value journal.ControlPlaneState }

func (state *fixedControlState) LoadControlPlane(context.Context) (journal.ControlPlaneState, error) {
	if state.value.State == "" {
		return journal.ControlPlaneState{}, fs.ErrNotExist
	}
	return state.value, nil
}
func (state *fixedControlState) MarkControlPlaneState(_ context.Context, value string) error {
	state.value.State = value
	return nil
}

func TestHeadlessControlPlaneProvisionDoesNotRequirePanelRuntime(t *testing.T) {
	provisioner := &appControlProvisioner{
		plan:   install.ChangeSet{PlanID: "plan_control", Operations: []install.Operation{{Target: "control-plane:owner-agent", Action: install.ActionService}}},
		result: controlplane.ProvisionResult{PlanID: "plan_control", OwnerUserID: "usr-owner", OwnerTeamID: "team-owner", OwnerAgentID: "agt-owner", OwnerAssetID: "chat_memory-team-owner-agt-owner"},
	}
	service := Service{ControlProvisioner: provisioner, ControlRequest: controlplane.ProvisionRequest{InstallationID: "install", InstanceID: "default"}}
	plan, err := service.PlanControlPlaneProvision(context.Background())
	if err != nil || plan.PlanID != "plan_control" {
		t.Fatalf("plan/error = %#v/%v", plan, err)
	}
	if err := service.ApplyControlPlaneProvision(context.Background(), plan.PlanID); err != nil || provisioner.applyCalls != 1 {
		t.Fatalf("apply/error = %d/%v", provisioner.applyCalls, err)
	}
}

func TestPanelRuntimePlanReusesExistingControlPlaneWithoutMetadataIntents(t *testing.T) {
	state := &fixedControlState{value: journal.ControlPlaneState{
		InstallationID: "install", InstanceID: "default", OwnerUserID: "usr-owner", OwnerTeamID: "team-owner",
		OwnerAgentID: "agt-owner", OwnerAssetID: "chat_memory-team-owner-agt-owner", State: "active",
	}}
	target := newMemoryTarget(nil)
	service := Service{
		ControlPlaneStates: state,
		PanelRuntime:       &panel.Runtime{Runner: target, Target: target},
		PanelDesired: panel.Desired{
			RegistryPath: "/Users/test/.mlink/panel/metadata-instances.json", HostAddress: "127.0.0.1", PanelHostPort: 8125, KnowledgeHostPort: 8424,
			InstanceID: "default", InstanceName: "MLink Local", GatewayEndpoint: "http://host.docker.internal:8420",
			KnowledgePublicBaseURL: "http://host.docker.internal:8424/v3", KnowledgeLLMProxyBaseURL: "http://host.docker.internal:8420",
		},
	}
	plan, err := service.PlanPanelRuntime(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Operations) != 4 {
		t.Fatalf("Panel operations = %#v", plan.Operations)
	}
	for _, operation := range plan.Operations {
		if strings.Contains(operation.Target, "control-plane") || strings.Contains(operation.Target, "owner-agent") {
			t.Fatalf("Panel Plan contains metadata intent: %#v", operation)
		}
	}
	after, _ := state.LoadControlPlane(context.Background())
	if after.OwnerUserID != "usr-owner" || after.OwnerTeamID != "team-owner" || after.OwnerAgentID != "agt-owner" || after.OwnerAssetID != "chat_memory-team-owner-agt-owner" {
		t.Fatalf("Panel planning changed identity: %#v", after)
	}
}
