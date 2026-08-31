package tui

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"mlink/internal/app"
	"mlink/internal/config"
	"mlink/internal/doctor"
	"mlink/internal/identity"
	"mlink/internal/install"
)

type Step int

const (
	StepWelcome Step = iota
	StepDetect
	StepProvider
	StepConnection
	StepIdentity
	StepAgents
	StepPreview
	StepApply
	StepVerify
	StepPanelPreview
	StepPanelApply
	StepCapacity
	StepCutoverPreview
	StepCutoverApply
	StepComplete
)

type Application interface {
	PlanInstall(context.Context, app.InstallRequest) (install.ChangeSet, error)
	ApplyInstall(context.Context, string, app.InstallRequest) error
	Status(context.Context) (app.Status, error)
	Doctor(context.Context, []app.Agent) (doctor.Report, error)
	DetectIdentityCandidates(context.Context) ([]identity.Candidate, error)
	PlanPanelProvision(context.Context) (install.ChangeSet, error)
	ApplyPanelProvision(context.Context, string) error
	PlanPanelCutover(context.Context, app.ControlPlaneCutoverRequest) (install.ChangeSet, error)
	ApplyPanelCutover(context.Context, string, app.ControlPlaneCutoverRequest) error
	PanelControlStatus(context.Context) (app.PanelControlStatus, error)
}

type Model struct {
	application       Application
	request           app.InstallRequest
	step              Step
	width             int
	height            int
	cursor            int
	selected          map[app.Agent]bool
	token             textinput.Model
	capacity          textinput.Model
	status            app.Status
	plan              install.ChangeSet
	panelPlan         install.ChangeSet
	cutoverPlan       install.ChangeSet
	panelStatus       app.PanelControlStatus
	dynamicAgentLimit int
	report            doctor.Report
	candidates        []identity.Candidate
	identityCursor    int
	identitySelected  int
	confirmed         bool
	busy              bool
	err               error
}

type statusMsg struct {
	status app.Status
	err    error
}

type planMsg struct {
	plan install.ChangeSet
	err  error
}

type applyMsg struct{ err error }

type doctorMsg struct {
	report doctor.Report
	err    error
}

type candidatesMsg struct {
	values []identity.Candidate
	err    error
}

type panelPlanMsg struct {
	plan install.ChangeSet
	err  error
}

type panelApplyMsg struct{ err error }

type cutoverPlanMsg struct {
	plan install.ChangeSet
	err  error
}

type cutoverApplyMsg struct{ err error }

type panelStatusMsg struct {
	status app.PanelControlStatus
	err    error
}

func New(application Application, request app.InstallRequest) Model {
	token := textinput.New()
	token.Placeholder = "MemoryCore token"
	token.Prompt = "> "
	token.EchoMode = textinput.EchoPassword
	token.EchoCharacter = '•'
	token.CharLimit = 16 * 1024
	capacity := textinput.New()
	capacity.Placeholder = "500"
	capacity.Prompt = "> "
	capacity.CharLimit = 5
	return Model{
		application: application,
		request:     cloneRequest(request),
		width:       100,
		height:      30,
		selected: map[app.Agent]bool{
			app.Codex: true, app.Pi: true, app.Hermes: true,
		},
		token:            token,
		capacity:         capacity,
		identitySelected: -1,
	}
}

func (model Model) Init() tea.Cmd { return nil }

func (model Model) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	switch value := message.(type) {
	case tea.WindowSizeMsg:
		model.width, model.height = value.Width, value.Height
		return model, nil
	case statusMsg:
		model.busy = false
		model.status, model.err = value.status, value.err
		return model, nil
	case planMsg:
		model.busy = false
		model.plan, model.err = value.plan, value.err
		return model, nil
	case applyMsg:
		model.busy = false
		model.err = value.err
		model.wipeToken()
		if value.err == nil {
			model.step = StepVerify
			model.busy = true
			return model, model.doctorCommand()
		}
		model.confirmed = false
		model.step = StepConnection
		model.token.Focus()
		return model, textinput.Blink
	case doctorMsg:
		model.busy = false
		model.report, model.err = value.report, value.err
		return model, nil
	case candidatesMsg:
		model.busy = false
		model.candidates, model.err = value.values, value.err
		model.identityCursor = 0
		model.identitySelected = -1
		if value.err == nil && len(value.values) == 0 {
			model.err = errors.New("no Hermes Feishu identity candidates were detected")
		}
		return model, nil
	case panelPlanMsg:
		model.busy = false
		model.panelPlan, model.err = value.plan, value.err
		return model, nil
	case panelApplyMsg:
		model.busy = false
		model.err = value.err
		model.confirmed = false
		if value.err == nil {
			model.step = StepCapacity
			model.capacity.Focus()
			return model, textinput.Blink
		}
		return model, nil
	case cutoverPlanMsg:
		model.busy = false
		model.cutoverPlan, model.err = value.plan, value.err
		return model, nil
	case cutoverApplyMsg:
		model.busy = false
		model.err = value.err
		model.confirmed = false
		if value.err == nil {
			model.step = StepComplete
			model.busy = true
			return model, model.panelStatusCommand()
		}
		return model, nil
	case panelStatusMsg:
		model.busy = false
		model.panelStatus, model.err = value.status, value.err
		return model, nil
	case tea.KeyMsg:
		if value.String() == "ctrl+c" || value.String() == "q" && model.step != StepConnection {
			model.wipeToken()
			return model, tea.Quit
		}
		return model.updateKey(value)
	}
	return model, nil
}

func (model Model) updateKey(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	if model.busy {
		return model, nil
	}
	model.err = nil
	switch model.step {
	case StepWelcome:
		if key.Type == tea.KeyEnter {
			model.step = StepDetect
			model.busy = true
			return model, model.statusCommand()
		}
	case StepDetect:
		if key.Type == tea.KeyEnter {
			model.step = StepProvider
		}
	case StepProvider:
		if key.Type == tea.KeyEnter {
			model.step = StepConnection
			model.token.Focus()
			return model, textinput.Blink
		}
	case StepConnection:
		if key.Type == tea.KeyEnter {
			token := strings.TrimSpace(model.token.Value())
			if token == "" {
				model.err = errors.New("MemoryCore token is required")
				return model, nil
			}
			model.token.SetValue(token)
			model.token.Blur()
			model.step = StepIdentity
			model.busy = true
			return model, model.candidatesCommand()
		}
		var command tea.Cmd
		model.token, command = model.token.Update(key)
		return model, command
	case StepIdentity:
		switch key.String() {
		case "up", "k":
			if model.identityCursor > 0 {
				model.identityCursor--
			}
		case "down", "j":
			if model.identityCursor+1 < len(model.candidates) {
				model.identityCursor++
			}
		case " ":
			if len(model.candidates) != 0 {
				model.identitySelected = model.identityCursor
			}
		case "enter":
			if model.identitySelected < 0 || model.identitySelected >= len(model.candidates) {
				model.err = errors.New("select the Feishu identity that belongs to the local owner")
				return model, nil
			}
			if err := model.selectOwnerBinding(); err != nil {
				model.err = err
				return model, nil
			}
			model.step = StepAgents
		}
	case StepAgents:
		switch key.String() {
		case "up", "k":
			if model.cursor > 0 {
				model.cursor--
			}
		case "down", "j":
			if model.cursor < 2 {
				model.cursor++
			}
		case " ":
			agent := orderedAgents()[model.cursor]
			model.selected[agent] = !model.selected[agent]
		case "enter":
			if len(model.selectedAgents()) == 0 {
				model.err = errors.New("select at least one Agent")
				return model, nil
			}
			model.step = StepPreview
			model.busy = true
			return model, model.planCommand()
		}
	case StepPreview:
		if key.Type == tea.KeyEnter && model.err == nil && model.plan.PlanID != "" {
			model.step = StepApply
			model.confirmed = false
		}
	case StepApply:
		switch key.String() {
		case "y", "Y":
			model.confirmed = true
		case "n", "N", "esc":
			model.confirmed = false
			model.step = StepPreview
		case "enter":
			if model.confirmed {
				model.busy = true
				return model, model.applyCommand()
			}
		}
	case StepVerify:
		if key.String() == "r" {
			model.busy = true
			return model, model.doctorCommand()
		}
		if key.Type == tea.KeyEnter && model.err == nil {
			model.step = StepPanelPreview
			model.busy = true
			return model, model.panelPlanCommand()
		}
	case StepPanelPreview:
		if key.Type == tea.KeyEnter && model.err == nil && model.panelPlan.PlanID != "" {
			model.step = StepPanelApply
			model.confirmed = false
		}
	case StepPanelApply:
		return model.confirmationKey(key, StepPanelPreview, model.panelApplyCommand)
	case StepCapacity:
		if key.Type == tea.KeyEnter {
			limit, err := strconv.Atoi(strings.TrimSpace(model.capacity.Value()))
			if err != nil || limit <= 0 || limit > 10_000 {
				model.err = errors.New("enter a dynamic Agent limit between 1 and 10000")
				return model, nil
			}
			model.dynamicAgentLimit = limit
			model.capacity.Blur()
			model.step = StepCutoverPreview
			model.busy = true
			return model, model.cutoverPlanCommand()
		}
		var command tea.Cmd
		model.capacity, command = model.capacity.Update(key)
		return model, command
	case StepCutoverPreview:
		if key.Type == tea.KeyEnter && model.err == nil && model.cutoverPlan.PlanID != "" {
			model.step = StepCutoverApply
			model.confirmed = false
		}
	case StepCutoverApply:
		return model.confirmationKey(key, StepCutoverPreview, model.cutoverApplyCommand)
	case StepComplete:
		if key.String() == "r" {
			model.busy = true
			return model, model.panelStatusCommand()
		}
	}
	return model, nil
}

func (model Model) confirmationKey(key tea.KeyMsg, back Step, apply func() tea.Cmd) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "y", "Y":
		model.confirmed = true
	case "n", "N", "esc":
		model.confirmed = false
		model.step = back
	case "enter":
		if model.confirmed {
			model.busy = true
			return model, apply()
		}
	}
	return model, nil
}

func (model Model) statusCommand() tea.Cmd {
	return func() tea.Msg {
		status, err := model.application.Status(context.Background())
		return statusMsg{status: status, err: err}
	}
}

func (model Model) planCommand() tea.Cmd {
	request := model.installRequest()
	return func() tea.Msg {
		plan, err := model.application.PlanInstall(context.Background(), request)
		wipe(request.SecretInputs[app.MemoryCoreTokenSecret])
		wipe(request.SecretInputs[app.OwnerBindingSecret])
		return planMsg{plan: plan, err: err}
	}
}

func (model Model) applyCommand() tea.Cmd {
	request := model.installRequest()
	planID := model.plan.PlanID
	return func() tea.Msg {
		err := model.application.ApplyInstall(context.Background(), planID, request)
		wipe(request.SecretInputs[app.MemoryCoreTokenSecret])
		wipe(request.SecretInputs[app.OwnerBindingSecret])
		return applyMsg{err: err}
	}
}

func (model Model) candidatesCommand() tea.Cmd {
	return func() tea.Msg {
		values, err := model.application.DetectIdentityCandidates(context.Background())
		return candidatesMsg{values: values, err: err}
	}
}

func (model Model) doctorCommand() tea.Cmd {
	agents := model.selectedAgents()
	return func() tea.Msg {
		report, err := model.application.Doctor(context.Background(), agents)
		return doctorMsg{report: report, err: err}
	}
}

func (model Model) panelPlanCommand() tea.Cmd {
	return func() tea.Msg {
		plan, err := model.application.PlanPanelProvision(context.Background())
		return panelPlanMsg{plan: plan, err: err}
	}
}

func (model Model) panelApplyCommand() tea.Cmd {
	planID := model.panelPlan.PlanID
	return func() tea.Msg {
		return panelApplyMsg{err: model.application.ApplyPanelProvision(context.Background(), planID)}
	}
}

func (model Model) cutoverPlanCommand() tea.Cmd {
	request := app.ControlPlaneCutoverRequest{DynamicAgentLimit: model.dynamicAgentLimit}
	return func() tea.Msg {
		plan, err := model.application.PlanPanelCutover(context.Background(), request)
		return cutoverPlanMsg{plan: plan, err: err}
	}
}

func (model Model) cutoverApplyCommand() tea.Cmd {
	planID := model.cutoverPlan.PlanID
	request := app.ControlPlaneCutoverRequest{DynamicAgentLimit: model.dynamicAgentLimit}
	return func() tea.Msg {
		return cutoverApplyMsg{err: model.application.ApplyPanelCutover(context.Background(), planID, request)}
	}
}

func (model Model) panelStatusCommand() tea.Cmd {
	return func() tea.Msg {
		status, err := model.application.PanelControlStatus(context.Background())
		return panelStatusMsg{status: status, err: err}
	}
}

func (model Model) installRequest() app.InstallRequest {
	request := cloneRequest(model.request)
	request.Agents = model.selectedAgents()
	request.SecretInputs = map[string][]byte{app.MemoryCoreTokenSecret: []byte(model.token.Value())}
	if owner := model.request.SecretInputs[app.OwnerBindingSecret]; len(owner) != 0 {
		request.SecretInputs[app.OwnerBindingSecret] = append([]byte(nil), owner...)
	}
	return request
}

func (model *Model) wipeToken() {
	value := []byte(model.token.Value())
	wipe(value)
	model.token.SetValue("")
	if model.request.SecretInputs != nil {
		wipe(model.request.SecretInputs[app.MemoryCoreTokenSecret])
		delete(model.request.SecretInputs, app.MemoryCoreTokenSecret)
		wipe(model.request.SecretInputs[app.OwnerBindingSecret])
		delete(model.request.SecretInputs, app.OwnerBindingSecret)
	}
	for index := range model.candidates {
		model.candidates[index].Wipe()
	}
	model.candidates = nil
}

func (model *Model) selectOwnerBinding() error {
	candidate := model.candidates[model.identitySelected]
	token := map[string]string{"union_id": "union", "user_id": "user", "open_id": "open"}[candidate.Kind]
	if token == "" || len(candidate.Value) == 0 {
		return errors.New("selected Feishu identity candidate is invalid")
	}
	if model.request.SecretInputs == nil {
		model.request.SecretInputs = make(map[string][]byte)
	}
	wipe(model.request.SecretInputs[app.OwnerBindingSecret])
	model.request.SecretInputs[app.OwnerBindingSecret] = append([]byte(nil), candidate.Value...)
	slot := "owner-feishu-" + token + "-1"
	model.request.OwnerBindingSlot = config.BindingRef{
		ID: slot, Source: "feishu", Kind: candidate.Kind, PrincipalID: "owner",
		SecretRef: "keychain://dev.mlink/identity/binding/" + slot, Status: config.BindingActive,
	}
	return nil
}

func (model Model) selectedAgents() []app.Agent {
	var result []app.Agent
	for _, agent := range orderedAgents() {
		if model.selected[agent] {
			result = append(result, agent)
		}
	}
	return result
}

func orderedAgents() []app.Agent { return []app.Agent{app.Codex, app.Pi, app.Hermes} }

func cloneRequest(input app.InstallRequest) app.InstallRequest {
	output := input
	output.Agents = append([]app.Agent(nil), input.Agents...)
	output.SecretInputs = make(map[string][]byte, len(input.SecretInputs))
	for key, value := range input.SecretInputs {
		output.SecretInputs[key] = append([]byte(nil), value...)
	}
	return output
}

func wipe(value []byte) {
	for index := range value {
		value[index] = 0
	}
}
