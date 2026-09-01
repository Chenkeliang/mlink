package cli

import (
	"bytes"
	"context"
	"io"
	"runtime"
	"strings"
	"testing"
	"time"

	"mlink/internal/version"
	"mlink/internal/workspacebackup"
)

func TestWorkspaceBackupCreateReadsProtectedPassphraseAndRequiresExactPlan(t *testing.T) {
	application := &fakeApplication{plan: fixtureInstallPlan()}
	stdout := &bytes.Buffer{}
	args := []string{"backup", "create", "--output", "/tmp/full.mlink-backup", "--passphrase-stdin", "--apply-plan", "plan_test", "--yes"}
	code := Run(context.Background(), args, Dependencies{App: application, Stdin: strings.NewReader("correct-passphrase\n"), Stdout: stdout, Stderr: io.Discard})
	if code != 0 || application.workspaceBackupApplies != 1 || application.workspacePassphraseBytes != len("correct-passphrase") {
		t.Fatalf("code/applies/passphrase = %d/%d/%d", code, application.workspaceBackupApplies, application.workspacePassphraseBytes)
	}
	if strings.Contains(stdout.String(), "correct-passphrase") {
		t.Fatalf("backup output leaked passphrase: %s", stdout)
	}
	application.workspaceBackupApplies = 0
	args[6] = "plan_stale"
	if code := Run(context.Background(), args, Dependencies{App: application, Stdin: strings.NewReader("correct-passphrase\n"), Stdout: io.Discard, Stderr: io.Discard}); code != 3 || application.workspaceBackupApplies != 0 {
		t.Fatalf("stale code/applies = %d/%d", code, application.workspaceBackupApplies)
	}
}

func TestWorkspaceBackupInspectRendersSafeManifestSummary(t *testing.T) {
	application := &fakeApplication{workspaceManifest: workspacebackup.Manifest{
		Format: workspacebackup.FormatV1, CreatedAt: time.Unix(1, 0).UTC(),
		MLink:        version.Info{Version: "0.1.0", GOOS: runtime.GOOS, GOARCH: runtime.GOARCH},
		Provider:     workspacebackup.ProviderManifest{ProviderID: "dev.mlink.tencentdb", InstanceID: "default"},
		ControlPlane: workspacebackup.ControlPlaneManifest{OwnerUserID: "usr-private", OwnerAgentID: "agt-private"},
		Agents:       []string{"codex", "pi"},
	}}
	stdout := &bytes.Buffer{}
	code := Run(context.Background(), []string{"backup", "inspect", "/tmp/full.mlink-backup", "--passphrase-stdin", "--json"}, Dependencies{
		App: application, Stdin: strings.NewReader("correct-passphrase\n"), Stdout: stdout, Stderr: io.Discard,
	})
	if code != 0 || !strings.Contains(stdout.String(), `"provider": "dev.mlink.tencentdb"`) {
		t.Fatalf("code/output = %d/%s", code, stdout)
	}
	for _, protected := range []string{"usr-private", "agt-private", "/tmp/full", "correct-passphrase"} {
		if strings.Contains(stdout.String(), protected) {
			t.Fatalf("inspect leaked %q: %s", protected, stdout)
		}
	}
}

func TestWorkspaceRestoreCLIUsesProtectedInputAndRejectsInlineOrOversizedSecrets(t *testing.T) {
	application := &fakeApplication{plan: fixtureInstallPlan()}
	args := []string{"backup", "restore", "/tmp/full.mlink-backup", "--passphrase-stdin", "--dry-run", "--json"}
	if code := Run(context.Background(), args, Dependencies{App: application, Stdin: strings.NewReader("correct-passphrase\n"), Stdout: io.Discard, Stderr: io.Discard}); code != 0 || application.workspaceRestorePlans != 1 {
		t.Fatalf("restore code/plans = %d/%d", code, application.workspaceRestorePlans)
	}
	if code := Run(context.Background(), []string{"backup", "restore", "/tmp/full.mlink-backup", "--passphrase", "inline-secret"}, Dependencies{App: application, Stderr: io.Discard}); code != 2 {
		t.Fatalf("inline secret code = %d", code)
	}
	oversized := strings.Repeat("x", 4097) + "\n"
	if code := Run(context.Background(), args, Dependencies{App: application, Stdin: strings.NewReader(oversized), Stdout: io.Discard, Stderr: io.Discard}); code != 2 {
		t.Fatalf("oversized secret code = %d", code)
	}
}

func TestWorkspaceBackupHelpDocumentsFullLifecycle(t *testing.T) {
	stdout := &bytes.Buffer{}
	if code := Run(context.Background(), []string{"help", "backup"}, Dependencies{Stdout: stdout, Stderr: io.Discard}); code != 0 {
		t.Fatalf("help code = %d", code)
	}
	for _, command := range []string{"backup create", "backup inspect", "backup restore", "--passphrase-stdin", "--apply-plan"} {
		if !strings.Contains(stdout.String(), command) {
			t.Fatalf("backup help missing %q: %s", command, stdout)
		}
	}
}
