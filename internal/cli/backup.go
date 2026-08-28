package cli

import (
	"bufio"
	"context"
	"strings"

	"mlink/internal/app"
)

func runBackup(ctx context.Context, args []string, deps Dependencies, input *bufio.Reader) int {
	if len(args) == 0 || args[0] != "restore" || len(args) < 2 {
		writeLine(deps.Stderr, "invalid mlink backup command")
		return 2
	}
	if deps.App == nil {
		writeLine(deps.Stderr, "mlink backup is unavailable")
		return 1
	}
	backupID := args[1]
	options, err := parseMutationOptions(args[2:], false)
	if err != nil || len(options.positionals) != 0 || strings.TrimSpace(backupID) == "" {
		writeLine(deps.Stderr, "invalid mlink backup restore arguments")
		return 2
	}
	request := app.RestoreRequest{BackupID: backupID}
	plan, err := deps.App.PlanRestore(ctx, request)
	if err != nil {
		writeLine(deps.Stderr, "mlink backup restore planning failed")
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
	if err := deps.App.ApplyRestore(ctx, plan.PlanID, request); err != nil {
		writeLine(deps.Stderr, "mlink backup restore failed")
		return exitCodeFor(err)
	}
	return 0
}
