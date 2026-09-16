package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"mlink/internal/app"
	"mlink/internal/config"
	"mlink/internal/doctor"
	"mlink/internal/install"
	"mlink/internal/journal"
	"mlink/internal/workspacebackup"
)

type fakeApplication struct {
	plan                     install.ChangeSet
	applyCalls               int
	appliedPlan              string
	installCalls             int
	status                   app.Status
	report                   doctor.Report
	drift                    app.DriftReport
	backups                  []journal.BackupSummary
	panelStatus              app.PanelControlStatus
	credentialStatuses       []app.CredentialStatus
	credentialCopies         int
	copiedCredential         app.CredentialRole
	workspaceManifest        workspacebackup.Manifest
	workspacePassphraseBytes int
	workspaceBackupApplies   int
	workspaceRestorePlans    int
}

func (application *fakeApplication) PlanCursorEnable(context.Context) (install.ChangeSet, error) {
	return application.plan, nil
}

func (application *fakeApplication) ApplyCursorEnable(_ context.Context, planID string) error {
	application.applyCalls++
	application.appliedPlan = planID
	return nil
}

func (application *fakeApplication) PlanInstall(context.Context, app.InstallRequest) (install.ChangeSet, error) {
	application.installCalls++
	return application.plan, nil
}

func (application *fakeApplication) ApplyInstall(_ context.Context, planID string, _ app.InstallRequest) error {
	application.applyCalls++
	application.appliedPlan = planID
	return nil
}

func (application *fakeApplication) PlanRestore(context.Context, app.RestoreRequest) (install.ChangeSet, error) {
	return application.plan, nil
}

func (application *fakeApplication) ApplyRestore(context.Context, string, app.RestoreRequest) error {
	application.applyCalls++
	return nil
}

func (application *fakeApplication) PlanUninstall(context.Context, app.UninstallRequest) (install.ChangeSet, error) {
	return application.plan, nil
}

func (application *fakeApplication) ApplyUninstall(context.Context, string, app.UninstallRequest) error {
	application.applyCalls++
	return nil
}

func (application *fakeApplication) PlanPanelProvision(context.Context) (install.ChangeSet, error) {
	return application.plan, nil
}
func (application *fakeApplication) ApplyPanelProvision(_ context.Context, planID string) error {
	application.applyCalls++
	application.appliedPlan = planID
	return nil
}
func (application *fakeApplication) PlanPanelCutover(context.Context, app.ControlPlaneCutoverRequest) (install.ChangeSet, error) {
	return application.plan, nil
}
func (application *fakeApplication) ApplyPanelCutover(_ context.Context, planID string, _ app.ControlPlaneCutoverRequest) error {
	application.applyCalls++
	application.appliedPlan = planID
	return nil
}
func (application *fakeApplication) PanelControlStatus(context.Context) (app.PanelControlStatus, error) {
	return application.panelStatus, nil
}
func (application *fakeApplication) OpenPanel(context.Context) error         { return nil }
func (application *fakeApplication) CopyPanelOwnerKey(context.Context) error { return nil }
func (application *fakeApplication) CredentialStatuses(context.Context) ([]app.CredentialStatus, error) {
	return application.credentialStatuses, nil
}
func (application *fakeApplication) CopyCredential(_ context.Context, role app.CredentialRole) error {
	application.credentialCopies++
	application.copiedCredential = role
	return nil
}
func (application *fakeApplication) PlanWorkspaceBackup(_ context.Context, request app.WorkspaceBackupRequest) (install.ChangeSet, error) {
	application.workspacePassphraseBytes = len(request.Passphrase)
	return application.plan, nil
}
func (application *fakeApplication) ApplyWorkspaceBackup(_ context.Context, _ string, request app.WorkspaceBackupRequest) error {
	application.workspacePassphraseBytes = len(request.Passphrase)
	application.workspaceBackupApplies++
	return nil
}
func (application *fakeApplication) InspectWorkspaceBackup(context.Context, string, []byte) (workspacebackup.Manifest, error) {
	return application.workspaceManifest, nil
}
func (application *fakeApplication) PlanWorkspaceRestore(_ context.Context, request app.WorkspaceRestoreRequest) (install.ChangeSet, error) {
	application.workspacePassphraseBytes = len(request.Passphrase)
	application.workspaceRestorePlans++
	return application.plan, nil
}
func (application *fakeApplication) ApplyWorkspaceRestore(context.Context, string, app.WorkspaceRestoreRequest) error {
	return nil
}

func (application *fakeApplication) Status(context.Context) (app.Status, error) {
	return application.status, nil
}

func (application *fakeApplication) Doctor(context.Context, []app.Agent) (doctor.Report, error) {
	return application.report, nil
}

func (application *fakeApplication) ConfigDiff(context.Context) (app.DriftReport, error) {
	return application.drift, nil
}

func (application *fakeApplication) ListBackups(context.Context) ([]journal.BackupSummary, error) {
	return application.backups, nil
}

func TestRunPreservesTencentDBProviderCommand(t *testing.T) {
	calls := 0
	deps := Dependencies{
		Stdout: &bytes.Buffer{},
		Stderr: &bytes.Buffer{},
		ServeTencentDB: func(context.Context) error {
			calls++
			return nil
		},
	}
	if code := Run(context.Background(), []string{"provider", "run", "tencentdb"}, deps); code != 0 {
		t.Fatalf("Run() code = %d, want 0", code)
	}
	if calls != 1 {
		t.Fatalf("ServeTencentDB calls = %d, want 1", calls)
	}
}

func TestRunWithoutArgumentsStartsTUI(t *testing.T) {
	calls := 0
	code := Run(context.Background(), nil, Dependencies{
		Stderr: io.Discard,
		RunTUI: func(context.Context) error {
			calls++
			return nil
		},
	})
	if code != 0 || calls != 1 {
		t.Fatalf("code/calls = %d/%d", code, calls)
	}
}

func TestRunReportsProviderFailureWithoutLeakingDetails(t *testing.T) {
	stderr := &bytes.Buffer{}
	code := Run(context.Background(), []string{"provider", "run", "tencentdb"}, Dependencies{
		Stdout: &bytes.Buffer{},
		Stderr: stderr,
		ServeTencentDB: func(context.Context) error {
			return errors.New("token actual-secret rejected")
		},
	})
	if code != 1 {
		t.Fatalf("Run() code = %d, want 1", code)
	}
	if got := stderr.String(); got != "mlink provider command failed\n" {
		t.Fatalf("stderr = %q", got)
	}
}

func TestRunRecognizesStillPlannedCommands(t *testing.T) {
	commands := [][]string{
		{"hook", "codex"},
	}
	for _, args := range commands {
		stderr := &bytes.Buffer{}
		if code := Run(context.Background(), args, Dependencies{Stdout: &bytes.Buffer{}, Stderr: stderr}); code != 2 {
			t.Fatalf("Run(%q) code = %d, want 2", args, code)
		}
		if !strings.Contains(stderr.String(), "not implemented") {
			t.Fatalf("Run(%q) stderr = %q", args, stderr.String())
		}
	}
}

func TestStatusJSONRendersStructuredState(t *testing.T) {
	application := &fakeApplication{status: app.Status{
		Installed: true, ActivePlanID: "plan_active", ConnectionID: "local", Adapters: map[app.Agent]bool{app.Codex: true},
	}}
	stdout := &bytes.Buffer{}
	code := Run(context.Background(), []string{"status", "--json"}, Dependencies{App: application, Stdout: stdout, Stderr: io.Discard})
	if code != 0 || !strings.Contains(stdout.String(), `"active_plan_id": "plan_active"`) || !strings.Contains(stdout.String(), `"codex": true`) {
		t.Fatalf("code/output = %d/%s", code, stdout)
	}
}

func TestDoctorReturnsPendingActionExitCode(t *testing.T) {
	application := &fakeApplication{report: doctor.Report{Checks: []doctor.Check{
		{ID: "codex.hook", State: doctor.StatePendingAction, Code: "awaiting_trust"},
	}}}
	stdout := &bytes.Buffer{}
	code := Run(context.Background(), []string{"doctor", "codex", "--json"}, Dependencies{App: application, Stdout: stdout, Stderr: io.Discard})
	if code != 4 || !strings.Contains(stdout.String(), `"code": "awaiting_trust"`) {
		t.Fatalf("code/output = %d/%s", code, stdout)
	}
}

func TestConfigDiffAndBackupListRenderJSON(t *testing.T) {
	application := &fakeApplication{
		drift:   app.DriftReport{Resources: []app.DriftResource{{OwnerID: "owner", Target: "/target", State: app.DriftModified}}},
		backups: []journal.BackupSummary{{BackupID: "plan_backup", Resources: 8}},
	}
	for name, args := range map[string][]string{
		"diff":    {"config", "diff", "--json"},
		"backups": {"backup", "list", "--json"},
	} {
		t.Run(name, func(t *testing.T) {
			stdout := &bytes.Buffer{}
			if code := Run(context.Background(), args, Dependencies{App: application, Stdout: stdout, Stderr: io.Discard}); code != 0 {
				t.Fatalf("code = %d", code)
			}
			if !strings.Contains(stdout.String(), "modified") && !strings.Contains(stdout.String(), "plan_backup") {
				t.Fatalf("output = %s", stdout)
			}
		})
	}
}

func TestRunStartsBrokerService(t *testing.T) {
	calls := 0
	code := Run(context.Background(), []string{"broker", "serve"}, Dependencies{
		Stderr: io.Discard,
		ServeBroker: func(context.Context) error {
			calls++
			return nil
		},
	})
	if code != 0 || calls != 1 {
		t.Fatalf("code/calls = %d/%d", code, calls)
	}
}

func TestRunStartsMCPService(t *testing.T) {
	calls := 0
	code := Run(context.Background(), []string{"mcp", "serve"}, Dependencies{
		Stderr: io.Discard,
		ServeMCP: func(context.Context) error {
			calls++
			return nil
		},
	})
	if code != 0 || calls != 1 {
		t.Fatalf("code/calls = %d/%d", code, calls)
	}
}

func TestAdapterEnableCursorRequiresExactPlan(t *testing.T) {
	application := &fakeApplication{plan: install.ChangeSet{PlanID: "plan_cursor", MLinkVersion: "dev", Operations: []install.Operation{{Target: "/Users/test/.cursor/hooks.json", Action: install.ActionSemanticMerge}}}}
	code := Run(context.Background(), []string{"adapter", "enable", "cursor", "--apply-plan", "plan_cursor", "--yes"}, Dependencies{App: application, Stdin: strings.NewReader(""), Stdout: io.Discard, Stderr: io.Discard})
	if code != 0 || application.applyCalls != 1 || application.appliedPlan != "plan_cursor" {
		t.Fatalf("code/application = %d/%#v", code, application)
	}
}

func TestInstallDryRunPrintsPlanAndNeverApplies(t *testing.T) {
	application := &fakeApplication{plan: fixtureInstallPlan()}
	stdout := new(bytes.Buffer)
	code := Run(context.Background(), []string{"install", "--dry-run", "codex", "pi", "hermes"}, Dependencies{
		App: application, InstallRequest: fixtureCLIInstallRequest(), Stdin: strings.NewReader(""), Stdout: stdout, Stderr: io.Discard,
	})
	if code != 0 {
		t.Fatalf("code = %d", code)
	}
	if application.applyCalls != 0 {
		t.Fatalf("apply calls = %d", application.applyCalls)
	}
	for _, want := range []string{"Plan", "plan_test", "/Users/test/.local/bin/mlink", "memory.provider", "hy-memory -> mlink"} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("preview missing %q:\n%s", want, stdout)
		}
	}
}

func TestInstallDeclinedConsentNeverApplies(t *testing.T) {
	application := &fakeApplication{plan: fixtureInstallPlan()}
	code := Run(context.Background(), []string{"install", "codex"}, Dependencies{
		App: application, InstallRequest: fixtureCLIInstallRequest(), Stdin: strings.NewReader("n\n"), Stdout: io.Discard, Stderr: io.Discard,
	})
	if code != 0 || application.applyCalls != 0 {
		t.Fatalf("code/apply = %d/%d", code, application.applyCalls)
	}
}

func TestInstallRequiresDisplayedPlanForNonInteractiveApply(t *testing.T) {
	application := &fakeApplication{plan: fixtureInstallPlan()}
	code := Run(context.Background(), []string{"install", "codex", "--apply-plan", "plan_test", "--yes"}, Dependencies{
		App: application, InstallRequest: fixtureCLIInstallRequest(), Stdin: strings.NewReader(""), Stdout: io.Discard, Stderr: io.Discard,
	})
	if code != 0 || application.applyCalls != 1 || application.appliedPlan != "plan_test" {
		t.Fatalf("code/calls/plan = %d/%d/%q", code, application.applyCalls, application.appliedPlan)
	}
	application.applyCalls = 0
	code = Run(context.Background(), []string{"install", "codex", "--yes"}, Dependencies{
		App: application, InstallRequest: fixtureCLIInstallRequest(), Stdin: strings.NewReader(""), Stdout: io.Discard, Stderr: io.Discard,
	})
	if code != 2 || application.applyCalls != 0 {
		t.Fatalf("unsafe apply code/calls = %d/%d", code, application.applyCalls)
	}
}

func TestInstallReadsMemoryCoreTokenFromStdinAndRejectsArgvSecret(t *testing.T) {
	application := &fakeApplication{plan: fixtureInstallPlan()}
	template := fixtureCLIInstallRequest()
	template.SecretInputs = nil
	code := Run(context.Background(), []string{"install", "--dry-run", "codex", "--memorycore-token-stdin"}, Dependencies{
		App: application, InstallRequest: template, Stdin: strings.NewReader("stdin-token\n"), Stdout: io.Discard, Stderr: io.Discard,
	})
	if code != 0 {
		t.Fatalf("stdin token code = %d", code)
	}
	code = Run(context.Background(), []string{"install", "--dry-run", "codex", "--memorycore-token", "argv-secret"}, Dependencies{
		App: application, InstallRequest: template, Stdin: strings.NewReader(""), Stdout: io.Discard, Stderr: io.Discard,
	})
	if code != 2 {
		t.Fatalf("argv token code = %d", code)
	}
}

func TestInstallJSONIsNonMutatingWithoutApplyPlan(t *testing.T) {
	application := &fakeApplication{plan: fixtureInstallPlan()}
	stdout := new(bytes.Buffer)
	code := Run(context.Background(), []string{"install", "codex", "--json"}, Dependencies{
		App: application, InstallRequest: fixtureCLIInstallRequest(), Stdin: strings.NewReader("y\n"), Stdout: stdout, Stderr: io.Discard,
	})
	if code != 0 || application.applyCalls != 0 {
		t.Fatalf("code/apply = %d/%d", code, application.applyCalls)
	}
	if strings.Contains(stdout.String(), "secret") || !strings.Contains(stdout.String(), `"plan_id": "plan_test"`) {
		t.Fatalf("json = %s", stdout)
	}
}

func TestBackupRestoreAndUninstallRequireConfirmation(t *testing.T) {
	for name, args := range map[string][]string{
		"restore":   {"backup", "restore", "plan_backup"},
		"uninstall": {"uninstall", "codex"},
	} {
		t.Run(name, func(t *testing.T) {
			application := &fakeApplication{plan: fixtureInstallPlan()}
			code := Run(context.Background(), args, Dependencies{
				App: application, Stdin: strings.NewReader("n\n"), Stdout: io.Discard, Stderr: io.Discard,
			})
			if code != 0 || application.applyCalls != 0 {
				t.Fatalf("code/apply = %d/%d", code, application.applyCalls)
			}
		})
	}
}

func TestMutatingCommandWrongExplicitPlanReturnsConflict(t *testing.T) {
	application := &fakeApplication{plan: fixtureInstallPlan()}
	code := Run(context.Background(), []string{"uninstall", "codex", "--apply-plan", "plan_other", "--yes"}, Dependencies{
		App: application, Stdin: strings.NewReader(""), Stdout: io.Discard, Stderr: io.Discard,
	})
	if code != 3 || application.applyCalls != 0 {
		t.Fatalf("code/apply = %d/%d", code, application.applyCalls)
	}
}

func fixtureInstallPlan() install.ChangeSet {
	return install.ChangeSet{
		PlanID: "plan_test", GeneratedAt: time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC), MLinkVersion: "0.1.0",
		Operations: []install.Operation{
			{ID: "op_binary", OwnerID: "dev.mlink.binary", Target: "/Users/test/.local/bin/mlink", Action: install.ActionCreate, RollbackAction: "remove_created"},
			{
				ID: "op_hermes", OwnerID: "dev.mlink.adapter.hermes.config", Target: "/home/test/.hermes/config.yaml", Action: install.ActionSemanticMerge, RollbackAction: "restore_backup",
				SemanticDiff:        []install.SemanticDiff{{Path: "memory.provider", Before: "hy-memory", After: "mlink"}},
				ProtectedInvariants: []install.Invariant{{Name: "hermes.model_auth", BeforeHash: "same", ProposedHash: "same", Preserved: true}},
			},
		},
	}
}

func fixtureCLIInstallRequest() app.InstallRequest {
	return app.InstallRequest{
		Connection:   config.Connection{ID: "local"},
		SecretInputs: map[string][]byte{app.MemoryCoreTokenSecret: []byte("memorycore-token")},
	}
}

func TestRunRejectsUnknownCommand(t *testing.T) {
	stderr := &bytes.Buffer{}
	if code := Run(context.Background(), []string{"unknown"}, Dependencies{Stdout: &bytes.Buffer{}, Stderr: stderr}); code != 2 {
		t.Fatalf("Run() code = %d, want 2", code)
	}
	if got := stderr.String(); got != "unsupported MLink command\n" {
		t.Fatalf("stderr = %q", got)
	}
}

func TestRunRendersRootAndCommandHelpWithoutApplication(t *testing.T) {
	for name, args := range map[string][]string{
		"root flag":    {"--help"},
		"root command": {"help"},
		"maintenance":  {"help", "maintenance"},
		"install flag": {"install", "--help"},
	} {
		t.Run(name, func(t *testing.T) {
			stdout := &bytes.Buffer{}
			if code := Run(context.Background(), args, Dependencies{Stdout: stdout, Stderr: io.Discard}); code != 0 {
				t.Fatalf("Run(%q) = %d", args, code)
			}
			if !strings.Contains(stdout.String(), "Usage:") || !strings.Contains(stdout.String(), "doctor") {
				t.Fatalf("help = %q", stdout.String())
			}
		})
	}
}

func TestRunDispatchesCodexHookEvent(t *testing.T) {
	calls := 0
	code := Run(context.Background(), []string{"hook", "codex", "Stop"}, Dependencies{
		Stdout: &bytes.Buffer{},
		Stderr: &bytes.Buffer{},
		RunCodexHook: func(_ context.Context, event string) error {
			calls++
			if event != "Stop" {
				t.Fatalf("event = %q", event)
			}
			return nil
		},
	})
	if code != 0 || calls != 1 {
		t.Fatalf("code/calls = %d/%d", code, calls)
	}
}

func TestRunDispatchesCursorHookEvent(t *testing.T) {
	calls := 0
	code := Run(context.Background(), []string{"hook", "cursor", "afterAgentResponse"}, Dependencies{
		Stderr: io.Discard,
		RunCursorHook: func(_ context.Context, event string) error {
			calls++
			if event != "afterAgentResponse" {
				t.Fatalf("event = %q", event)
			}
			return nil
		},
	})
	if code != 0 || calls != 1 {
		t.Fatalf("code/calls = %d/%d", code, calls)
	}
}

func TestRunDispatchesClaudeHookEvent(t *testing.T) {
	calls := 0
	code := Run(context.Background(), []string{"hook", "claude", "UserPromptSubmit"}, Dependencies{
		Stderr: io.Discard,
		RunClaudeHook: func(_ context.Context, event string) error {
			calls++
			if event != "UserPromptSubmit" {
				t.Fatalf("event = %q", event)
			}
			return nil
		},
	})
	if code != 0 || calls != 1 {
		t.Fatalf("code/calls = %d/%d", code, calls)
	}
}
