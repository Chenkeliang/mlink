package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"strings"

	"mlink/internal/app"
	"mlink/internal/install"
	"mlink/internal/journal"
)

type journalMaintenanceApplication interface {
	ListUnresolvedJournalEvents(context.Context) ([]app.JournalEventDescriptor, error)
	PlanJournalResolution(context.Context, app.JournalResolutionRequest) (install.ChangeSet, error)
	ApplyJournalResolution(context.Context, string, app.JournalResolutionRequest) error
}

type credentialMaintenanceApplication interface {
	PlanHermesGrantRotation(context.Context) (install.ChangeSet, error)
	ApplyHermesGrantRotation(context.Context, string) error
}

func runMaintenance(ctx context.Context, args []string, deps Dependencies, _ *bufio.Reader) int {
	if len(args) >= 3 && args[0] == "credentials" && args[1] == "rotate" && args[2] == "hermes-grant" {
		return runHermesGrantRotation(ctx, args[3:], deps)
	}
	if len(args) < 2 || args[0] != "journal" {
		writeLine(deps.Stderr, "invalid mlink maintenance command")
		return 2
	}
	application, ok := deps.App.(journalMaintenanceApplication)
	if !ok {
		writeLine(deps.Stderr, "mlink journal maintenance is unavailable")
		return 1
	}
	switch args[1] {
	case "list":
		return runJournalList(ctx, args[2:], deps, application)
	case "inspect":
		return runJournalInspect(ctx, args[2:], deps, application)
	case "discard", "acknowledge":
		return runJournalResolution(ctx, args[1], args[2:], deps, application)
	default:
		return 2
	}
}

func runHermesGrantRotation(ctx context.Context, args []string, deps Dependencies) int {
	application, ok := deps.App.(credentialMaintenanceApplication)
	if !ok {
		writeLine(deps.Stderr, "mlink credential maintenance is unavailable")
		return 1
	}
	var applyPlan string
	var yes, jsonOutput, dryRun bool
	for index := 0; index < len(args); index++ {
		switch args[index] {
		case "--apply-plan":
			index++
			if index >= len(args) || strings.HasPrefix(args[index], "--") {
				return 2
			}
			applyPlan = args[index]
		case "--yes":
			yes = true
		case "--json":
			jsonOutput = true
		case "--dry-run":
			dryRun = true
		default:
			return 2
		}
	}
	if dryRun && applyPlan != "" || yes && applyPlan == "" {
		return 2
	}
	plan, err := application.PlanHermesGrantRotation(ctx)
	if err != nil {
		writeLine(deps.Stderr, "mlink Hermes grant rotation planning failed")
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
	if err := application.ApplyHermesGrantRotation(ctx, plan.PlanID); err != nil {
		writeLine(deps.Stderr, "mlink Hermes grant rotation failed")
		return exitCodeFor(err)
	}
	return 0
}

func runJournalList(ctx context.Context, args []string, deps Dependencies, application journalMaintenanceApplication) int {
	jsonOutput := len(args) == 1 && args[0] == "--json"
	if len(args) != 0 && !jsonOutput {
		return 2
	}
	events, err := application.ListUnresolvedJournalEvents(ctx)
	if err != nil {
		writeLine(deps.Stderr, "mlink journal list failed")
		return 1
	}
	if jsonOutput {
		encoder := json.NewEncoder(deps.Stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(events); err != nil {
			return 1
		}
		return 0
	}
	for _, event := range events {
		writeLine(deps.Stdout, event.String())
	}
	return 0
}

func runJournalInspect(ctx context.Context, args []string, deps Dependencies, application journalMaintenanceApplication) int {
	jsonOutput := len(args) == 2 && args[1] == "--json"
	if (len(args) != 1 && !jsonOutput) || strings.HasPrefix(args[0], "-") || len(args[0]) < 8 {
		return 2
	}
	events, err := application.ListUnresolvedJournalEvents(ctx)
	if err != nil {
		return 1
	}
	var matches []app.JournalEventDescriptor
	for _, event := range events {
		if strings.HasSuffix(event.EventSuffix, args[0]) || event.EventSuffix == args[0] {
			matches = append(matches, event)
		}
	}
	if len(matches) != 1 {
		return 1
	}
	if jsonOutput {
		encoder := json.NewEncoder(deps.Stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(matches[0]); err != nil {
			return 1
		}
	} else {
		writeLine(deps.Stdout, matches[0].String())
	}
	return 0
}

func runJournalResolution(ctx context.Context, command string, args []string, deps Dependencies, application journalMaintenanceApplication) int {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return 2
	}
	request := app.JournalResolutionRequest{EventRef: args[0], Resolution: journal.ResolutionDiscarded}
	if command == "acknowledge" {
		request.Resolution = journal.ResolutionDelivered
	}
	var applyPlan string
	var yes, jsonOutput, dryRun bool
	for index := 1; index < len(args); index++ {
		switch args[index] {
		case "--reason", "--provider-ref", "--apply-plan":
			flag := args[index]
			index++
			if index >= len(args) || strings.HasPrefix(args[index], "--") {
				return 2
			}
			switch flag {
			case "--reason":
				request.Reason = args[index]
			case "--provider-ref":
				request.ProviderRef = args[index]
			case "--apply-plan":
				applyPlan = args[index]
			}
		case "--yes":
			yes = true
		case "--json":
			jsonOutput = true
		case "--dry-run":
			dryRun = true
		default:
			return 2
		}
	}
	if strings.TrimSpace(request.Reason) == "" || (request.Resolution == journal.ResolutionDelivered) != (strings.TrimSpace(request.ProviderRef) != "") || dryRun && applyPlan != "" || yes && applyPlan == "" {
		return 2
	}
	plan, err := application.PlanJournalResolution(ctx, request)
	if err != nil {
		writeLine(deps.Stderr, "mlink journal resolution planning failed")
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
	if err := application.ApplyJournalResolution(ctx, plan.PlanID, request); err != nil {
		writeLine(deps.Stderr, "mlink journal resolution failed")
		return exitCodeFor(err)
	}
	return 0
}
