package tui

import (
	"context"
	"errors"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"mlink/internal/app"
	"mlink/internal/doctor"
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
)

type Application interface {
	PlanInstall(context.Context, app.InstallRequest) (install.ChangeSet, error)
	ApplyInstall(context.Context, string, app.InstallRequest) error
	Status(context.Context) (app.Status, error)
	Doctor(context.Context, []app.Agent) (doctor.Report, error)
}

type Model struct {
	application Application
	request     app.InstallRequest
	step        Step
	width       int
	height      int
	cursor      int
	selected    map[app.Agent]bool
	token       textinput.Model
	status      app.Status
	plan        install.ChangeSet
	report      doctor.Report
	confirmed   bool
	busy        bool
	err         error
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

func New(application Application, request app.InstallRequest) Model {
	token := textinput.New()
	token.Placeholder = "MemoryCore token"
	token.Prompt = "> "
	token.EchoMode = textinput.EchoPassword
	token.EchoCharacter = '•'
	token.CharLimit = 16 * 1024
	return Model{
		application: application,
		request:     cloneRequest(request),
		width:       100,
		height:      30,
		selected: map[app.Agent]bool{
			app.Codex: true, app.Pi: true, app.Hermes: true,
		},
		token: token,
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
		return model, nil
	case doctorMsg:
		model.busy = false
		model.report, model.err = value.report, value.err
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
			if model.token.Value() == "" {
				model.err = errors.New("MemoryCore token is required")
				return model, nil
			}
			model.token.Blur()
			model.step = StepIdentity
			return model, nil
		}
		var command tea.Cmd
		model.token, command = model.token.Update(key)
		return model, command
	case StepIdentity:
		if key.Type == tea.KeyEnter {
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
		return planMsg{plan: plan, err: err}
	}
}

func (model Model) applyCommand() tea.Cmd {
	request := model.installRequest()
	planID := model.plan.PlanID
	return func() tea.Msg {
		err := model.application.ApplyInstall(context.Background(), planID, request)
		wipe(request.SecretInputs[app.MemoryCoreTokenSecret])
		return applyMsg{err: err}
	}
}

func (model Model) doctorCommand() tea.Cmd {
	agents := model.selectedAgents()
	return func() tea.Msg {
		report, err := model.application.Doctor(context.Background(), agents)
		return doctorMsg{report: report, err: err}
	}
}

func (model Model) installRequest() app.InstallRequest {
	request := cloneRequest(model.request)
	request.Agents = model.selectedAgents()
	request.SecretInputs = map[string][]byte{app.MemoryCoreTokenSecret: []byte(model.token.Value())}
	return request
}

func (model *Model) wipeToken() {
	value := []byte(model.token.Value())
	wipe(value)
	model.token.SetValue("")
	if model.request.SecretInputs != nil {
		wipe(model.request.SecretInputs[app.MemoryCoreTokenSecret])
		delete(model.request.SecretInputs, app.MemoryCoreTokenSecret)
	}
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
