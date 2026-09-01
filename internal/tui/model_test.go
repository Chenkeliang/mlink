package tui

import (
	"context"
	"errors"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"mlink/internal/app"
	"mlink/internal/config"
	"mlink/internal/doctor"
	"mlink/internal/identity"
	"mlink/internal/install"
	"mlink/internal/provider/lifecycle"
)

type fakeApplication struct {
	plan                    install.ChangeSet
	applyCalls              int
	planCalls               int
	candidates              []identity.Candidate
	planRequest             app.InstallRequest
	applyErr                error
	panelPlan               install.ChangeSet
	panelApplies            int
	providerStatus          lifecycle.BackendStatus
	providerPlan            install.ChangeSet
	providerRequest         lifecycle.BackendInstallRequest
	providerApplies         int
	providerApplyErr        error
	controlPlan             install.ChangeSet
	controlApplies          int
	controlLimit            int
	bootstrapRequest        app.ControlPlaneBootstrapRequest
	panelStatus             app.PanelControlStatus
	workspaceRestorePlan    install.ChangeSet
	workspaceRestorePlans   int
	workspaceRestoreApplies int
	workspaceRestoreRequest app.WorkspaceRestoreRequest
	workspaceBackupPlan     install.ChangeSet
	workspaceBackupPlans    int
	workspaceBackupApplies  int
	credentialStatuses      []app.CredentialStatus
	credentialCopies        int
	workspaceRestorePlanErr error
	workspaceBackupPlanErr  error
}

func (application *fakeApplication) PlanInstall(_ context.Context, request app.InstallRequest) (install.ChangeSet, error) {
	application.planCalls++
	application.planRequest = cloneRequest(request)
	return application.plan, nil
}
func (application *fakeApplication) ApplyInstall(context.Context, string, app.InstallRequest) error {
	application.applyCalls++
	return application.applyErr
}
func (application *fakeApplication) Status(context.Context) (app.Status, error) {
	return app.Status{Adapters: map[app.Agent]bool{}}, nil
}
func (application *fakeApplication) Doctor(context.Context, []app.Agent) (doctor.Report, error) {
	return doctor.Report{Checks: []doctor.Check{{ID: "broker.socket", State: doctor.StatePassed, Code: "reachable"}}}, nil
}
func (application *fakeApplication) DetectIdentityCandidates(context.Context) ([]identity.Candidate, error) {
	if application.candidates == nil {
		return []identity.Candidate{identity.NewCandidate("Owner", "union_id", "on_owner", time.Unix(20, 0))}, nil
	}
	return append([]identity.Candidate(nil), application.candidates...), nil
}
func (application *fakeApplication) ProviderStatus(context.Context) (lifecycle.BackendStatus, error) {
	if application.providerStatus.State == "" {
		return lifecycle.BackendStatus{ProviderID: "dev.mlink.tencentdb", State: lifecycle.BackendReachable, Endpoint: "http://127.0.0.1:8420", Local: true}, nil
	}
	return application.providerStatus, nil
}
func (application *fakeApplication) PlanProviderInstall(_ context.Context, request lifecycle.BackendInstallRequest) (install.ChangeSet, error) {
	application.providerRequest = lifecycle.BackendInstallRequest{
		ProviderID: request.ProviderID, Endpoint: request.Endpoint, GatewayToken: append([]byte(nil), request.GatewayToken...),
		LLMBaseURL: request.LLMBaseURL, LLMModel: request.LLMModel, LLMAPIKey: append([]byte(nil), request.LLMAPIKey...),
	}
	return application.providerPlan, nil
}
func (application *fakeApplication) ApplyProviderInstall(context.Context, string, lifecycle.BackendInstallRequest) error {
	application.providerApplies++
	return application.providerApplyErr
}
func (application *fakeApplication) PlanControlPlaneProvision(_ context.Context, limit int) (install.ChangeSet, error) {
	application.controlLimit = limit
	if application.controlPlan.PlanID != "" {
		return application.controlPlan, nil
	}
	return install.ChangeSet{PlanID: "plan_control"}, nil
}
func (application *fakeApplication) ApplyControlPlaneProvision(_ context.Context, _ string, limit int) error {
	application.controlLimit, application.controlApplies = limit, application.controlApplies+1
	return nil
}
func (application *fakeApplication) PlanControlPlaneBootstrap(_ context.Context, request app.ControlPlaneBootstrapRequest) (install.ChangeSet, error) {
	application.bootstrapRequest = request
	application.bootstrapRequest.GatewayToken = append([]byte(nil), request.GatewayToken...)
	return application.PlanControlPlaneProvision(context.Background(), request.DynamicAgentLimit)
}
func (application *fakeApplication) ApplyControlPlaneBootstrap(_ context.Context, _ string, request app.ControlPlaneBootstrapRequest) error {
	application.bootstrapRequest = request
	application.bootstrapRequest.GatewayToken = append([]byte(nil), request.GatewayToken...)
	return application.ApplyControlPlaneProvision(context.Background(), "", request.DynamicAgentLimit)
}
func (application *fakeApplication) PlanPanelRuntime(context.Context) (install.ChangeSet, error) {
	if application.panelPlan.PlanID != "" {
		return application.panelPlan, nil
	}
	return install.ChangeSet{PlanID: "plan_panel"}, nil
}
func (application *fakeApplication) ApplyPanelRuntime(context.Context, string) error {
	application.panelApplies++
	return nil
}
func (application *fakeApplication) PanelControlStatus(context.Context) (app.PanelControlStatus, error) {
	return application.panelStatus, nil
}
func (application *fakeApplication) PlanWorkspaceRestore(_ context.Context, request app.WorkspaceRestoreRequest) (install.ChangeSet, error) {
	application.workspaceRestorePlans++
	application.workspaceRestoreRequest = request
	application.workspaceRestoreRequest.Passphrase = append([]byte(nil), request.Passphrase...)
	return application.workspaceRestorePlan, application.workspaceRestorePlanErr
}
func (application *fakeApplication) ApplyWorkspaceRestore(_ context.Context, _ string, request app.WorkspaceRestoreRequest) error {
	application.workspaceRestoreApplies++
	application.workspaceRestoreRequest = request
	application.workspaceRestoreRequest.Passphrase = append([]byte(nil), request.Passphrase...)
	return application.applyErr
}
func (application *fakeApplication) PlanWorkspaceBackup(_ context.Context, request app.WorkspaceBackupRequest) (install.ChangeSet, error) {
	application.workspaceBackupPlans++
	return application.workspaceBackupPlan, application.workspaceBackupPlanErr
}
func (application *fakeApplication) ApplyWorkspaceBackup(context.Context, string, app.WorkspaceBackupRequest) error {
	application.workspaceBackupApplies++
	return application.applyErr
}
func (application *fakeApplication) CredentialStatuses(context.Context) ([]app.CredentialStatus, error) {
	return append([]app.CredentialStatus(nil), application.credentialStatuses...), nil
}
func (application *fakeApplication) CopyCredential(context.Context, app.CredentialRole) error {
	application.credentialCopies++
	return nil
}

func TestWelcomeOffersRestoreAndExistingCorePaths(t *testing.T) {
	model := New(&fakeApplication{}, fixtureRequest())
	model = advance(t, model, tea.KeyMsg{Type: tea.KeyDown})
	model = advance(t, model, enterKey())
	if model.step != StepRestoreBundle {
		t.Fatalf("restore welcome step = %d", model.step)
	}
	model = New(&fakeApplication{}, fixtureRequest())
	model = advance(t, model, tea.KeyMsg{Type: tea.KeyDown})
	model = advance(t, model, tea.KeyMsg{Type: tea.KeyDown})
	model = advance(t, model, enterKey())
	if model.step != StepConnection || model.installBackend {
		t.Fatalf("existing Core step/install = %d/%t", model.step, model.installBackend)
	}
}

func TestRestoreFlowMasksPassphraseUsesExactPlanAndWipesAfterApply(t *testing.T) {
	application := &fakeApplication{workspaceRestorePlan: install.ChangeSet{PlanID: "plan_restore"}}
	model := New(application, fixtureRequest())
	model.welcomeCursor = 1
	model = advance(t, model, enterKey())
	model.restoreBundle.SetValue("/tmp/full.mlink-backup")
	model = advance(t, model, enterKey())
	model.restorePassphrase.SetValue("correct-passphrase")
	model = advance(t, model, enterKey())
	if model.step != StepRestorePreview || application.workspaceRestorePlans != 1 || string(application.workspaceRestoreRequest.Passphrase) != "correct-passphrase" {
		t.Fatalf("preview = step:%d plans:%d request:%#v", model.step, application.workspaceRestorePlans, application.workspaceRestoreRequest)
	}
	if len(application.workspaceRestoreRequest.SelectedAgents) != 0 {
		t.Fatalf("restore TUI overrode bundle Agent selection: %#v", application.workspaceRestoreRequest.SelectedAgents)
	}
	model = advance(t, model, enterKey())
	model = advance(t, model, runeKey('y'))
	model = advance(t, model, enterKey())
	if model.step != StepRestoreVerify || application.workspaceRestoreApplies != 1 || model.restorePassphrase.Value() != "" {
		t.Fatalf("apply = step:%d applies:%d passphrase:%q", model.step, application.workspaceRestoreApplies, model.restorePassphrase.Value())
	}
}

func TestWelcomeBackupFlowRequiresMatchingPassphrasesAndExactConfirmation(t *testing.T) {
	application := &fakeApplication{workspaceBackupPlan: install.ChangeSet{PlanID: "plan_backup"}}
	model := New(application, fixtureRequest())
	model = advance(t, model, runeKey('b'))
	if model.step != StepBackupInput {
		t.Fatalf("backup entry step = %d", model.step)
	}
	model.backupOutput.SetValue("/tmp/full.mlink-backup")
	model.backupPassphrase.SetValue("correct-passphrase")
	model.backupConfirmation.SetValue("different-passphrase")
	model.backupCursor = 2
	model = advance(t, model, enterKey())
	if model.err == nil || application.workspaceBackupPlans != 0 {
		t.Fatalf("mismatch error/plans = %v/%d", model.err, application.workspaceBackupPlans)
	}
	model.backupConfirmation.SetValue("correct-passphrase")
	model = advance(t, model, enterKey())
	if model.step != StepBackupPreview || application.workspaceBackupPlans != 1 {
		t.Fatalf("backup preview = %d/%d", model.step, application.workspaceBackupPlans)
	}
	model = advance(t, model, enterKey())
	model = advance(t, model, runeKey('y'))
	model = advance(t, model, enterKey())
	if model.step != StepBackupComplete || application.workspaceBackupApplies != 1 || model.backupPassphrase.Value() != "" || model.backupConfirmation.Value() != "" {
		t.Fatalf("backup apply/wipe = %d/%d/%q/%q", model.step, application.workspaceBackupApplies, model.backupPassphrase.Value(), model.backupConfirmation.Value())
	}
}

func TestWelcomeCredentialsFlowCopiesOnlyAfterConfirmation(t *testing.T) {
	application := &fakeApplication{credentialStatuses: []app.CredentialStatus{{Role: app.CredentialPanelOwner, Present: true, Fingerprint: "0123456789ab", CopyAllowed: true}}}
	model := New(application, fixtureRequest())
	model = advance(t, model, runeKey('c'))
	if model.step != StepCredentials || len(model.credentialStatuses) != 1 {
		t.Fatalf("credential entry = %d/%#v", model.step, model.credentialStatuses)
	}
	model = advance(t, model, enterKey())
	if model.step != StepCredentialConfirm || application.credentialCopies != 0 {
		t.Fatalf("credential confirmation = %d/%d", model.step, application.credentialCopies)
	}
	model = advance(t, model, runeKey('y'))
	model = advance(t, model, enterKey())
	if model.step != StepCredentials || application.credentialCopies != 1 {
		t.Fatalf("credential copy = %d/%d", model.step, application.credentialCopies)
	}
}

func TestBackupAndRestorePlanningFailuresWipePassphrases(t *testing.T) {
	application := &fakeApplication{workspaceRestorePlanErr: errors.New("restore plan failed"), workspaceBackupPlanErr: errors.New("backup plan failed")}
	restore := New(application, fixtureRequest())
	restore.step, restore.restoreCursor = StepRestoreBundle, 1
	restore.restoreBundle.SetValue("/tmp/full.mlink-backup")
	restore.restorePassphrase.SetValue("correct-passphrase")
	restore = advance(t, restore, enterKey())
	if restore.restorePassphrase.Value() != "" {
		t.Fatal("restore planning failure retained passphrase")
	}
	backup := New(application, fixtureRequest())
	backup.step, backup.backupCursor = StepBackupInput, 2
	backup.backupOutput.SetValue("/tmp/full.mlink-backup")
	backup.backupPassphrase.SetValue("correct-passphrase")
	backup.backupConfirmation.SetValue("correct-passphrase")
	backup = advance(t, backup, enterKey())
	if backup.backupPassphrase.Value() != "" || backup.backupConfirmation.Value() != "" {
		t.Fatal("backup planning failure retained passphrases")
	}
}

func TestWizardDetectsMissingBackendBeforeCredentials(t *testing.T) {
	application := &fakeApplication{providerStatus: lifecycle.BackendStatus{ProviderID: "dev.mlink.tencentdb", State: lifecycle.BackendAbsent, Endpoint: "http://127.0.0.1:8420", Local: true}}
	model := New(application, fixtureRequest())
	model = advance(t, model, enterKey())
	model = advance(t, model, enterKey())
	model = advance(t, model, enterKey())
	if model.step != StepBackendMode || model.backendStatus.State != lifecycle.BackendAbsent {
		t.Fatalf("step/status = %d/%#v", model.step, model.backendStatus)
	}
}

func TestWizardReachableBackendUsesExistingConnection(t *testing.T) {
	model := New(&fakeApplication{}, fixtureRequest())
	model = advance(t, model, enterKey())
	model = advance(t, model, enterKey())
	model = advance(t, model, enterKey())
	if model.step != StepConnection || model.installBackend || model.connectionCursor != 1 {
		t.Fatalf("step/install/cursor = %d/%t/%d", model.step, model.installBackend, model.connectionCursor)
	}
}

func TestWizardStoppedBackendOffersSetupAndCanConnectExisting(t *testing.T) {
	application := &fakeApplication{providerStatus: lifecycle.BackendStatus{ProviderID: "dev.mlink.tencentdb", State: lifecycle.BackendStopped, Installed: true}}
	model := New(application, fixtureRequest())
	model = advance(t, model, enterKey())
	model = advance(t, model, enterKey())
	model = advance(t, model, enterKey())
	if model.step != StepBackendMode {
		t.Fatalf("stopped backend step = %d", model.step)
	}
	model = advance(t, model, tea.KeyMsg{Type: tea.KeyDown})
	model = advance(t, model, enterKey())
	if model.step != StepConnection || model.installBackend {
		t.Fatalf("connect-existing = step:%d install:%t", model.step, model.installBackend)
	}
}

func TestWizardStoppedOwnedBackendRestartDoesNotRequestLLMCredentials(t *testing.T) {
	application := &fakeApplication{providerStatus: lifecycle.BackendStatus{ProviderID: "dev.mlink.tencentdb", State: lifecycle.BackendStopped, Installed: true}}
	model := New(application, fixtureRequest())
	model = advance(t, model, enterKey())
	model = advance(t, model, enterKey())
	model = advance(t, model, enterKey())
	model = advance(t, model, enterKey())
	if model.step != StepConnection || !model.installBackend || len(model.connectionFields()) != 2 {
		t.Fatalf("restart inputs = step:%d install:%t fields:%d", model.step, model.installBackend, len(model.connectionFields()))
	}
}

func TestWizardIncompatibleBackendFailsClosedBeforeCredentials(t *testing.T) {
	application := &fakeApplication{providerStatus: lifecycle.BackendStatus{ProviderID: "dev.mlink.tencentdb", State: lifecycle.BackendIncompatible}}
	model := New(application, fixtureRequest())
	model = advance(t, model, enterKey())
	model = advance(t, model, enterKey())
	model = advance(t, model, enterKey())
	if model.step != StepBackendDetect || model.err == nil {
		t.Fatalf("incompatible backend = step:%d error:%v", model.step, model.err)
	}
}

func TestWizardLocalBackendCollectsInstallInputsBeforeIdentity(t *testing.T) {
	application := &fakeApplication{providerStatus: lifecycle.BackendStatus{ProviderID: "dev.mlink.tencentdb", State: lifecycle.BackendAbsent}, providerPlan: install.ChangeSet{PlanID: "plan_backend"}}
	model := New(application, fixtureRequest())
	model = advance(t, model, enterKey())
	model = advance(t, model, enterKey())
	model = advance(t, model, enterKey())
	model = advance(t, model, enterKey())
	model.endpoint.SetValue(" http://127.0.0.1:8420 ")
	model.token.SetValue(" gateway ")
	model.llmBaseURL.SetValue(" https://llm.example/v1 ")
	model.llmModel.SetValue(" deepseek-chat ")
	model.llmAPIKey.SetValue(" llm-secret ")
	model.connectionCursor = 4
	model = advance(t, model, enterKey())
	if model.step != StepIdentity || !model.installBackend {
		t.Fatalf("step/install = %d/%t", model.step, model.installBackend)
	}
	model = selectIdentityAndCapacity(t, model, "500")
	if model.step != StepBackendPreview || application.providerRequest.Endpoint != "http://127.0.0.1:8420" || application.providerRequest.LLMModel != "deepseek-chat" {
		t.Fatalf("backend preview/request = %d/%#v", model.step, application.providerRequest)
	}
}

func TestWizardCreatesCoreIdentityBeforeAnyAgentInstallPlan(t *testing.T) {
	application := &fakeApplication{plan: install.ChangeSet{PlanID: "plan_install"}, controlPlan: install.ChangeSet{PlanID: "plan_control"}}
	model := connectedIdentityModel(application)
	model = selectIdentityAndCapacity(t, model, "777")
	if model.step != StepControlPreview || application.planCalls != 0 || application.controlLimit != 777 ||
		application.bootstrapRequest.Connection.ProviderConfig["base_url"] != "http://127.0.0.1:8420" || string(application.bootstrapRequest.GatewayToken) != "memorycore-token" {
		t.Fatalf("before control apply = step:%d plan:%d limit:%d request:%#v", model.step, application.planCalls, application.controlLimit, application.bootstrapRequest)
	}
	model = advance(t, model, enterKey())
	model = advance(t, model, runeKey('y'))
	model = advance(t, model, enterKey())
	if model.step != StepAgents || application.controlApplies != 1 || application.planCalls != 0 {
		t.Fatalf("control gate = step:%d control:%d install-plan:%d", model.step, application.controlApplies, application.planCalls)
	}
	model = advance(t, model, enterKey())
	if model.step != StepPreview || application.planCalls != 1 || application.planRequest.DynamicAgentLimit != 777 {
		t.Fatalf("after gate = step:%d plans:%d request:%#v", model.step, application.planCalls, application.planRequest)
	}
}

func TestWizardBackendApplyResumesAtCoreIdentityAndKeepsFailureRetryable(t *testing.T) {
	application := &fakeApplication{controlPlan: install.ChangeSet{PlanID: "plan_control"}}
	model := New(application, fixtureRequest())
	model.step, model.backendPlan, model.confirmed = StepBackendApply, install.ChangeSet{PlanID: "plan_backend"}, true
	model.installBackend = true
	model.dynamicAgentLimit = 500
	model.endpoint.SetValue("http://127.0.0.1:8420")
	model.token.SetValue("gateway-token-long")
	model.llmBaseURL.SetValue("https://llm.example/v1")
	model.llmModel.SetValue("model")
	model.llmAPIKey.SetValue("key")
	model = advance(t, model, enterKey())
	if model.step != StepControlPreview || application.providerApplies != 1 || model.llmAPIKey.Value() != "" || model.installBackend {
		t.Fatalf("backend resume = step:%d applies:%d llm-key:%q install:%t", model.step, application.providerApplies, model.llmAPIKey.Value(), model.installBackend)
	}

	application.providerApplyErr = errors.New("docker unavailable")
	model.step, model.backendPlan, model.confirmed = StepBackendApply, install.ChangeSet{PlanID: "plan_backend_retry"}, true
	model.llmAPIKey.SetValue("retry-key")
	model = advance(t, model, enterKey())
	if model.step != StepBackendApply || model.err == nil || model.llmAPIKey.Value() == "" {
		t.Fatalf("retryable failure = step:%d error:%v llm-key:%q", model.step, model.err, model.llmAPIKey.Value())
	}
}

func TestWizardCannotApplyBeforeDedicatedConfirmation(t *testing.T) {
	application := &fakeApplication{plan: install.ChangeSet{PlanID: "plan_test"}}
	model := New(application, fixtureRequest())
	model.step, model.plan = StepPreview, application.plan
	model = advance(t, model, enterKey())
	model = advance(t, model, runeKey('y'))
	if application.applyCalls != 0 {
		t.Fatalf("applied before Enter: %d", application.applyCalls)
	}
	model = advance(t, model, enterKey())
	if application.applyCalls != 1 {
		t.Fatalf("apply calls = %d", application.applyCalls)
	}
}

func TestWizardRequiresExplicitOwnerSelection(t *testing.T) {
	application := &fakeApplication{candidates: []identity.Candidate{
		identity.NewCandidate("陈科良", "union_id", "on_owner", time.Unix(20, 0)),
		identity.NewCandidate("陈科良", "user_id", "u_other", time.Unix(10, 0)),
	}}
	model := connectedIdentityModel(application)
	model = advance(t, model, enterKey())
	if model.step != StepIdentity {
		t.Fatalf("auto-selected duplicate name: step=%d", model.step)
	}
	model = advance(t, model, tea.KeyMsg{Type: tea.KeySpace})
	model = advance(t, model, enterKey())
	if model.step != StepCapacity || model.request.OwnerBindingSlot.ID != "owner-feishu-union-1" || string(model.request.SecretInputs[app.OwnerBindingSecret]) != "on_owner" {
		t.Fatalf("selection = step:%d request:%#v", model.step, model.request)
	}
}

func TestWizardCanContinueWithoutHermesOrFeishuIdentity(t *testing.T) {
	application := &fakeApplication{candidates: []identity.Candidate{}}
	model := New(application, fixtureRequest())
	model.step = StepIdentity
	model.candidates = []identity.Candidate{}
	model.err = errors.New("no Hermes Feishu identity candidates were detected")
	model = advance(t, model, runeKey('s'))
	if model.step != StepCapacity || model.selected[app.Hermes] || model.request.OwnerBindingSlot.ID != "" {
		t.Fatalf("local-only identity = step:%d hermes:%t binding:%#v", model.step, model.selected[app.Hermes], model.request.OwnerBindingSlot)
	}
}

func TestAgentSelectionCanReachFourthCursorOption(t *testing.T) {
	model := New(&fakeApplication{}, fixtureRequest())
	model.step = StepAgents
	for range 3 {
		model = advance(t, model, tea.KeyMsg{Type: tea.KeyDown})
	}
	if model.cursor != 3 || orderedAgents()[model.cursor] != app.Cursor {
		t.Fatalf("cursor/index = %d/%s", model.cursor, orderedAgents()[model.cursor])
	}
	model = advance(t, model, tea.KeyMsg{Type: tea.KeySpace})
	if model.selected[app.Cursor] {
		t.Fatal("Space did not toggle Cursor option")
	}
}

func TestPanelInstallReusesControlPlaneWithoutProvisioningAgain(t *testing.T) {
	application := &fakeApplication{panelPlan: install.ChangeSet{PlanID: "plan_panel"}}
	model := New(application, fixtureRequest())
	model.step = StepVerify
	model = advance(t, model, enterKey())
	model = advance(t, model, enterKey())
	if model.step != StepPanelPreview || application.controlApplies != 0 {
		t.Fatalf("panel preview = step:%d control:%d", model.step, application.controlApplies)
	}
	model = advance(t, model, enterKey())
	model = advance(t, model, runeKey('y'))
	model = advance(t, model, enterKey())
	if model.step != StepComplete || application.panelApplies != 1 || application.controlApplies != 0 {
		t.Fatalf("panel = step:%d panel:%d control:%d", model.step, application.panelApplies, application.controlApplies)
	}
}

func TestWizardApplyErrorReturnsToCredentialEntry(t *testing.T) {
	application := &fakeApplication{plan: install.ChangeSet{PlanID: "plan_test"}, applyErr: errors.New("keychain unavailable")}
	model := New(application, fixtureRequest())
	model.step, model.plan, model.dynamicAgentLimit = StepApply, application.plan, 500
	model.token.SetValue("memorycore-token")
	model.confirmed = true
	model = advance(t, model, enterKey())
	if model.step != StepConnection || model.confirmed || model.token.Value() != "" {
		t.Fatalf("step/confirmed/token = %d/%t/%q", model.step, model.confirmed, model.token.Value())
	}
}

func fixtureRequest() app.InstallRequest {
	return app.InstallRequest{OwnerSlug: "keliang", DynamicAgentLimit: 500, Connection: config.Connection{
		ID: "local", ProviderID: "dev.mlink.tencentdb", ProviderVersion: "0.1.0", ConfigRevision: "rev-local-1",
		ProviderConfig: map[string]any{"base_url": "http://127.0.0.1:8420", "service_id": "default", "timeout_ms": 5000},
		TenantID:       "personal", AgentID: "default", UserID: "local-user",
	}}
}

func connectedIdentityModel(application *fakeApplication) Model {
	model := New(application, fixtureRequest())
	model.step = StepIdentity
	model.token.SetValue("memorycore-token")
	model.candidates = []identity.Candidate{identity.NewCandidate("Owner", "union_id", "on_owner", time.Unix(20, 0))}
	return model
}

func selectIdentityAndCapacity(t *testing.T, model Model, limit string) Model {
	t.Helper()
	model = advance(t, model, tea.KeyMsg{Type: tea.KeySpace})
	model = advance(t, model, enterKey())
	model.capacity.SetValue(limit)
	return advance(t, model, enterKey())
}

func advance(t *testing.T, model Model, message tea.Msg) Model {
	t.Helper()
	updated, command := model.Update(message)
	result := updated.(Model)
	if command != nil {
		resultMessage := command()
		updated, followup := result.Update(resultMessage)
		result = updated.(Model)
		if followup != nil {
			followupMessage := followup()
			updated, _ = result.Update(followupMessage)
			result = updated.(Model)
		}
	}
	return result
}

func enterKey() tea.KeyMsg          { return tea.KeyMsg{Type: tea.KeyEnter} }
func runeKey(value rune) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{value}} }
