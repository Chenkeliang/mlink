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

type backendUninstallPlanner struct {
	plan install.ChangeSet
	err  error
}

func (planner backendUninstallPlanner) PlanUninstall(context.Context) (install.ChangeSet, error) {
	return planner.plan, planner.err
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

func TestFullUninstallAfterIncrementalCursorEnableRemovesOnlyOwnedCursorEntries(t *testing.T) {
	service, target, _ := newInstallFixture(t)
	target.files["/Users/test/.cursor/hooks.json"] = memoryFile{content: []byte(`{"version":1,"hooks":{"afterFileEdit":[{"command":"./format.sh"}]}}`), mode: 0o600}
	target.files["/Users/test/.cursor/mcp.json"] = memoryFile{content: []byte(`{"mcpServers":{"postgres":{"command":"npx"}}}`), mode: 0o600}
	installRequest := fixtureInstallRequest()
	installPlan, err := service.PlanInstall(context.Background(), installRequest)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.ApplyInstall(context.Background(), installPlan.PlanID, installRequest); err != nil {
		t.Fatal(err)
	}
	cursorPlan, err := service.PlanCursorEnable(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := service.ApplyCursorEnable(context.Background(), cursorPlan.PlanID); err != nil {
		t.Fatal(err)
	}
	request := UninstallRequest{Agents: []Agent{Codex, Pi, Hermes, Cursor}}
	plan, err := service.PlanUninstall(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.ApplyUninstall(context.Background(), plan.PlanID, request); err != nil {
		t.Fatal(err)
	}
	if _, exists := target.files[service.Paths.Binary]; exists {
		t.Fatal("full uninstall left MLink binary")
	}
	if hooks := string(target.files["/Users/test/.cursor/hooks.json"].content); !strings.Contains(hooks, "./format.sh") || strings.Contains(hooks, "hook cursor") {
		t.Fatalf("Cursor hooks after uninstall = %s", hooks)
	}
	if mcp := string(target.files["/Users/test/.cursor/mcp.json"].content); !strings.Contains(mcp, "postgres") || strings.Contains(mcp, "mlink-memory") {
		t.Fatalf("Cursor MCP after uninstall = %s", mcp)
	}
}

func TestFullUninstallAcceptsMLinkRotatedHermesGrantOnlyWhenKeychainMatches(t *testing.T) {
	service, target, secrets := newInstallFixture(t)
	request := fixtureInstallRequest()
	plan, err := service.PlanInstall(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.ApplyInstall(context.Background(), plan.PlanID, request); err != nil {
		t.Fatal(err)
	}
	path := "/home/test/.hermes/mlink.json"
	rotated := []byte("rotated-hermes-grant")
	file := target.files[path]
	file.content = bytes.Replace(file.content, []byte("hermes-grant-secret"), rotated, 1)
	target.files[path] = file
	secrets.values["adapter/hermes/token"] = append([]byte(nil), rotated...)
	uninstallRequest := UninstallRequest{Agents: []Agent{Codex, Pi, Hermes}}
	uninstallPlan, err := service.PlanUninstall(context.Background(), uninstallRequest)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.ApplyUninstall(context.Background(), uninstallPlan.PlanID, uninstallRequest); err != nil {
		t.Fatal(err)
	}
	if _, exists := target.files[path]; exists {
		t.Fatal("rotated Hermes grant file remains after uninstall")
	}

	service, target, _ = newInstallFixture(t)
	request = fixtureInstallRequest()
	plan, _ = service.PlanInstall(context.Background(), request)
	_ = service.ApplyInstall(context.Background(), plan.PlanID, request)
	file = target.files[path]
	file.content = bytes.Replace(file.content, []byte("hermes-grant-secret"), []byte("external-change"), 1)
	target.files[path] = file
	if _, err := service.PlanUninstall(context.Background(), uninstallRequest); err == nil {
		t.Fatal("external Hermes grant change was accepted")
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
	store.state.State = "active"
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

func TestFullUninstallComposesOwnedMemoryCoreRemovalWithoutDataVolume(t *testing.T) {
	service, _, _ := newInstallFixture(t)
	request := fixtureInstallRequest()
	installPlan, err := service.PlanInstall(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.ApplyInstall(context.Background(), installPlan.PlanID, request); err != nil {
		t.Fatal(err)
	}
	service.ProviderBackend = backendUninstallPlanner{plan: install.ChangeSet{
		PlanID: "plan_backend", Operations: []install.Operation{{
			ID: "op_backend_remove", OwnerID: "dev.mlink.memorycore.container", Target: "service:remove:tdai-memory-core",
			Action: install.ActionService, Command: []string{"docker", "rm", "-f", "tdai-memory-core"},
		}},
	}}
	plan, err := service.PlanUninstall(context.Background(), UninstallRequest{Agents: []Agent{Codex, Pi, Hermes}})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, operation := range plan.Operations {
		found = found || operation.Target == "service:remove:tdai-memory-core"
		if strings.Contains(operation.Target, "volume") || strings.Contains(strings.Join(operation.Command, " "), "volume rm") {
			t.Fatalf("ordinary uninstall removes MemoryCore data: %#v", operation)
		}
	}
	if !found {
		t.Fatalf("MemoryCore removal missing: %#v", plan.Operations)
	}
}

func TestFullUninstallPropagatesUnownedBackendRefusal(t *testing.T) {
	service, _, _ := newInstallFixture(t)
	request := fixtureInstallRequest()
	installPlan, _ := service.PlanInstall(context.Background(), request)
	_ = service.ApplyInstall(context.Background(), installPlan.PlanID, request)
	service.ProviderBackend = backendUninstallPlanner{err: errors.New("MLink refuses to remove an unowned MemoryCore container")}
	if _, err := service.PlanUninstall(context.Background(), UninstallRequest{Agents: []Agent{Codex, Pi, Hermes}}); err == nil || !strings.Contains(err.Error(), "unowned") {
		t.Fatalf("PlanUninstall() error = %v", err)
	}
}

func TestUninstallRemovesOnlyOwnedClaudeHooks(t *testing.T) {
	service, target, _ := newInstallFixture(t)
	target.files["/Users/test/.claude/settings.json"] = memoryFile{content: []byte(`{"model":"opus[1m]","hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"/Users/test/bin/check","timeout":5}]}]}}`), mode: 0o600}
	installRequest := fixtureInstallRequest()
	installRequest.Agents = []Agent{Codex, Pi, Hermes, Cursor, Claude}
	installPlan, err := service.PlanInstall(context.Background(), installRequest)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.ApplyInstall(context.Background(), installPlan.PlanID, installRequest); err != nil {
		t.Fatal(err)
	}
	if settings := string(target.files["/Users/test/.claude/settings.json"].content); !strings.Contains(settings, "hook claude Stop") {
		t.Fatalf("install did not add MLink Hooks: %s", settings)
	}
	request := UninstallRequest{Agents: []Agent{Codex, Pi, Hermes, Cursor, Claude}}
	plan, err := service.PlanUninstall(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.ApplyUninstall(context.Background(), plan.PlanID, request); err != nil {
		t.Fatal(err)
	}
	settings := string(target.files["/Users/test/.claude/settings.json"].content)
	if strings.Contains(settings, "hook claude") {
		t.Fatalf("owned Claude Code Hooks survived uninstall: %s", settings)
	}
	if !strings.Contains(settings, "/Users/test/bin/check") || !strings.Contains(settings, "opus[1m]") {
		t.Fatalf("unrelated Claude Code settings were removed: %s", settings)
	}
}

func TestUninstallLeavingClaudeEnabledIsNotAFullTeardown(t *testing.T) {
	service, target, secrets := newInstallFixture(t)
	target.files["/Users/test/.claude/settings.json"] = memoryFile{content: []byte(`{"model":"opus[1m]"}`), mode: 0o600}
	installRequest := fixtureInstallRequest()
	installRequest.Agents = []Agent{Codex, Pi, Hermes, Cursor, Claude}
	installPlan, err := service.PlanInstall(context.Background(), installRequest)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.ApplyInstall(context.Background(), installPlan.PlanID, installRequest); err != nil {
		t.Fatal(err)
	}
	request := UninstallRequest{Agents: []Agent{Codex, Pi, Hermes, Cursor}}
	plan, err := service.PlanUninstall(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.ApplyUninstall(context.Background(), plan.PlanID, request); err != nil {
		t.Fatal(err)
	}
	if _, exists := target.files[service.Paths.Binary]; !exists {
		t.Fatal("partial uninstall removed the MLink binary Claude Code Hooks still call")
	}
	if _, exists := secrets.values["identity/hmac-key"]; !exists {
		t.Fatal("partial uninstall deleted shared install secrets")
	}
	if settings := string(target.files["/Users/test/.claude/settings.json"].content); !strings.Contains(settings, "hook claude Stop") {
		t.Fatalf("unselected Claude Code Hooks were removed: %s", settings)
	}
}
