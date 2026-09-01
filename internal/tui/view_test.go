package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"

	"mlink/internal/identity"
	"mlink/internal/install"
)

func TestNarrowViewHasNoOverflow(t *testing.T) {
	model := New(&fakeApplication{}, fixtureRequest())
	model.width = 72
	model.height = 28
	for _, line := range strings.Split(model.View(), "\n") {
		if width := lipgloss.Width(line); width > 72 {
			t.Fatalf("line width = %d: %q", width, line)
		}
	}
}

func TestViewNeverRendersRawCandidateID(t *testing.T) {
	application := &fakeApplication{candidates: []identity.Candidate{identity.NewCandidate("陈科良", "union_id", "on_actual_union_id", time.Unix(20, 0))}}
	model := New(application, fixtureRequest())
	model.step = StepIdentity
	model.candidates = application.candidates
	for _, width := range []int{72, 100, 140} {
		model.width = width
		if strings.Contains(model.View(), "on_actual_union_id") {
			t.Fatalf("width %d leaked ID", width)
		}
	}
}

func TestStatusMarkerAppearsAfterLabel(t *testing.T) {
	model := New(&fakeApplication{}, fixtureRequest())
	model.width = 100
	model.step = StepDetect
	view := model.View()
	for _, line := range strings.Split(view, "\n") {
		if strings.Contains(line, "TencentDB MemoryCore") {
			if strings.Index(line, "TencentDB MemoryCore") > strings.Index(line, "ready") {
				t.Fatalf("status marker is not after label:\n%s", view)
			}
			return
		}
	}
	t.Fatalf("TencentDB status row is missing:\n%s", view)
}

func TestPanelPreviewExplainsOfficialHubAndOptInKnowledge(t *testing.T) {
	model := New(&fakeApplication{}, fixtureRequest())
	model.width = 120
	model.step = StepPanelPreview
	view := model.View()
	for _, want := range []string{"Official Memory Hub", "8125", "8424", "not automatically imported"} {
		if !strings.Contains(view, want) {
			t.Fatalf("Hub preview missing %q:\n%s", want, view)
		}
	}
}

func TestEveryWizardStepFitsSupportedTerminalWidths(t *testing.T) {
	model := New(&fakeApplication{}, fixtureRequest())
	model.backendPlan = install.ChangeSet{PlanID: "plan_backend"}
	model.controlPlan = install.ChangeSet{PlanID: "plan_control"}
	model.plan = install.ChangeSet{PlanID: "plan_install"}
	model.panelPlan = install.ChangeSet{PlanID: "plan_panel"}
	model.candidates = []identity.Candidate{identity.NewCandidate("陈科良", "union_id", "on_hidden", time.Unix(20, 0))}
	for step := StepWelcome; step <= StepComplete; step++ {
		model.step = step
		for _, width := range []int{80, 100, 140} {
			model.width, model.height = width, 40
			view := model.View()
			for _, line := range strings.Split(view, "\n") {
				if actual := lipgloss.Width(line); actual > width {
					t.Fatalf("step=%d width=%d line-width=%d line=%q", step, width, actual, line)
				}
			}
		}
	}
}

func TestWelcomeAndRestoreViewsExposeOnlyActionableInformation(t *testing.T) {
	model := New(&fakeApplication{}, fixtureRequest())
	view := model.View()
	for _, label := range []string{"New installation", "Restore encrypted backup", "Connect existing MemoryCore"} {
		if !strings.Contains(view, label) {
			t.Fatalf("welcome missing %q:\n%s", label, view)
		}
	}
	model.step = StepRestoreBundle
	model.restoreBundle.SetValue("/Users/private/full.mlink-backup")
	model.restorePassphrase.SetValue("secret-passphrase")
	view = model.View()
	if strings.Contains(view, "secret-passphrase") || !strings.Contains(view, "Encrypted backup") {
		t.Fatalf("restore input view is unsafe:\n%s", view)
	}
}
