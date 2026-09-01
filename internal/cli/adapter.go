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

func runAdapter(ctx context.Context, args []string, deps Dependencies) int {
	if len(args) < 2 || args[0] != "enable" || args[1] != "cursor" {
		return 2
	}
	application, ok := deps.App.(cursorLifecycleApplication)
	if !ok {
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
	plan, err := application.PlanCursorEnable(ctx)
	if err != nil {
		writeLine(deps.Stderr, "mlink Cursor planning failed")
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
	if err := application.ApplyCursorEnable(ctx, plan.PlanID); err != nil {
		writeLine(deps.Stderr, "mlink Cursor enable failed")
		return exitCodeFor(err)
	}
	return 0
}
