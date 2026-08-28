package tui

import (
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"mlink/internal/app"
	"mlink/internal/doctor"
)

var stepNames = []string{"Welcome", "Detect", "Provider", "Connection", "Identity", "Agents", "Preview", "Confirm", "Verify"}

func (model Model) View() string {
	width := model.width
	if width <= 0 {
		width = 80
	}
	var lines []string
	lines = append(lines, model.logo(width)...)
	lines = append(lines, "")
	lines = append(lines, model.titleStyle().Render(fmt.Sprintf("%02d / 09  %s", int(model.step)+1, stepNames[model.step])))
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
	case StepConnection:
		baseURL, _ := model.request.Connection.ProviderConfig["base_url"].(string)
		serviceID, _ := model.request.Connection.ProviderConfig["service_id"].(string)
		return []string{
			model.row("Endpoint", baseURL),
			model.row("Service", serviceID),
			model.row("Credential", "macOS Keychain"),
			"",
			model.token.View(),
		}
	case StepIdentity:
		return []string{
			model.row("Tenant", model.request.Connection.TenantID),
			model.row("Agent", model.request.Connection.AgentID),
			model.row("Local user", model.request.Connection.UserID),
			model.row("Hermes users", "Feishu stable ID → private hash"),
		}
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
		lines := []string{model.row("Plan", model.plan.PlanID)}
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
	default:
		return nil
	}
}

func (model Model) footer() string {
	switch model.step {
	case StepConnection:
		return "Enter continue · Ctrl+C quit"
	case StepAgents:
		return "↑/↓ move · Space toggle · Enter preview · q quit"
	case StepApply:
		return "y confirm · n back · Enter apply · q quit"
	case StepVerify:
		return "r verify again · q quit"
	default:
		return "Enter continue · q quit"
	}
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
