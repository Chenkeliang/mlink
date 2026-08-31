package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"mlink/internal/app"
	"mlink/internal/install"
)

type identityApplication interface {
	PlanIdentityBind(context.Context, app.IdentityBindRequest) (install.ChangeSet, error)
	ApplyIdentityBind(context.Context, string, app.IdentityBindRequest) error
	PlanIdentityRebind(context.Context, app.IdentityRebindRequest) (install.ChangeSet, error)
	ApplyIdentityRebind(context.Context, string, app.IdentityRebindRequest) error
	PlanIdentityRevoke(context.Context, app.IdentityRevokeRequest) (install.ChangeSet, error)
	ApplyIdentityRevoke(context.Context, string, app.IdentityRevokeRequest) error
	IdentityList(context.Context) ([]app.IdentityDescriptor, error)
}

type identityOptions struct {
	dryRun, json, yes, stdin bool
	applyPlan                string
	principal, source, kind  string
	slot, newSlot            string
	positionals              []string
}

func runIdentity(ctx context.Context, args []string, deps Dependencies, input *bufio.Reader) int {
	if len(args) == 0 {
		writeLine(deps.Stderr, "invalid mlink identity command")
		return 2
	}
	if args[0] == "list" {
		application, ok := deps.App.(identityApplication)
		if !ok {
			writeLine(deps.Stderr, "mlink identity is unavailable")
			return 1
		}
		return runIdentityList(ctx, args[1:], deps, application)
	}
	if args[0] != "bind" && args[0] != "rebind" && args[0] != "revoke" {
		writeLine(deps.Stderr, "invalid mlink identity command")
		return 2
	}
	options, err := parseIdentityOptions(args[1:])
	if err != nil {
		writeLine(deps.Stderr, "invalid mlink identity arguments")
		return 2
	}
	application, ok := deps.App.(identityApplication)
	if !ok {
		writeLine(deps.Stderr, "mlink identity is unavailable")
		return 1
	}
	switch args[0] {
	case "bind":
		if !options.stdin || options.slot == "" || options.principal == "" || options.source != "feishu" || options.kind == "" || len(options.positionals) != 0 {
			return 2
		}
		value, err := readBoundedLine(input, 16*1024)
		if err != nil || len(value) == 0 {
			wipeBytes(value)
			return 2
		}
		defer wipeBytes(value)
		request := app.IdentityBindRequest{SlotID: options.slot, Source: options.source, Kind: options.kind, PrincipalID: options.principal, Value: value}
		plan, err := application.PlanIdentityBind(ctx, request)
		return finishIdentityMutation(ctx, deps, input, options, plan, err, func(planID string) error { return application.ApplyIdentityBind(ctx, planID, request) })
	case "rebind":
		if !options.stdin || len(options.positionals) != 1 || options.newSlot == "" || options.source != "feishu" || options.kind == "" || options.principal == "" {
			return 2
		}
		value, err := readBoundedLine(input, 16*1024)
		if err != nil || len(value) == 0 {
			wipeBytes(value)
			return 2
		}
		defer wipeBytes(value)
		request := app.IdentityRebindRequest{OldSlotID: options.positionals[0], NewBinding: app.IdentityBindRequest{SlotID: options.newSlot, Source: options.source, Kind: options.kind, PrincipalID: options.principal, Value: value}}
		plan, err := application.PlanIdentityRebind(ctx, request)
		return finishIdentityMutation(ctx, deps, input, options, plan, err, func(planID string) error { return application.ApplyIdentityRebind(ctx, planID, request) })
	case "revoke":
		if len(options.positionals) != 1 || options.stdin || options.slot != "" || options.newSlot != "" {
			return 2
		}
		request := app.IdentityRevokeRequest{SlotID: options.positionals[0]}
		plan, err := application.PlanIdentityRevoke(ctx, request)
		return finishIdentityMutation(ctx, deps, input, options, plan, err, func(planID string) error { return application.ApplyIdentityRevoke(ctx, planID, request) })
	default:
		writeLine(deps.Stderr, "invalid mlink identity command")
		return 2
	}
}

func runIdentityList(ctx context.Context, args []string, deps Dependencies, application identityApplication) int {
	jsonOutput := len(args) == 1 && args[0] == "--json"
	if len(args) != 0 && !jsonOutput {
		return 2
	}
	values, err := application.IdentityList(ctx)
	if err != nil {
		writeLine(deps.Stderr, "mlink identity list failed")
		return 1
	}
	if jsonOutput {
		encoder := json.NewEncoder(deps.Stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(values); err != nil {
			return 1
		}
		return 0
	}
	for _, value := range values {
		_, _ = fmt.Fprintf(deps.Stdout, "%s\t%s\t%s\t%s\t%s\n", value.SlotID, value.Source, value.Kind, value.Status, value.Suffix)
	}
	return 0
}

func finishIdentityMutation(ctx context.Context, deps Dependencies, input *bufio.Reader, options identityOptions, plan install.ChangeSet, planErr error, apply func(string) error) int {
	if planErr != nil {
		writeLine(deps.Stderr, "mlink identity planning failed")
		return exitCodeFor(planErr)
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
	if err := apply(plan.PlanID); err != nil {
		writeLine(deps.Stderr, "mlink identity mutation failed")
		return exitCodeFor(err)
	}
	return 0
}

func parseIdentityOptions(args []string) (identityOptions, error) {
	var options identityOptions
	for index := 0; index < len(args); index++ {
		argument := args[index]
		switch argument {
		case "--dry-run":
			options.dryRun = true
		case "--json":
			options.json = true
		case "--yes":
			options.yes = true
		case "--stdin":
			options.stdin = true
		case "--apply-plan", "--principal", "--source", "--kind", "--slot", "--new-slot":
			index++
			if index >= len(args) || strings.HasPrefix(args[index], "--") {
				return identityOptions{}, fmt.Errorf("%s requires a value", argument)
			}
			switch argument {
			case "--apply-plan":
				options.applyPlan = args[index]
			case "--principal":
				options.principal = args[index]
			case "--source":
				options.source = args[index]
			case "--kind":
				options.kind = args[index]
			case "--slot":
				options.slot = args[index]
			case "--new-slot":
				options.newSlot = args[index]
			}
		default:
			if strings.HasPrefix(argument, "-") {
				return identityOptions{}, fmt.Errorf("unknown option %q", argument)
			}
			options.positionals = append(options.positionals, argument)
		}
	}
	if options.dryRun && options.applyPlan != "" || options.yes && options.applyPlan == "" {
		return identityOptions{}, errors.New("invalid mutation confirmation")
	}
	return options, nil
}
