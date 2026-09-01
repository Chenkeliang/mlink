package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"slices"

	"mlink/internal/app"
	"mlink/internal/install"
)

type Application interface {
	PlanInstall(context.Context, app.InstallRequest) (install.ChangeSet, error)
	ApplyInstall(context.Context, string, app.InstallRequest) error
	PlanRestore(context.Context, app.RestoreRequest) (install.ChangeSet, error)
	ApplyRestore(context.Context, string, app.RestoreRequest) error
	PlanUninstall(context.Context, app.UninstallRequest) (install.ChangeSet, error)
	ApplyUninstall(context.Context, string, app.UninstallRequest) error
	PlanPanelProvision(context.Context) (install.ChangeSet, error)
	ApplyPanelProvision(context.Context, string) error
	PlanPanelCutover(context.Context, app.ControlPlaneCutoverRequest) (install.ChangeSet, error)
	ApplyPanelCutover(context.Context, string, app.ControlPlaneCutoverRequest) error
	PanelControlStatus(context.Context) (app.PanelControlStatus, error)
	OpenPanel(context.Context) error
	CopyPanelOwnerKey(context.Context) error
}

type Dependencies struct {
	Stdin          io.Reader
	Stdout         io.Writer
	Stderr         io.Writer
	App            Application
	InstallRequest app.InstallRequest
	ServeTencentDB func(context.Context) error
	ServeBroker    func(context.Context) error
	ServeMCP       func(context.Context) error
	RunCodexHook   func(context.Context, string) error
	RunCursorHook  func(context.Context, string) error
	RunTUI         func(context.Context) error
}

func Run(ctx context.Context, args []string, deps Dependencies) int {
	input := bufio.NewReader(deps.Stdin)
	if len(args) == 0 {
		if deps.RunTUI == nil || deps.RunTUI(ctx) != nil {
			writeLine(deps.Stderr, "mlink TUI failed")
			return 1
		}
		return 0
	}
	if slices.Equal(args, []string{"provider", "run", "tencentdb"}) {
		if deps.ServeTencentDB == nil || deps.ServeTencentDB(ctx) != nil {
			writeLine(deps.Stderr, "mlink provider command failed")
			return 1
		}
		return 0
	}
	if slices.Equal(args, []string{"broker", "serve"}) {
		if deps.ServeBroker == nil || deps.ServeBroker(ctx) != nil {
			writeLine(deps.Stderr, "mlink broker command failed")
			return 1
		}
		return 0
	}
	if slices.Equal(args, []string{"mcp", "serve"}) {
		if deps.ServeMCP == nil || deps.ServeMCP(ctx) != nil {
			writeLine(deps.Stderr, "mlink MCP command failed")
			return 1
		}
		return 0
	}
	if len(args) > 0 && args[0] == "install" {
		return runInstall(ctx, args[1:], deps, input)
	}
	if len(args) > 0 && args[0] == "backup" {
		return runBackup(ctx, args[1:], deps, input)
	}
	if len(args) > 0 && args[0] == "uninstall" {
		return runUninstall(ctx, args[1:], deps, input)
	}
	if len(args) > 0 && args[0] == "status" {
		return runStatus(ctx, args[1:], deps)
	}
	if len(args) > 0 && args[0] == "doctor" {
		return runDoctor(ctx, args[1:], deps)
	}
	if len(args) > 0 && args[0] == "config" {
		return runConfig(ctx, args[1:], deps)
	}
	if len(args) > 0 && args[0] == "identity" {
		return runIdentity(ctx, args[1:], deps, input)
	}
	if len(args) > 0 && args[0] == "panel" {
		return runPanel(ctx, args[1:], deps, input)
	}
	if len(args) > 0 && args[0] == "version" {
		return runVersion(args[1:], deps)
	}
	if len(args) > 0 && args[0] == "maintenance" {
		return runMaintenance(ctx, args[1:], deps, input)
	}
	if len(args) == 3 && args[0] == "hook" && args[1] == "codex" {
		if deps.RunCodexHook == nil || deps.RunCodexHook(ctx, args[2]) != nil {
			writeLine(deps.Stderr, "mlink codex hook failed")
			return 1
		}
		return 0
	}
	if len(args) == 3 && args[0] == "hook" && args[1] == "cursor" {
		if deps.RunCursorHook == nil || deps.RunCursorHook(ctx, args[2]) != nil {
			writeLine(deps.Stderr, "mlink cursor hook failed")
			return 1
		}
		return 0
	}
	if isPlannedCommand(args) {
		writeLine(deps.Stderr, "MLink command not implemented")
		return 2
	}
	writeLine(deps.Stderr, "unsupported MLink command")
	return 2
}

func isPlannedCommand(args []string) bool {
	for _, command := range [][]string{
		{"install"},
		{"status"},
		{"doctor"},
		{"backup"},
		{"hook", "codex"},
	} {
		if slices.Equal(args, command) {
			return true
		}
	}
	return false
}

func renderPlan(writer io.Writer, plan install.ChangeSet, jsonOutput bool) error {
	if jsonOutput {
		data, err := install.RenderJSON(plan)
		if err != nil {
			return err
		}
		_, err = writer.Write(data)
		return err
	}
	text, err := install.RenderText(plan)
	if err != nil {
		return err
	}
	_, err = io.WriteString(writer, text)
	return err
}

func writeLine(w io.Writer, message string) {
	if w != nil {
		_, _ = fmt.Fprintln(w, message)
	}
}
