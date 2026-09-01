package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"

	"mlink/internal/identity"
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
