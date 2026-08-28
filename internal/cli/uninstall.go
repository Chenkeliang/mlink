package cli

import (
	"bufio"
	"context"

	"mlink/internal/app"
)

func runUninstall(ctx context.Context, args []string, deps Dependencies, input *bufio.Reader) int {
	if deps.App == nil {
		writeLine(deps.Stderr, "mlink uninstall is unavailable")
		return 1
	}
	options, err := parseMutationOptions(args, false)
	if err != nil {
		writeLine(deps.Stderr, "invalid mlink uninstall arguments")
		return 2
	}
	agents, err := parseAgents(options.positionals)
	if err != nil {
		return 2
	}
	request := app.UninstallRequest{Agents: agents}
	plan, err := deps.App.PlanUninstall(ctx, request)
	if err != nil {
		writeLine(deps.Stderr, "mlink uninstall planning failed")
		return exitCodeFor(err)
	}
	if err := renderPlan(deps.Stdout, plan, options.json); err != nil {
		return 1
	}
	if options.dryRun || options.json && options.applyPlan == "" {
		return 0
	}
	if options.applyPlan != "" {
		if !options.yes || options.applyPlan != plan.PlanID {
			return 3
		}
	} else {
		confirmed, err := confirmApply(input, deps.Stdout)
		if err != nil || !confirmed {
			return 0
		}
	}
	if err := deps.App.ApplyUninstall(ctx, plan.PlanID, request); err != nil {
		writeLine(deps.Stderr, "mlink uninstall failed")
		return exitCodeFor(err)
	}
	return 0
}
