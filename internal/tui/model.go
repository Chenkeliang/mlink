package tui

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"mlink/internal/app"
	"mlink/internal/config"
	"mlink/internal/doctor"
	"mlink/internal/identity"
	"mlink/internal/install"
	"mlink/internal/provider/lifecycle"
)

type Step int

const (
	StepWelcome Step = iota
	StepDetect
	StepProvider
	StepBackendDetect
	StepBackendMode
	StepConnection
	StepIdentity
	StepCapacity
	StepBackendPreview
	StepBackendApply
	StepControlPreview
	StepControlApply
	StepAgents
	StepPreview
	StepApply
	StepVerify
	StepPanelMode
	StepPanelPreview
	StepPanelApply
	StepComplete
)

type Application interface {
	PlanInstall(context.Context, app.InstallRequest) (install.ChangeSet, error)
	ApplyInstall(context.Context, string, app.InstallRequest) error
	Status(context.Context) (app.Status, error)
	Doctor(context.Context, []app.Agent) (doctor.Report, error)
	DetectIdentityCandidates(context.Context) ([]identity.Candidate, error)
	PanelControlStatus(context.Context) (app.PanelControlStatus, error)
	ProviderStatus(context.Context) (lifecycle.BackendStatus, error)
	PlanProviderInstall(context.Context, lifecycle.BackendInstallRequest) (install.ChangeSet, error)
	ApplyProviderInstall(context.Context, string, lifecycle.BackendInstallRequest) error
	PlanControlPlaneProvision(context.Context, int) (install.ChangeSet, error)
	ApplyControlPlaneProvision(context.Context, string, int) error
	PlanPanelRuntime(context.Context) (install.ChangeSet, error)
	ApplyPanelRuntime(context.Context, string) error
}

type Model struct {
	application       Application
	request           app.InstallRequest
	step              Step
	width             int
	height            int
	cursor            int
	selected          map[app.Agent]bool
	endpoint          textinput.Model
	token             textinput.Model
	llmBaseURL        textinput.Model
	llmModel          textinput.Model
	llmAPIKey         textinput.Model
	capacity          textinput.Model
	status            app.Status
	plan              install.ChangeSet
	panelPlan         install.ChangeSet
	panelStatus       app.PanelControlStatus
	backendStatus     lifecycle.BackendStatus
	backendPlan       install.ChangeSet
	controlPlan       install.ChangeSet
	installBackend    bool
	backendModeCursor int
	connectionCursor  int
	panelModeCursor   int
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
type backendStatusMsg struct {
	status lifecycle.BackendStatus
	err    error
}
type backendPlanMsg struct {
	plan install.ChangeSet
	err  error
}
type backendApplyMsg struct{ err error }
type controlPlanMsg struct {
	plan install.ChangeSet
	err  error
}
type controlApplyMsg struct{ err error }
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
type panelStatusMsg struct {
	status app.PanelControlStatus
	err    error
}

func New(application Application, request app.InstallRequest) Model {
	endpoint := textinput.New()
	endpoint.Placeholder = "http://127.0.0.1:8420"
	endpoint.Prompt = "> "
	endpoint.CharLimit = 2048
	if value, ok := request.Connection.ProviderConfig["base_url"].(string); ok {
		endpoint.SetValue(strings.TrimSpace(value))
	}
	token := passwordInput("MemoryCore gateway token")
	llmBaseURL := textinput.New()
	llmBaseURL.Placeholder = "Memory LLM base URL"
	llmBaseURL.Prompt = "> "
	llmBaseURL.CharLimit = 2048
	llmBaseURL.SetValue(strings.TrimSpace(os.Getenv("MLINK_MEMORY_LLM_BASE_URL")))
	llmModel := textinput.New()
	llmModel.Placeholder = "Memory LLM model"
	llmModel.Prompt = "> "
	llmModel.CharLimit = 256
	llmModel.SetValue(strings.TrimSpace(os.Getenv("MLINK_MEMORY_LLM_MODEL")))
	llmAPIKey := passwordInput("Memory LLM API key")
	capacity := textinput.New()
	capacity.Placeholder = "500"
	capacity.Prompt = "> "
	capacity.CharLimit = 5
	if request.DynamicAgentLimit > 0 {
		capacity.SetValue(strconv.Itoa(request.DynamicAgentLimit))
	}
	return Model{
		application: application, request: cloneRequest(request), width: 100, height: 30,
		selected: map[app.Agent]bool{app.Codex: true, app.Pi: true, app.Hermes: true, app.Cursor: true},
		endpoint: endpoint, token: token, llmBaseURL: llmBaseURL, llmModel: llmModel,
		llmAPIKey: llmAPIKey, capacity: capacity, identitySelected: -1,
	}
}

func passwordInput(placeholder string) textinput.Model {
	input := textinput.New()
	input.Placeholder = placeholder
	input.Prompt = "> "
	input.EchoMode = textinput.EchoPassword
	input.EchoCharacter = '•'
	input.CharLimit = 16 * 1024
	return input
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
	case backendStatusMsg:
		model.busy = false
		model.backendStatus, model.err = value.status, value.err
		if value.err != nil {
			return model, nil
		}
		switch value.status.State {
		case lifecycle.BackendReachable:
			model.installBackend = false
			model.step = StepConnection
			model.connectionCursor = 1
			return model, model.focusConnectionField()
		case lifecycle.BackendAbsent, lifecycle.BackendStopped:
			model.step = StepBackendMode
			model.backendModeCursor = 0
		default:
			model.err = errors.New("selected memory backend is not compatible or reachable")
		}
		return model, nil
	case backendPlanMsg:
		model.busy = false
		model.backendPlan, model.err = value.plan, value.err
		return model, nil
	case backendApplyMsg:
		model.busy = false
		model.err = value.err
		model.confirmed = false
		if value.err == nil {
			model.wipeLLMKey()
			model.step = StepControlPreview
			model.busy = true
			return model, model.controlPlanCommand()
		}
		return model, nil
	case controlPlanMsg:
		model.busy = false
		model.controlPlan, model.err = value.plan, value.err
		return model, nil
	case controlApplyMsg:
		model.busy = false
		model.err = value.err
		model.confirmed = false
		if value.err == nil {
			model.step = StepAgents
		}
		return model, nil
	case planMsg:
		model.busy = false
		model.plan, model.err = value.plan, value.err
		return model, nil
	case applyMsg:
		model.busy = false
		model.err = value.err
		model.wipeConnectionSecrets()
		if value.err == nil {
			model.step = StepVerify
			model.busy = true
			return model, model.doctorCommand()
		}
		model.confirmed = false
		model.step = StepConnection
		model.connectionCursor = 1
		return model, model.focusConnectionField()
	case doctorMsg:
		model.busy = false
		model.report, model.err = value.report, value.err
		return model, nil
	case candidatesMsg:
		model.busy = false
		model.candidates, model.err = value.values, value.err
		model.identityCursor, model.identitySelected = 0, -1
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
			model.wipeSecrets()
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
			model.step, model.busy = StepDetect, true
			return model, model.statusCommand()
		}
	case StepDetect:
		if key.Type == tea.KeyEnter {
			model.step = StepProvider
		}
	case StepProvider:
		if key.Type == tea.KeyEnter {
			model.step, model.busy = StepBackendDetect, true
			return model, model.backendStatusCommand()
		}
	case StepBackendDetect:
		if key.String() == "r" || key.Type == tea.KeyEnter {
			model.busy = true
			return model, model.backendStatusCommand()
		}
	case StepBackendMode:
		switch key.String() {
		case "up", "k":
			if model.backendModeCursor > 0 {
				model.backendModeCursor--
			}
		case "down", "j":
			if model.backendModeCursor+1 < len(backendModes()) {
				model.backendModeCursor++
			}
		case "enter":
			model.installBackend = model.backendModeCursor == 0
			model.step, model.connectionCursor = StepConnection, 0
			return model, model.focusConnectionField()
		}
	case StepConnection:
		return model.updateConnection(key)
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
			model.step = StepCapacity
			model.capacity.Focus()
			return model, textinput.Blink
		}
	case StepCapacity:
		if key.Type == tea.KeyEnter {
			limit, err := strconv.Atoi(strings.TrimSpace(model.capacity.Value()))
			if err != nil || limit <= 0 || limit > 10_000 {
				model.err = errors.New("enter a dynamic Agent limit between 1 and 10000")
				return model, nil
			}
			model.dynamicAgentLimit, model.request.DynamicAgentLimit = limit, limit
			model.capacity.Blur()
			model.busy = true
			if model.installBackend {
				model.step = StepBackendPreview
				return model, model.backendPlanCommand()
			}
			model.step = StepControlPreview
			return model, model.controlPlanCommand()
		}
		var command tea.Cmd
		model.capacity, command = model.capacity.Update(key)
		return model, command
	case StepBackendPreview:
		if key.Type == tea.KeyEnter && model.err == nil && model.backendPlan.PlanID != "" {
			model.step, model.confirmed = StepBackendApply, false
		}
	case StepBackendApply:
		return model.confirmationKey(key, StepBackendPreview, model.backendApplyCommand)
	case StepControlPreview:
		if key.Type == tea.KeyEnter && model.err == nil && model.controlPlan.PlanID != "" {
			model.step, model.confirmed = StepControlApply, false
		}
	case StepControlApply:
		return model.confirmationKey(key, StepControlPreview, model.controlApplyCommand)
	case StepAgents:
		switch key.String() {
		case "up", "k":
			if model.cursor > 0 {
				model.cursor--
			}
		case "down", "j":
			if model.cursor+1 < len(orderedAgents()) {
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
			model.step, model.busy = StepPreview, true
			return model, model.planCommand()
		}
	case StepPreview:
		if key.Type == tea.KeyEnter && model.err == nil && model.plan.PlanID != "" {
			model.step, model.confirmed = StepApply, false
		}
	case StepApply:
		return model.confirmationKey(key, StepPreview, model.applyCommand)
	case StepVerify:
		if key.String() == "r" {
			model.busy = true
			return model, model.doctorCommand()
		}
		if key.Type == tea.KeyEnter && model.err == nil {
			model.step, model.panelModeCursor = StepPanelMode, 0
		}
	case StepPanelMode:
		switch key.String() {
		case "up", "k":
			if model.panelModeCursor > 0 {
				model.panelModeCursor--
			}
		case "down", "j":
			if model.panelModeCursor+1 < len(panelModes()) {
				model.panelModeCursor++
			}
		case "enter":
			if model.panelModeCursor == 1 {
				model.step, model.busy = StepComplete, true
				return model, model.panelStatusCommand()
			}
			model.step, model.busy = StepPanelPreview, true
			return model, model.panelPlanCommand()
		}
	case StepPanelPreview:
		if key.Type == tea.KeyEnter && model.err == nil && model.panelPlan.PlanID != "" {
			model.step, model.confirmed = StepPanelApply, false
		}
	case StepPanelApply:
		return model.confirmationKey(key, StepPanelPreview, model.panelApplyCommand)
	case StepComplete:
		if key.String() == "r" {
			model.busy = true
			return model, model.panelStatusCommand()
		}
	}
	return model, nil
}

func (model Model) updateConnection(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	fields := model.connectionFields()
	switch key.String() {
	case "up", "shift+tab":
		if model.connectionCursor > 0 {
			model.connectionCursor--
		}
		return model, model.focusConnectionField()
	case "down", "tab":
		if model.connectionCursor+1 < len(fields) {
			model.connectionCursor++
		}
		return model, model.focusConnectionField()
	case "enter":
		if model.connectionCursor+1 < len(fields) {
			model.connectionCursor++
			return model, model.focusConnectionField()
		}
		if err := model.validateConnectionInputs(); err != nil {
			model.err = err
			return model, nil
		}
		model.blurConnectionFields()
		model.step, model.busy = StepIdentity, true
		return model, model.candidatesCommand()
	}
	field := fields[model.connectionCursor]
	updated, command := field.Update(key)
	model.setConnectionField(model.connectionCursor, updated)
	return model, command
}

func (model Model) connectionFields() []textinput.Model {
	fields := []textinput.Model{model.endpoint, model.token}
	if model.installBackend {
		fields = append(fields, model.llmBaseURL, model.llmModel, model.llmAPIKey)
	}
	return fields
}

func (model *Model) setConnectionField(index int, value textinput.Model) {
	switch index {
	case 0:
		model.endpoint = value
	case 1:
		model.token = value
	case 2:
		model.llmBaseURL = value
	case 3:
		model.llmModel = value
	case 4:
		model.llmAPIKey = value
	}
}

func (model *Model) blurConnectionFields() {
	model.endpoint.Blur()
	model.token.Blur()
	model.llmBaseURL.Blur()
	model.llmModel.Blur()
	model.llmAPIKey.Blur()
}

func (model *Model) focusConnectionField() tea.Cmd {
	model.blurConnectionFields()
	fields := model.connectionFields()
	if model.connectionCursor < 0 || model.connectionCursor >= len(fields) {
		model.connectionCursor = 0
	}
	fields[model.connectionCursor].Focus()
	model.setConnectionField(model.connectionCursor, fields[model.connectionCursor])
	return textinput.Blink
}

func (model *Model) validateConnectionInputs() error {
	endpoint, token := strings.TrimSpace(model.endpoint.Value()), strings.TrimSpace(model.token.Value())
	if endpoint == "" {
		return errors.New("MemoryCore endpoint is required")
	}
	if token == "" {
		return errors.New("MemoryCore gateway token is required")
	}
	model.endpoint.SetValue(endpoint)
	model.token.SetValue(token)
	if model.installBackend {
		baseURL, llmModel, apiKey := strings.TrimSpace(model.llmBaseURL.Value()), strings.TrimSpace(model.llmModel.Value()), strings.TrimSpace(model.llmAPIKey.Value())
		if baseURL == "" || llmModel == "" || apiKey == "" {
			return errors.New("local MemoryCore install requires the memory LLM base URL, model, and API key")
		}
		model.llmBaseURL.SetValue(baseURL)
		model.llmModel.SetValue(llmModel)
		model.llmAPIKey.SetValue(apiKey)
	}
	if model.request.Connection.ProviderConfig == nil {
		model.request.Connection.ProviderConfig = make(map[string]any)
	}
	model.request.Connection.ProviderConfig["base_url"] = endpoint
	return nil
}

func (model Model) confirmationKey(key tea.KeyMsg, back Step, apply func() tea.Cmd) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "y", "Y":
		model.confirmed = true
	case "n", "N", "esc":
		model.confirmed, model.step = false, back
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
		return statusMsg{status, err}
	}
}
func (model Model) backendStatusCommand() tea.Cmd {
	return func() tea.Msg {
		status, err := model.application.ProviderStatus(context.Background())
		return backendStatusMsg{status, err}
	}
}
func (model Model) backendPlanCommand() tea.Cmd {
	request := model.backendInstallRequest()
	return func() tea.Msg {
		defer request.Wipe()
		plan, err := model.application.PlanProviderInstall(context.Background(), request)
		return backendPlanMsg{plan, err}
	}
}
func (model Model) backendApplyCommand() tea.Cmd {
	request, planID := model.backendInstallRequest(), model.backendPlan.PlanID
	return func() tea.Msg {
		defer request.Wipe()
		return backendApplyMsg{model.application.ApplyProviderInstall(context.Background(), planID, request)}
	}
}
func (model Model) controlPlanCommand() tea.Cmd {
	limit := model.dynamicAgentLimit
	return func() tea.Msg {
		plan, err := model.application.PlanControlPlaneProvision(context.Background(), limit)
		return controlPlanMsg{plan, err}
	}
}
func (model Model) controlApplyCommand() tea.Cmd {
	planID, limit := model.controlPlan.PlanID, model.dynamicAgentLimit
	return func() tea.Msg {
		return controlApplyMsg{model.application.ApplyControlPlaneProvision(context.Background(), planID, limit)}
	}
}
func (model Model) planCommand() tea.Cmd {
	request := model.installRequest()
	return func() tea.Msg {
		defer wipeInstallRequest(&request)
		plan, err := model.application.PlanInstall(context.Background(), request)
		return planMsg{plan, err}
	}
}
func (model Model) applyCommand() tea.Cmd {
	request, planID := model.installRequest(), model.plan.PlanID
	return func() tea.Msg {
		defer wipeInstallRequest(&request)
		return applyMsg{model.application.ApplyInstall(context.Background(), planID, request)}
	}
}
func (model Model) candidatesCommand() tea.Cmd {
	return func() tea.Msg {
		values, err := model.application.DetectIdentityCandidates(context.Background())
		return candidatesMsg{values, err}
	}
}
func (model Model) doctorCommand() tea.Cmd {
	agents := model.selectedAgents()
	return func() tea.Msg {
		report, err := model.application.Doctor(context.Background(), agents)
		return doctorMsg{report, err}
	}
}
func (model Model) panelPlanCommand() tea.Cmd {
	return func() tea.Msg {
		plan, err := model.application.PlanPanelRuntime(context.Background())
		return panelPlanMsg{plan, err}
	}
}
func (model Model) panelApplyCommand() tea.Cmd {
	planID := model.panelPlan.PlanID
	return func() tea.Msg {
		return panelApplyMsg{model.application.ApplyPanelRuntime(context.Background(), planID)}
	}
}
func (model Model) panelStatusCommand() tea.Cmd {
	return func() tea.Msg {
		status, err := model.application.PanelControlStatus(context.Background())
		return panelStatusMsg{status, err}
	}
}

func (model Model) backendInstallRequest() lifecycle.BackendInstallRequest {
	return lifecycle.BackendInstallRequest{
		ProviderID: "dev.mlink.tencentdb", Endpoint: strings.TrimSpace(model.endpoint.Value()), GatewayToken: []byte(strings.TrimSpace(model.token.Value())),
		LLMBaseURL: strings.TrimSpace(model.llmBaseURL.Value()), LLMModel: strings.TrimSpace(model.llmModel.Value()), LLMAPIKey: []byte(strings.TrimSpace(model.llmAPIKey.Value())),
	}
}

func (model Model) installRequest() app.InstallRequest {
	request := cloneRequest(model.request)
	request.Agents, request.DynamicAgentLimit = model.selectedAgents(), model.dynamicAgentLimit
	request.SecretInputs = map[string][]byte{app.MemoryCoreTokenSecret: []byte(model.token.Value())}
	if owner := model.request.SecretInputs[app.OwnerBindingSecret]; len(owner) != 0 {
		request.SecretInputs[app.OwnerBindingSecret] = append([]byte(nil), owner...)
	}
	return request
}

func (model *Model) wipeLLMKey() {
	value := []byte(model.llmAPIKey.Value())
	wipe(value)
	model.llmAPIKey.SetValue("")
}
func (model *Model) wipeConnectionSecrets() {
	value := []byte(model.token.Value())
	wipe(value)
	model.token.SetValue("")
	model.wipeLLMKey()
}
func (model *Model) wipeSecrets() {
	model.wipeConnectionSecrets()
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
	model.request.OwnerBindingSlot = config.BindingRef{ID: slot, Source: "feishu", Kind: candidate.Kind, PrincipalID: "owner", SecretRef: "keychain://dev.mlink/identity/binding/" + slot, Status: config.BindingActive}
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

func backendModes() []string {
	return []string{"Install official local MemoryCore", "Connect an existing MemoryCore"}
}
func panelModes() []string       { return []string{"Install optional Memory Hub", "Skip for now"} }
func orderedAgents() []app.Agent { return []app.Agent{app.Codex, app.Pi, app.Hermes, app.Cursor} }

func cloneRequest(input app.InstallRequest) app.InstallRequest {
	output := input
	output.Agents = append([]app.Agent(nil), input.Agents...)
	output.Connection.ProviderConfig = make(map[string]any, len(input.Connection.ProviderConfig))
	for key, value := range input.Connection.ProviderConfig {
		output.Connection.ProviderConfig[key] = value
	}
	output.Connection.SecretRefs = make(map[string]string, len(input.Connection.SecretRefs))
	for key, value := range input.Connection.SecretRefs {
		output.Connection.SecretRefs[key] = value
	}
	output.SecretInputs = make(map[string][]byte, len(input.SecretInputs))
	for key, value := range input.SecretInputs {
		output.SecretInputs[key] = append([]byte(nil), value...)
	}
	return output
}

func wipeInstallRequest(request *app.InstallRequest) {
	for key := range request.SecretInputs {
		wipe(request.SecretInputs[key])
	}
}
func wipe(value []byte) {
	for index := range value {
		value[index] = 0
	}
}
