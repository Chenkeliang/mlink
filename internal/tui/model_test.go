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
)

type fakeApplication struct {
	plan           install.ChangeSet
	applyCalls     int
	planCalls      int
	candidates     []identity.Candidate
	planRequest    app.InstallRequest
	applyErr       error
	panelPlan      install.ChangeSet
	cutoverPlan    install.ChangeSet
	panelApplies   int
	cutoverApplies int
	cutoverRequest app.ControlPlaneCutoverRequest
}

func (application *fakeApplication) PlanInstall(_ context.Context, request app.InstallRequest) (install.ChangeSet, error) {
	application.planCalls++
	application.planRequest = cloneRequest(request)
	return application.plan, nil
}

func (application *fakeApplication) DetectIdentityCandidates(context.Context) ([]identity.Candidate, error) {
	if application.candidates == nil {
		return []identity.Candidate{identity.NewCandidate("Owner", "union_id", "on_owner", time.Unix(20, 0))}, nil
	}
	return append([]identity.Candidate(nil), application.candidates...), nil
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

func (application *fakeApplication) PlanPanelProvision(context.Context) (install.ChangeSet, error) {
	if application.panelPlan.PlanID != "" {
		return application.panelPlan, nil
	}
	return application.plan, nil
}
func (application *fakeApplication) ApplyPanelProvision(context.Context, string) error {
	application.panelApplies++
	return nil
}
func (application *fakeApplication) PlanPanelCutover(context.Context, app.ControlPlaneCutoverRequest) (install.ChangeSet, error) {
	if application.cutoverPlan.PlanID != "" {
		return application.cutoverPlan, nil
	}
	return application.plan, nil
}
func (application *fakeApplication) ApplyPanelCutover(_ context.Context, _ string, request app.ControlPlaneCutoverRequest) error {
	application.cutoverApplies++
	application.cutoverRequest = request
	return nil
}
func (application *fakeApplication) PanelControlStatus(context.Context) (app.PanelControlStatus, error) {
	return app.PanelControlStatus{}, nil
}

func TestWizardCannotApplyBeforeDedicatedConfirmation(t *testing.T) {
	application := &fakeApplication{plan: install.ChangeSet{PlanID: "plan_test", Operations: []install.Operation{{Target: "/tmp/mlink", Action: install.ActionCreate}}}}
	model := New(application, fixtureRequest())
	model.width = 100
	model.token.SetValue("memorycore-token")

	model = advance(t, model, tea.KeyMsg{Type: tea.KeyEnter}) // detect
	model = advance(t, model, tea.KeyMsg{Type: tea.KeyEnter}) // provider
	model = advance(t, model, tea.KeyMsg{Type: tea.KeyEnter}) // connection
	model = advance(t, model, tea.KeyMsg{Type: tea.KeyEnter}) // identity candidates
	model = advance(t, model, tea.KeyMsg{Type: tea.KeySpace}) // select identity
	model = advance(t, model, tea.KeyMsg{Type: tea.KeyEnter}) // identity
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

func TestWizardRequiresExplicitOwnerSelection(t *testing.T) {
	application := &fakeApplication{candidates: []identity.Candidate{
		identity.NewCandidate("陈科良", "union_id", "on_owner", time.Unix(20, 0)),
		identity.NewCandidate("陈科良", "user_id", "u_other", time.Unix(10, 0)),
	}}
	model := New(application, fixtureRequest())
	model.token.SetValue("memorycore-token")
	model = driveToIdentity(t, model)
	model = advance(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	if model.step != StepIdentity || application.planCalls != 0 {
		t.Fatalf("auto-selected duplicate name: step=%d calls=%d", model.step, application.planCalls)
	}
	model = advance(t, model, tea.KeyMsg{Type: tea.KeySpace})
	model = advance(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	if model.step != StepAgents || model.request.OwnerBindingSlot.ID != "owner-feishu-union-1" || string(model.request.SecretInputs[app.OwnerBindingSecret]) != "on_owner" {
		t.Fatalf("selection = step:%d request:%#v", model.step, model.request)
	}
}

func TestWizardTrimsTokenBeforePlanning(t *testing.T) {
	application := &fakeApplication{plan: install.ChangeSet{PlanID: "plan_test"}}
	model := New(application, fixtureRequest())
	model.token.SetValue("  memorycore-token \r\n")
	model = driveToIdentity(t, model)
	model = advance(t, model, tea.KeyMsg{Type: tea.KeySpace})
	model = advance(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	model = advance(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	if got := string(application.planRequest.SecretInputs[app.MemoryCoreTokenSecret]); got != "memorycore-token" {
		t.Fatalf("planned token = %q", got)
	}
}

func TestWizardApplyErrorReturnsToCredentialEntry(t *testing.T) {
	application := &fakeApplication{
		plan:     install.ChangeSet{PlanID: "plan_test"},
		applyErr: errors.New("keychain unavailable"),
	}
	model := New(application, fixtureRequest())
	model.token.SetValue("memorycore-token")
	model = driveToIdentity(t, model)
	model = advance(t, model, tea.KeyMsg{Type: tea.KeySpace})
	model = advance(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	model = advance(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	model = advance(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	model = advance(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	model = advance(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	if model.step != StepConnection || model.confirmed || model.token.Value() != "" {
		t.Fatalf("step/confirmed/token = %d/%t/%q", model.step, model.confirmed, model.token.Value())
	}
}

func TestWizardUsesSeparatePanelAndCutoverConfirmations(t *testing.T) {
	application := &fakeApplication{
		panelPlan: install.ChangeSet{PlanID: "plan_stage_a"}, cutoverPlan: install.ChangeSet{PlanID: "plan_stage_b"},
	}
	model := New(application, fixtureRequest())
	model.step = StepVerify
	model = advance(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	if model.step != StepPanelPreview || model.panelPlan.PlanID != "plan_stage_a" {
		t.Fatalf("Panel preview = step:%d plan:%q", model.step, model.panelPlan.PlanID)
	}
	model = advance(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	model = advance(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	if application.panelApplies != 0 {
		t.Fatal("Panel applied before Enter")
	}
	model = advance(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	if model.step != StepCapacity || application.panelApplies != 1 {
		t.Fatalf("Panel apply = step:%d calls:%d", model.step, application.panelApplies)
	}
	model.capacity.SetValue("500")
	model = advance(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	if model.step != StepCutoverPreview || model.cutoverPlan.PlanID != "plan_stage_b" || model.cutoverPlan.PlanID == model.panelPlan.PlanID {
		t.Fatalf("cutover preview = step:%d plans:%q/%q", model.step, model.panelPlan.PlanID, model.cutoverPlan.PlanID)
	}
	model = advance(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	model = advance(t, model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	if application.cutoverApplies != 0 {
		t.Fatal("cutover applied before Enter")
	}
	model = advance(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	if model.step != StepComplete || application.cutoverApplies != 1 || application.cutoverRequest.DynamicAgentLimit != 500 {
		t.Fatalf("cutover apply = step:%d calls:%d request:%#v", model.step, application.cutoverApplies, application.cutoverRequest)
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

func driveToIdentity(t *testing.T, model Model) Model {
	t.Helper()
	model = advance(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	model = advance(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	model = advance(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	model = advance(t, model, tea.KeyMsg{Type: tea.KeyEnter})
	return model
}
