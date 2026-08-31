package app

import (
	"bytes"
	"context"
	"io/fs"
	"testing"

	"mlink/internal/controlplane"
	"mlink/internal/install"
	"mlink/internal/journal"
	"mlink/internal/panel"
)

type provisionStateStub struct{}

func (provisionStateStub) SaveControlPlane(context.Context, journal.ControlPlaneState) error {
	return nil
}
func (provisionStateStub) LoadControlPlane(context.Context) (journal.ControlPlaneState, error) {
	return journal.ControlPlaneState{}, fs.ErrNotExist
}

func TestPanelProvisionPreviewComposesRemoteAndPanelPlansWithoutWrites(t *testing.T) {
	target := newMemoryTarget(nil)
	control := &controlplane.Service{States: provisionStateStub{}}
	panelRuntime := &panel.Runtime{Runner: target, Target: target}
	service := &Service{
		ControlProvisioner: control,
		ControlRequest: controlplane.ProvisionRequest{
			InstallationID: "installation-1", InstanceID: "default", AdminUsername: "mlink-admin",
			OwnerUsername: "keliang", TeamName: "MLink", OwnerAgentName: "MLink Owner",
		},
		PanelRuntime: panelRuntime,
		PanelDesired: panel.Desired{
			SourceRoot: "/src/MemoryPanel", RegistryPath: "/Users/test/.mlink/panel/metadata-instances.json",
			HostAddress: "127.0.0.1", HostPort: 8125, ContainerPort: 8123, InstanceID: "default",
			InstanceName: "MLink Local", GatewayEndpoint: "http://host.docker.internal:8420",
		},
	}
	plan, err := service.PlanPanelProvision(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Operations) != 7 || target.writes != 0 || target.runs != 0 {
		t.Fatalf("plan/writes/runs = %d/%d/%d", len(plan.Operations), target.writes, target.runs)
	}
	rendered, _ := install.RenderJSON(plan)
	for _, forbidden := range [][]byte{[]byte("admin-key"), []byte("owner-key"), []byte("gateway-secret"), []byte("8096")} {
		if bytes.Contains(rendered, forbidden) {
			t.Fatalf("preview leaked %q: %s", forbidden, rendered)
		}
	}
}
