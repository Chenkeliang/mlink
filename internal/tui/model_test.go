package tui

import (
	"context"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"mlink/internal/app"
	"mlink/internal/config"
	"mlink/internal/doctor"
	"mlink/internal/install"
)

type fakeApplication struct {
	plan       install.ChangeSet
	applyCalls int
}

func (application *fakeApplication) PlanInstall(context.Context, app.InstallRequest) (install.ChangeSet, error) {
	return application.plan, nil
}

func (application *fakeApplication) ApplyInstall(context.Context, string, app.InstallRequest) error {
	application.applyCalls++
	return nil
}

func (application *fakeApplication) Status(context.Context) (app.Status, error) {
	return app.Status{Adapters: map[app.Agent]bool{}}, nil
}

func (application *fakeApplication) Doctor(context.Context, []app.Agent) (doctor.Report, error) {
	return doctor.Report{Checks: []doctor.Check{{ID: "broker.socket", State: doctor.StatePassed, Code: "reachable"}}}, nil
}

func TestWizardCannotApplyBeforeDedicatedConfirmation(t *testing.T) {
	application := &fakeApplication{plan: install.ChangeSet{PlanID: "plan_test", Operations: []install.Operation{{Target: "/tmp/mlink", Action: install.ActionCreate}}}}
	model := New(application, fixtureRequest())
	model.width = 100
	model.token.SetValue("memorycore-token")

	model = advance(t, model, tea.KeyMsg{Type: tea.KeyEnter}) // detect
	model = advance(t, model, tea.KeyMsg{Type: tea.KeyEnter}) // provider
	model = advance(t, model, tea.KeyMsg{Type: tea.KeyEnter}) // connection
	model = advance(t, model, tea.KeyMsg{Type: tea.KeyEnter}) // identity
	model = advance(t, model, tea.KeyMsg{Type: tea.KeyEnter}) // agents
	model = advance(t, model, tea.KeyMsg{Type: tea.KeyEnter}) // agents -> preview
	if model.step != StepPreview || application.applyCalls != 0 {
		t.Fatalf("step/apply = %d/%d", model.step, application.applyCalls)
	}
	model = advance(t, model, tea.KeyMsg{Type: tea.KeyEnter}) // dedicated confirmation page
	if model.step != StepApply || application.applyCalls != 0 {
		t.Fatalf("step/apply = %d/%d", model.step, application.applyCalls)
	}
	model = advance(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	if application.applyCalls != 0 {
		t.Fatalf("applied before final Enter: %d", application.applyCalls)
	}
	model = advance(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	if application.applyCalls != 1 {
		t.Fatalf("apply calls = %d", application.applyCalls)
	}
}

func fixtureRequest() app.InstallRequest {
	return app.InstallRequest{Connection: config.Connection{
		ID: "local", ProviderID: "dev.mlink.tencentdb", ProviderVersion: "0.1.0", ConfigRevision: "rev-local-1",
		ProviderConfig: map[string]any{"base_url": "http://127.0.0.1:8096", "service_id": "default", "timeout_ms": 5000},
		TenantID:       "personal", AgentID: "default", UserID: "local-user",
	}}
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
