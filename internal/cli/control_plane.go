package cli

import (
	"context"
	"strconv"
	"strings"

	"mlink/internal/install"
)

type controlPlaneApplication interface {
	PlanControlPlaneProvision(context.Context, int) (install.ChangeSet, error)
	ApplyControlPlaneProvision(context.Context, string, int) error
}

func runControlPlane(ctx context.Context, args []string, deps Dependencies) int {
	application, ok := deps.App.(controlPlaneApplication)
	if !ok || len(args) == 0 || args[0] != "provision" {
		return 2
	}
	limit := 0
	var applyPlan string
	var yes, dryRun, jsonOutput bool
	for index := 1; index < len(args); index++ {
		switch args[index] {
		case "--dynamic-agent-limit", "--apply-plan":
			flag := args[index]
			index++
			if index >= len(args) || strings.HasPrefix(args[index], "--") {
				return 2
			}
			if flag == "--dynamic-agent-limit" {
				limit, _ = strconv.Atoi(args[index])
			} else {
				applyPlan = args[index]
			}
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
	if limit <= 0 || limit > 10_000 || dryRun && applyPlan != "" || yes && applyPlan == "" {
		return 2
	}
	plan, err := application.PlanControlPlaneProvision(ctx, limit)
	if err != nil {
		writeLine(deps.Stderr, "mlink control-plane planning failed")
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
	if err := application.ApplyControlPlaneProvision(ctx, plan.PlanID, limit); err != nil {
		writeLine(deps.Stderr, "mlink control-plane provision failed")
		return exitCodeFor(err)
	}
	return 0
}
