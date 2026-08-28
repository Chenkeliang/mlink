package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
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
