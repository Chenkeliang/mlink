package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"mlink/internal/app"
	"mlink/internal/journal"
)

type backupListApplication interface {
	ListBackups(context.Context) ([]journal.BackupSummary, error)
}

func runBackup(ctx context.Context, args []string, deps Dependencies, input *bufio.Reader) int {
	if len(args) > 0 && args[0] == "list" {
		if len(args) > 2 || len(args) == 2 && args[1] != "--json" {
			writeLine(deps.Stderr, "invalid mlink backup list arguments")
			return 2
		}
		application, ok := deps.App.(backupListApplication)
		if !ok {
			writeLine(deps.Stderr, "mlink backup list is unavailable")
			return 1
		}
		backups, err := application.ListBackups(ctx)
		if err != nil {
			writeLine(deps.Stderr, "mlink backup list failed")
			return 1
		}
		if len(args) == 2 {
			encoder := json.NewEncoder(deps.Stdout)
			encoder.SetIndent("", "  ")
			if err := encoder.Encode(backups); err != nil {
				return 1
			}
			return 0
		}
		for _, backup := range backups {
			_, _ = fmt.Fprintf(deps.Stdout, "%s\t%d\t%s\n", backup.BackupID, backup.Resources, backup.CreatedAt.Format("2006-01-02 15:04:05Z"))
		}
		return 0
	}
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
