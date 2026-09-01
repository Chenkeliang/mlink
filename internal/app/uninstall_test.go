package app

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"mlink/internal/install"
	"mlink/internal/journal"
)

type blockingEvents struct {
	events []journal.Event
}

func TestFullUninstallUsesLatestSemanticBackupAndDeletesConnectionSecret(t *testing.T) {
	service, target, secrets := newInstallFixture(t)
	installRequest := fixtureInstallRequest()
	plan, err := service.PlanInstall(context.Background(), installRequest)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.ApplyInstall(context.Background(), plan.PlanID, installRequest); err != nil {
		t.Fatal(err)
	}
	hermesPath := "/home/test/.hermes/config.yaml"
	file := target.files[hermesPath]
	file.content = append(file.content, []byte("later_user_setting: keep\n")...)
	target.files[hermesPath] = file

	request := UninstallRequest{Agents: []Agent{Codex, Pi, Hermes}}
	uninstallPlan, err := service.PlanUninstall(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.ApplyUninstall(context.Background(), uninstallPlan.PlanID, request); err != nil {
		t.Fatal(err)
	}
	if _, exists := target.files[service.Paths.Binary]; exists {
		t.Fatal("MLink binary still exists")
	}
	if _, exists := secrets.values["connection/local/token"]; exists {
		t.Fatal("MemoryCore token still exists")
	}
	if _, exists := secrets.values["identity/hmac-key"]; exists {
		t.Fatal("identity key still exists")
	}
	if _, exists := secrets.values["adapter/hermes/token"]; exists {
		t.Fatal("Hermes grant still exists")
	}
	if _, exists := secrets.values["identity/binding/owner-feishu-union-1"]; exists {
		t.Fatal("owner Binding still exists")
	}
	if got := target.files[hermesPath].content; !bytes.Contains(got, []byte("provider: hy-memory")) || !bytes.Contains(got, []byte("later_user_setting: keep")) {
		t.Fatalf("Hermes config after uninstall = %s", got)
	}
	if got := target.files[hermesPath].content; !bytes.Contains(got, []byte("group_sessions_per_user: true")) || bytes.Contains(got, []byte("thread_sessions_per_user:")) {
		t.Fatalf("Hermes Session flags after uninstall = %s", got)
	}
}

func (store blockingEvents) ListStateDeletionBlockers(context.Context) ([]journal.Event, error) {
	return append([]journal.Event(nil), store.events...), nil
}

func TestUninstallBlocksStateDeletionWithAmbiguousEvents(t *testing.T) {
	service, _, _ := newInstallFixture(t)
	service.BlockingEvents = blockingEvents{events: []journal.Event{{ID: "event-1", State: journal.StateAmbiguous}}}
	_, err := service.PlanUninstall(context.Background(), UninstallRequest{Agents: []Agent{Codex}, RemoveState: true})
	if !errors.Is(err, journal.ErrBlockingEvents) {
		t.Fatalf("error = %v", err)
	}
}

func TestControlPlaneUninstallRemovesOnlyLocalPanelAndKeepsBackendMetadata(t *testing.T) {
	service, target, secrets := installedIdentityFixture(t)
	state := journal.ControlPlaneState{
		InstallationID: "personal", InstanceID: "default", OwnerUserID: "usr-owner-generated", OwnerTeamID: "team-owner-generated",
		OwnerAgentID: "agt-owner-generated", OwnerAssetID: "chat_memory-team-owner-generated-agt-owner-generated",
		PanelContainer: "tdai-memory-hub", PanelImage: "agentmemory/memory-hub@sha256:7be68305b9ab279407584ffe44300605a5833df57a1730ca5bd41bbbe4b3f104", State: "provisioned",
	}
	store := &identityControlStore{state: state, mappings: map[string]journal.PrincipalAgent{
		"prn_aaaaaaaaaaaaaaaaaaaaaaaaaa": {
			Fingerprint: "prn_aaaaaaaaaaaaaaaaaaaaaaaaaa", RouteKind: "hermes-private", BackendUserID: state.OwnerUserID,
			BackendTeamID: state.OwnerTeamID, BackendAgentID: "agt-private", BackendAssetID: "chat_memory-team-owner-generated-agt-private",
			DisplayLabel: "Feishu DM", State: "active",
		},
	}}
	service.ControlPlaneStates = store
	service.PrincipalAgentStates = store
	secrets.values["control/tencentdb/admin-user-key"] = []byte("admin-key")
	secrets.values["control/tencentdb/owner-user-key"] = []byte("owner-key")
	cutover, err := service.PlanControlPlaneCutover(context.Background(), ControlPlaneCutoverRequest{DynamicAgentLimit: 500})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.ApplyControlPlaneCutover(context.Background(), cutover.PlanID, ControlPlaneCutoverRequest{DynamicAgentLimit: 500}); err != nil {
		t.Fatal(err)
	}
	target.files[service.Paths.PanelRegistry] = memoryFile{content: []byte("protected registry"), mode: 0o600}
	request := UninstallRequest{Agents: []Agent{Codex, Pi, Hermes}}
	plan, err := service.PlanUninstall(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	rendered, _ := install.RenderJSON(plan)
	if bytes.Contains(rendered, []byte("docker volume rm")) || !bytes.Contains(rendered, []byte("tdai-memory-hub")) {
		t.Fatalf("Hub uninstall lifecycle is unsafe: %s", rendered)
	}
	containerOperation := operationForTarget(t, plan, "service:remove:tdai-memory-hub")
	rollback := strings.Join(containerOperation.RollbackCommand, " ")
	if !strings.Contains(rollback, "127.0.0.1:8125:8125") || !strings.Contains(rollback, "127.0.0.1:8424:8424") ||
		!strings.Contains(rollback, "tdai-panel-data:/data/knowledge") || strings.Contains(rollback, "8096") {
		t.Fatalf("Hub rollback command = %q", rollback)
	}
	for _, forbidden := range []string{"/v3/meta/", "conversation", "atomic", "scenario", "core/read", "delete backend"} {
		if strings.Contains(strings.ToLower(string(rendered)), forbidden) {
			t.Fatalf("uninstall touches backend memory %q: %s", forbidden, rendered)
		}
	}
	if err := service.ApplyUninstall(context.Background(), plan.PlanID, request); err != nil {
		t.Fatal(err)
	}
	if _, exists := target.files[service.Paths.PanelRegistry]; exists {
		t.Fatal("Panel registry still exists")
	}
	if store.state.State != "inactive" || store.mappings["prn_aaaaaaaaaaaaaaaaaaaaaaaaaa"].State != "inactive" {
		t.Fatalf("local mapping states = %#v/%#v", store.state, store.mappings)
	}
	if _, exists := secrets.values["control/tencentdb/owner-user-key"]; exists {
		t.Fatal("local Owner key still exists")
	}
}
