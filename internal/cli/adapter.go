package cli

import (
	"context"
	"strings"

	"mlink/internal/install"
)

type cursorLifecycleApplication interface {
	PlanCursorEnable(context.Context) (install.ChangeSet, error)
	ApplyCursorEnable(context.Context, string) error
}

type claudeLifecycleApplication interface {
	PlanClaudeEnable(context.Context) (install.ChangeSet, error)
	ApplyClaudeEnable(context.Context, string) error
}

type adapterLifecycle struct {
	name  string
	plan  func(context.Context) (install.ChangeSet, error)
	apply func(context.Context, string) error
}

func runAdapter(ctx context.Context, args []string, deps Dependencies) int {
	if len(args) < 2 || args[0] != "enable" {
		return 2
	}
	lifecycle, ok := resolveAdapterLifecycle(args[1], deps)
	if !ok {
		return 2
	}
	if lifecycle.plan == nil {
		return 1
	}
	var applyPlan string
	var yes, dryRun, jsonOutput bool
	for index := 2; index < len(args); index++ {
		switch args[index] {
		case "--apply-plan":
			index++
			if index >= len(args) || strings.HasPrefix(args[index], "--") {
				return 2
			}
			applyPlan = args[index]
		case "--yes":
			yes = true
		case "--dry-run":
			dryRun = true
		case "--json":
			jsonOutput = true
		default:
			return 2
		}
	}
	if dryRun && applyPlan != "" || yes && applyPlan == "" {
		return 2
	}
	plan, err := lifecycle.plan(ctx)
	if err != nil {
		writeLine(deps.Stderr, "mlink "+lifecycle.name+" planning failed")
		return exitCodeFor(err)
	}
	if err := renderPlan(deps.Stdout, plan, jsonOutput); err != nil {
		return 1
	}
	if applyPlan == "" {
		return 0
	}
	if !yes || applyPlan != plan.PlanID {
		return 3
	}
	if err := lifecycle.apply(ctx, plan.PlanID); err != nil {
		writeLine(deps.Stderr, "mlink "+lifecycle.name+" enable failed")
		return exitCodeFor(err)
	}
	return 0
}

func resolveAdapterLifecycle(agent string, deps Dependencies) (adapterLifecycle, bool) {
	switch agent {
	case "cursor":
		application, ok := deps.App.(cursorLifecycleApplication)
		if !ok {
			return adapterLifecycle{name: "Cursor"}, true
		}
		return adapterLifecycle{name: "Cursor", plan: application.PlanCursorEnable, apply: application.ApplyCursorEnable}, true
	case "claude":
		application, ok := deps.App.(claudeLifecycleApplication)
		if !ok {
			return adapterLifecycle{name: "Claude Code"}, true
		}
		return adapterLifecycle{name: "Claude Code", plan: application.PlanClaudeEnable, apply: application.ApplyClaudeEnable}, true
	default:
		return adapterLifecycle{}, false
	}
}
