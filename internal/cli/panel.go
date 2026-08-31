package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"strconv"
	"strings"

	"mlink/internal/app"
	"mlink/internal/install"
)

func runPanel(ctx context.Context, args []string, deps Dependencies, input *bufio.Reader) int {
	if deps.App == nil || len(args) == 0 {
		writeLine(deps.Stderr, "mlink panel command is unavailable")
		return 2
	}
	switch args[0] {
	case "provision":
		return runPanelMutation(ctx, args[1:], deps, input, func() (install.ChangeSet, error) {
			return deps.App.PlanPanelProvision(ctx)
		}, func(planID string) error { return deps.App.ApplyPanelProvision(ctx, planID) })
	case "cutover":
		limit, remaining, ok := panelAgentLimit(args[1:])
		if !ok {
			writeLine(deps.Stderr, "panel cutover requires --dynamic-agent-limit")
			return 2
		}
		request := app.ControlPlaneCutoverRequest{DynamicAgentLimit: limit}
		return runPanelMutation(ctx, remaining, deps, input, func() (install.ChangeSet, error) {
			return deps.App.PlanPanelCutover(ctx, request)
		}, func(planID string) error { return deps.App.ApplyPanelCutover(ctx, planID, request) })
	case "status", "doctor":
		if len(args) > 2 || len(args) == 2 && args[1] != "--json" {
			return 2
		}
		status, err := deps.App.PanelControlStatus(ctx)
		if err != nil {
			writeLine(deps.Stderr, "mlink panel status failed")
			return 1
		}
		if len(args) == 2 {
			data, _ := json.MarshalIndent(status, "", "  ")
			_, _ = deps.Stdout.Write(append(data, '\n'))
		} else {
			writeLine(deps.Stdout, "control-plane: "+status.ControlPlane.State)
			writeLine(deps.Stdout, "panel healthy: "+strconv.FormatBool(status.Panel.Healthy))
		}
		if args[0] == "doctor" && !status.Panel.Healthy {
			return 4
		}
		return 0
	case "open":
		if len(args) != 1 || deps.App.OpenPanel(ctx) != nil {
			writeLine(deps.Stderr, "mlink panel open failed")
			return 1
		}
		return 0
	case "copy-owner-key":
		if len(args) != 2 || args[1] != "--yes" {
			writeLine(deps.Stderr, "copy-owner-key requires --yes")
			return 2
		}
		writeLine(deps.Stdout, "Warning: the Owner key will remain in the system clipboard until replaced.")
		if deps.App.CopyPanelOwnerKey(ctx) != nil {
			writeLine(deps.Stderr, "mlink panel key copy failed")
			return 1
		}
		writeLine(deps.Stdout, "Owner key copied; paste it only into the local Panel login.")
		return 0
	default:
		return 2
	}
}

func runPanelMutation(ctx context.Context, args []string, deps Dependencies, input *bufio.Reader, plan func() (install.ChangeSet, error), apply func(string) error) int {
	options, err := parseMutationOptions(args, false)
	if err != nil || len(options.positionals) != 0 {
		writeLine(deps.Stderr, "invalid mlink panel arguments")
		return 2
	}
	changeSet, err := plan()
	if err != nil {
		writeLine(deps.Stderr, "mlink panel planning failed")
		return exitCodeFor(err)
	}
	if err := renderPlan(deps.Stdout, changeSet, options.json); err != nil {
		return 1
	}
	if options.dryRun || options.json && options.applyPlan == "" {
		return 0
	}
	if options.yes && options.applyPlan == "" {
		return 2
	}
	if options.applyPlan != "" {
		if !options.yes || options.applyPlan != changeSet.PlanID {
			return 3
		}
	} else {
		confirmed, err := confirmApply(input, deps.Stdout)
		if err != nil || !confirmed {
			return 0
		}
	}
	if err := apply(changeSet.PlanID); err != nil {
		writeLine(deps.Stderr, "mlink panel apply failed")
		return exitCodeFor(err)
	}
	return 0
}

func panelAgentLimit(args []string) (int, []string, bool) {
	remaining := make([]string, 0, len(args))
	limit := 0
	for index := 0; index < len(args); index++ {
		if args[index] != "--dynamic-agent-limit" {
			remaining = append(remaining, args[index])
			continue
		}
		index++
		if index >= len(args) || strings.HasPrefix(args[index], "--") {
			return 0, nil, false
		}
		parsed, err := strconv.Atoi(args[index])
		if err != nil || parsed <= 0 || parsed > 10_000 || limit != 0 {
			return 0, nil, false
		}
		limit = parsed
	}
	return limit, remaining, limit != 0
}
