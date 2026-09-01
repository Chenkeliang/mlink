package tui

import (
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"mlink/internal/app"
	"mlink/internal/doctor"
	"mlink/internal/install"
)

var stepNames = []string{
	"Welcome", "Detect", "Provider", "Backend Detect", "Backend Setup", "Connection",
	"Identity", "Capacity", "Backend Preview", "Backend Confirm", "Core Identity Preview",
	"Core Identity Confirm", "Agents", "Install Preview", "Install Confirm", "Install Verify",
	"Memory Hub", "Hub Preview", "Hub Confirm", "Complete",
}

func (model Model) View() string {
	width := model.width
	if width <= 0 {
		width = 80
	}
	var lines []string
	lines = append(lines, model.logo(width)...)
	lines = append(lines, "")
	lines = append(lines, model.titleStyle().Render(fmt.Sprintf("%02d / %02d  %s", int(model.step)+1, len(stepNames), stepNames[model.step])))
	lines = append(lines, "")
	lines = append(lines, model.stepView(width)...)
	if model.busy {
		lines = append(lines, "", model.mutedStyle().Render("Working…"))
	}
	if model.err != nil {
		lines = append(lines, "", model.errorStyle().Render("Error: "+model.err.Error()))
	}
	lines = append(lines, "", model.mutedStyle().Render(model.footer()))
	for index, line := range lines {
		lines[index] = ansi.Truncate(line, width, "")
	}
	return strings.Join(lines, "\n")
}

func (model Model) stepView(width int) []string {
	switch model.step {
	case StepWelcome:
		return []string{
			"Connect one memory service to Codex, Pi and Hermes.",
			model.mutedStyle().Render("MLink never changes model URLs, subscriptions or Agent login."),
		}
	case StepDetect:
		state := "ready"
		if model.status.Installed {
			state = "installed"
		}
		return []string{
			model.row("Local environment", state),
			model.row("TencentDB MemoryCore", "ready"),
			model.row("Model configuration", "protected"),
		}
	case StepProvider:
		return []string{
			model.row("TencentDB MemoryCore", "selected"),
			model.mutedStyle().Render("Provider connector: dev.mlink.tencentdb · 0.1.0"),
		}
	case StepBackendDetect:
		state := string(model.backendStatus.State)
		if state == "" {
			state = "checking"
		}
		return []string{
			model.row("MemoryCore", state),
			model.row("Endpoint", model.backendStatus.Endpoint),
			model.mutedStyle().Render("Detection is read-only. No container or configuration is changed."),
		}
	case StepBackendMode:
		lines := []string{model.row("MemoryCore", string(model.backendStatus.State))}
		for index, option := range backendModes() {
			cursor := "  "
			if index == model.backendModeCursor {
				cursor = "> "
			}
			lines = append(lines, cursor+option)
		}
		return lines
	case StepConnection:
		labels := []string{"MemoryCore endpoint", "Gateway token"}
		fields := []string{model.endpoint.View(), model.token.View()}
		if model.needsLLMCredentials() {
			labels = append(labels, "Memory LLM base URL", "Memory LLM model", "Memory LLM API key")
			fields = append(fields, model.llmBaseURL.View(), model.llmModel.View(), model.llmAPIKey.View())
		}
		lines := []string{model.row("Credential storage", "macOS Keychain")}
		for index := range labels {
			cursor := "  "
			if index == model.connectionCursor {
				cursor = "> "
			}
			lines = append(lines, cursor+labels[index], "  "+fields[index])
		}
		return lines
	case StepIdentity:
		lines := []string{
			model.row("Owner", model.request.OwnerSlug),
			model.mutedStyle().Render("Select the Feishu identity that shares the local owner memory."),
		}
		for index, candidate := range model.candidates {
			cursor := "  "
			if index == model.identityCursor {
				cursor = "> "
			}
			selected := "[ ]"
			if index == model.identitySelected {
				selected = "[x]"
			}
			lines = append(lines, cursor+fmt.Sprintf("%s %s · %s · …%s", selected, candidate.DisplayName, candidate.Kind, candidate.Suffix))
		}
		lines = append(lines, model.mutedStyle().Render("s  Continue without Feishu (Hermes will be disabled)"))
		return lines
	case StepCapacity:
		return []string{
			"Set the maximum number of dynamically created private/group Agents.",
			model.mutedStyle().Render("This limit is stored with the permanent Core identity."), "", model.capacity.View(),
		}
	case StepBackendPreview:
		lines := model.planLines(model.backendPlan, width)
		return append(lines, "", model.okStyle().Render("Official MemoryCore is loopback-bound; model settings of every Agent stay unchanged."))
	case StepBackendApply:
		return model.confirmationLines(model.backendPlan.PlanID, "Installs or starts the pinned official MemoryCore runtime and retains its data volume.")
	case StepControlPreview:
		lines := model.planLines(model.controlPlan, width)
		return append(lines, "", model.okStyle().Render("MemoryCore creates the permanent Owner, Team, Agent and Asset IDs before the first memory write."))
	case StepControlApply:
		return model.confirmationLines(model.controlPlan.PlanID, "Creates permanent Core identity. A later Memory Hub install must reuse these exact IDs.")
	case StepAgents:
		var lines []string
		for index, agent := range orderedAgents() {
			cursor := "  "
			if index == model.cursor {
				cursor = "> "
			}
			selected := "off"
			if model.selected[agent] {
				selected = "selected"
			}
			lines = append(lines, cursor+model.row(agentLabel(agent), selected))
		}
		return lines
	case StepPreview:
		lines := []string{
			model.row("Plan", model.plan.PlanID),
			model.row("personal-owner", "Codex · Pi · owner Feishu DM · L1/L2/L3"),
			model.row("hermes-private", "other Feishu DMs · L1"),
			model.row("hermes-groups", "Feishu groups and topics · group L1"),
		}
		for _, operation := range model.plan.Operations {
			lines = append(lines, model.row(string(operation.Action), operation.Target))
			if len(lines) >= 12 && width < 100 {
				lines = append(lines, model.mutedStyle().Render("More operations are included in the same ChangeSet."))
				break
			}
		}
		lines = append(lines, "", model.okStyle().Render("Protected model and authentication fields remain unchanged."))
		return lines
	case StepApply:
		answer := "not confirmed"
		if model.confirmed {
			answer = "confirmed"
		}
		return []string{
			model.row("Exact plan", model.plan.PlanID),
			"Backups are written before any configuration change.",
			model.row("Apply", answer),
			model.mutedStyle().Render("Press y, then Enter. Press n or Esc to go back."),
		}
	case StepVerify:
		if len(model.report.Checks) == 0 {
			return []string{"No verification result yet."}
		}
		lines := make([]string, 0, len(model.report.Checks))
		for _, check := range model.report.Checks {
			state := check.Code
			if check.State == doctor.StatePassed {
				state = model.okStyle().Render(check.Code)
			}
			lines = append(lines, model.row(check.ID, state))
		}
		return lines
	case StepPanelMode:
		lines := []string{model.okStyle().Render("Core identity is already fixed; installing the Hub cannot replace it.")}
		for index, option := range panelModes() {
			cursor := "  "
			if index == model.panelModeCursor {
				cursor = "> "
			}
			lines = append(lines, cursor+option)
		}
		return lines
	case StepPanelPreview:
		lines := []string{
			model.row("Official Memory Hub", "selected"),
			model.row("Panel", "http://127.0.0.1:8125"),
			model.row("Knowledge", "http://127.0.0.1:8424"),
		}
		lines = append(lines, model.planLines(model.panelPlan, width)...)
		return append(lines, "", model.mutedStyle().Render("The Hub reuses existing Core IDs. Knowledge assets are not automatically imported or injected; routing is unchanged."))
	case StepPanelApply:
		return model.confirmationLines(model.panelPlan.PlanID, "Starts the official loopback Memory Hub over the already-provisioned Core identity.")
	case StepComplete:
		return []string{
			model.row("Control plane", model.panelStatus.ControlPlane.State),
			model.row("Identity source", "MemoryCore · fixed before first write"),
			model.row("Hub container", map[bool]string{true: "present", false: "missing"}[model.panelStatus.Panel.ContainerPresent]),
			model.row("Panel", map[bool]string{true: "healthy", false: "unavailable"}[model.panelStatus.Panel.PanelHealthy]),
			model.row("Knowledge", map[bool]string{true: "healthy", false: "unavailable"}[model.panelStatus.Panel.KnowledgeHealthy]),
			model.row("Memory instance", map[bool]string{true: "visible", false: "missing"}[model.panelStatus.Panel.InstanceVisible]),
		}
	default:
		return nil
	}
}

func (model Model) footer() string {
	switch model.step {
	case StepConnection:
		return "↑/↓ or Tab move · Enter next · Ctrl+C quit"
	case StepBackendDetect:
		return "r detect again · q quit"
	case StepBackendMode, StepPanelMode:
		return "↑/↓ move · Enter select · q quit"
	case StepIdentity:
		return "↑/↓ move · Space select · Enter continue · s skip Hermes · q quit"
	case StepAgents:
		return "↑/↓ move · Space toggle · Enter preview · q quit"
	case StepApply:
		return "y confirm · n back · Enter apply · q quit"
	case StepVerify:
		return "Enter optional Hub choice · r verify again · q quit"
	case StepBackendPreview, StepControlPreview, StepPanelPreview:
		return "Enter continue · q quit"
	case StepBackendApply, StepControlApply, StepPanelApply:
		return "y confirm · n back · Enter apply · q quit"
	case StepCapacity:
		return "Type limit · Enter preview · Ctrl+C quit"
	case StepComplete:
		return "r verify again · q quit"
	default:
		return "Enter continue · q quit"
	}
}

func (model Model) planLines(plan install.ChangeSet, width int) []string {
	lines := []string{model.row("Plan", plan.PlanID)}
	for _, operation := range plan.Operations {
		lines = append(lines, model.row(string(operation.Action), operation.Target))
		if len(lines) >= 10 && width < 100 {
			lines = append(lines, model.mutedStyle().Render("More operations are included in this exact ChangeSet."))
			break
		}
	}
	return lines
}

func (model Model) confirmationLines(planID, description string) []string {
	answer := "not confirmed"
	if model.confirmed {
		answer = "confirmed"
	}
	return []string{model.row("Exact plan", planID), description, model.row("Apply", answer), model.mutedStyle().Render("Press y, then Enter. Press n or Esc to go back.")}
}

func (model Model) row(label, value string) string {
	if model.width < 80 {
		return label + ": " + value
	}
	left := lipgloss.NewStyle().Width(26).Render(label)
	return lipgloss.JoinHorizontal(lipgloss.Top, left, value)
}

func (model Model) logo(width int) []string {
	if width < 80 {
		return []string{model.logoStyle().Render("MLink")}
	}
	patterns := map[rune][]string{
		'M': {"█   █", "██ ██", "█ █ █", "█   █", "█   █"},
		'L': {"█    ", "█    ", "█    ", "█    ", "█████"},
		'I': {"█████", "  █  ", "  █  ", "  █  ", "█████"},
		'N': {"█   █", "██  █", "█ █ █", "█  ██", "█   █"},
		'K': {"█  ██", "█ ██ ", "███  ", "█ ██ ", "█  ██"},
	}
	lines := make([]string, 5)
	for _, letter := range "MLINK" {
		for row := range lines {
			if lines[row] != "" {
				lines[row] += "  "
			}
			lines[row] += patterns[letter][row]
		}
	}
	for index := range lines {
		lines[index] = model.logoStyle().Render(lines[index])
	}
	return lines
}

func agentLabel(agent app.Agent) string {
	switch agent {
	case app.Codex:
		return "Codex Hooks"
	case app.Pi:
		return "Pi Extension"
	case app.Hermes:
		return "Hermes Memory Provider"
	case app.Cursor:
		return "Cursor Hooks + MCP"
	default:
		return string(agent)
	}
}

func (model Model) logoStyle() lipgloss.Style {
	return colorStyle(lipgloss.NewStyle().Bold(true), lipgloss.Color("220"))
}

func (model Model) titleStyle() lipgloss.Style {
	return colorStyle(lipgloss.NewStyle().Bold(true), lipgloss.Color("81"))
}

func (model Model) mutedStyle() lipgloss.Style {
	return colorStyle(lipgloss.NewStyle(), lipgloss.Color("245"))
}

func (model Model) okStyle() lipgloss.Style {
	return colorStyle(lipgloss.NewStyle(), lipgloss.Color("42"))
}

func (model Model) errorStyle() lipgloss.Style {
	return colorStyle(lipgloss.NewStyle(), lipgloss.Color("203"))
}

func colorStyle(style lipgloss.Style, color lipgloss.Color) lipgloss.Style {
	if os.Getenv("NO_COLOR") != "" {
		return style
	}
	return style.Foreground(color)
}
