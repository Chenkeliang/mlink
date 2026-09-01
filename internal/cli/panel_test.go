package cli

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"mlink/internal/app"
	"mlink/internal/journal"
	"mlink/internal/panel"
)

func TestPanelProvisionPreviewNeverAppliesOrPrintsKeys(t *testing.T) {
	application := &fakeApplication{plan: fixtureInstallPlan()}
	stdout := &bytes.Buffer{}
	code := Run(context.Background(), []string{"panel", "provision", "--dry-run", "--json"}, Dependencies{
		App: application, Stdin: strings.NewReader(""), Stdout: stdout, Stderr: io.Discard,
	})
	if code != 0 || application.applyCalls != 0 || !strings.Contains(stdout.String(), `"plan_id": "plan_test"`) {
		t.Fatalf("code/apply/output = %d/%d/%s", code, application.applyCalls, stdout)
	}
	for _, secret := range []string{"owner-key", "admin-key", "gateway-secret"} {
		if strings.Contains(stdout.String(), secret) {
			t.Fatalf("preview leaked %q: %s", secret, stdout)
		}
	}
}

func TestPanelProvisionApplyRequiresExactPlanAndYes(t *testing.T) {
	application := &fakeApplication{plan: fixtureInstallPlan()}
	for _, test := range []struct {
		args  []string
		code  int
		calls int
	}{
		{[]string{"panel", "provision", "--apply-plan", "plan_test", "--yes"}, 0, 1},
		{[]string{"panel", "provision", "--apply-plan", "plan_other", "--yes"}, 3, 0},
		{[]string{"panel", "provision", "--yes"}, 2, 0},
	} {
		application.applyCalls = 0
		code := Run(context.Background(), test.args, Dependencies{App: application, Stdin: strings.NewReader(""), Stdout: io.Discard, Stderr: io.Discard})
		if code != test.code || application.applyCalls != test.calls {
			t.Fatalf("Run(%q) = %d calls=%d", test.args, code, application.applyCalls)
		}
	}
}

func TestPanelCutoverRequiresExplicitAgentLimit(t *testing.T) {
	application := &fakeApplication{plan: fixtureInstallPlan()}
	if code := Run(context.Background(), []string{"panel", "cutover", "--dry-run", "--json"}, Dependencies{App: application, Stdout: io.Discard, Stderr: io.Discard}); code != 2 {
		t.Fatalf("missing limit code = %d", code)
	}
	if code := Run(context.Background(), []string{"panel", "cutover", "--dynamic-agent-limit", "500", "--dry-run", "--json"}, Dependencies{App: application, Stdout: io.Discard, Stderr: io.Discard}); code != 0 {
		t.Fatalf("explicit limit code = %d", code)
	}
}

func TestPanelOwnerKeyCopyIsSeparateAndWarnsWithoutPrintingKey(t *testing.T) {
	application := &fakeApplication{}
	stdout := &bytes.Buffer{}
	if code := Run(context.Background(), []string{"panel", "copy-owner-key", "--yes"}, Dependencies{App: application, Stdout: stdout, Stderr: io.Discard}); code != 0 {
		t.Fatalf("copy code = %d", code)
	}
	if !strings.Contains(stdout.String(), "clipboard") || strings.Contains(stdout.String(), "owner-key") {
		t.Fatalf("copy output = %s", stdout)
	}
	if code := Run(context.Background(), []string{"panel", "copy-owner-key"}, Dependencies{App: application, Stdout: io.Discard, Stderr: io.Discard}); code != 2 {
		t.Fatalf("unconfirmed copy code = %d", code)
	}
}

func TestPanelStatusAndDoctorRequireHubPanelKnowledgeAndInstance(t *testing.T) {
	application := &fakeApplication{panelStatus: app.PanelControlStatus{
		ControlPlane: journal.ControlPlaneState{State: "provisioned"},
		Panel:        panel.Status{ContainerPresent: true, PanelHealthy: true, KnowledgeHealthy: true, InstanceVisible: true, Healthy: true},
	}}
	stdout := &bytes.Buffer{}
	if code := Run(context.Background(), []string{"panel", "status", "--json"}, Dependencies{App: application, Stdout: stdout, Stderr: io.Discard}); code != 0 {
		t.Fatalf("status code = %d", code)
	}
	for _, field := range []string{`"container_present": true`, `"panel_healthy": true`, `"knowledge_healthy": true`, `"instance_visible": true`} {
		if !strings.Contains(stdout.String(), field) {
			t.Fatalf("status missing %s: %s", field, stdout)
		}
	}
	application.panelStatus.Panel.KnowledgeHealthy = false
	application.panelStatus.Panel.Healthy = false
	if code := Run(context.Background(), []string{"panel", "doctor", "--json"}, Dependencies{App: application, Stdout: io.Discard, Stderr: io.Discard}); code != 4 {
		t.Fatalf("degraded doctor code = %d", code)
	}
}
