package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"mlink/internal/app"
	"mlink/internal/install"
	"mlink/internal/journal"
	"mlink/internal/workspacebackup"
)

type backupListApplication interface {
	ListBackups(context.Context) ([]journal.BackupSummary, error)
}

type workspaceBackupApplication interface {
	PlanWorkspaceBackup(context.Context, app.WorkspaceBackupRequest) (install.ChangeSet, error)
	ApplyWorkspaceBackup(context.Context, string, app.WorkspaceBackupRequest) error
	InspectWorkspaceBackup(context.Context, string, []byte) (workspacebackup.Manifest, error)
	PlanWorkspaceRestore(context.Context, app.WorkspaceRestoreRequest) (install.ChangeSet, error)
	ApplyWorkspaceRestore(context.Context, string, app.WorkspaceRestoreRequest) error
}

type workspaceManifestSummary struct {
	Format          string    `json:"format"`
	CreatedAt       time.Time `json:"created_at"`
	MLinkVersion    string    `json:"mlink_version"`
	Platform        string    `json:"platform"`
	Provider        string    `json:"provider"`
	Volumes         int       `json:"volumes"`
	Agents          []string  `json:"agents"`
	PrincipalAgents int       `json:"principal_agents"`
}

func runBackup(ctx context.Context, args []string, deps Dependencies, input *bufio.Reader) int {
	if len(args) > 0 && args[0] == "create" {
		return runWorkspaceBackupCreate(ctx, args[1:], deps, input)
	}
	if len(args) > 0 && args[0] == "inspect" {
		return runWorkspaceBackupInspect(ctx, args[1:], deps, input)
	}
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
	if filepath.IsAbs(args[1]) || containsFlag(args[2:], "--passphrase-stdin") {
		return runWorkspaceRestore(ctx, args[1:], deps, input)
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

type workspaceBackupOptions struct {
	path                  string
	passphraseStdin       bool
	approveExternalSource bool
	dryRun                bool
	json                  bool
	yes                   bool
	applyPlan             string
}

func parseWorkspaceBackupOptions(args []string, allowOutput, allowApproval bool) (workspaceBackupOptions, bool) {
	var options workspaceBackupOptions
	for index := 0; index < len(args); index++ {
		switch args[index] {
		case "--output", "--apply-plan":
			flag := args[index]
			index++
			if index >= len(args) || strings.HasPrefix(args[index], "--") {
				return workspaceBackupOptions{}, false
			}
			if flag == "--output" {
				if !allowOutput || options.path != "" {
					return workspaceBackupOptions{}, false
				}
				options.path = args[index]
			} else {
				if options.applyPlan != "" {
					return workspaceBackupOptions{}, false
				}
				options.applyPlan = args[index]
			}
		case "--passphrase-stdin":
			if options.passphraseStdin {
				return workspaceBackupOptions{}, false
			}
			options.passphraseStdin = true
		case "--approve-external-source":
			if !allowApproval || options.approveExternalSource {
				return workspaceBackupOptions{}, false
			}
			options.approveExternalSource = true
		case "--dry-run":
			options.dryRun = true
		case "--json":
			options.json = true
		case "--yes":
			options.yes = true
		default:
			return workspaceBackupOptions{}, false
		}
	}
	if !options.passphraseStdin || options.dryRun && options.applyPlan != "" || options.yes && options.applyPlan == "" {
		return workspaceBackupOptions{}, false
	}
	return options, true
}

func runWorkspaceBackupCreate(ctx context.Context, args []string, deps Dependencies, input *bufio.Reader) int {
	application, ok := deps.App.(workspaceBackupApplication)
	if !ok {
		writeLine(deps.Stderr, "mlink workspace backup is unavailable")
		return 1
	}
	options, valid := parseWorkspaceBackupOptions(args, true, true)
	if !valid || !filepath.IsAbs(options.path) {
		return 2
	}
	passphrase, err := readBoundedLine(input, 4096)
	if err != nil || len(passphrase) < 12 {
		wipeBytes(passphrase)
		return 2
	}
	request := app.WorkspaceBackupRequest{OutputPath: options.path, Passphrase: passphrase, ApproveExternalSource: options.approveExternalSource}
	defer request.Wipe()
	plan, err := application.PlanWorkspaceBackup(ctx, request)
	if err != nil {
		writeLine(deps.Stderr, "mlink workspace backup planning failed")
		return exitCodeFor(err)
	}
	if err := renderPlan(deps.Stdout, plan, options.json); err != nil {
		return 1
	}
	if options.applyPlan == "" {
		return 0
	}
	if !options.yes || options.applyPlan != plan.PlanID {
		return 3
	}
	if err := application.ApplyWorkspaceBackup(ctx, plan.PlanID, request); err != nil {
		writeLine(deps.Stderr, "mlink workspace backup failed")
		return exitCodeFor(err)
	}
	return 0
}

func runWorkspaceBackupInspect(ctx context.Context, args []string, deps Dependencies, input *bufio.Reader) int {
	application, ok := deps.App.(workspaceBackupApplication)
	if !ok || len(args) < 2 || len(args) > 3 || !filepath.IsAbs(args[0]) {
		return 2
	}
	jsonOutput := false
	seenPassphrase := false
	for _, argument := range args[1:] {
		switch argument {
		case "--passphrase-stdin":
			if seenPassphrase {
				return 2
			}
			seenPassphrase = true
		case "--json":
			if jsonOutput {
				return 2
			}
			jsonOutput = true
		default:
			return 2
		}
	}
	if !seenPassphrase {
		return 2
	}
	passphrase, err := readBoundedLine(input, 4096)
	if err != nil || len(passphrase) < 12 {
		wipeBytes(passphrase)
		return 2
	}
	defer wipeBytes(passphrase)
	manifest, err := application.InspectWorkspaceBackup(ctx, args[0], passphrase)
	if err != nil {
		writeLine(deps.Stderr, "mlink workspace backup inspection failed")
		return 1
	}
	summary := safeWorkspaceManifestSummary(manifest)
	if jsonOutput {
		encoder := json.NewEncoder(deps.Stdout)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(summary); err != nil {
			return 1
		}
		return 0
	}
	writeLine(deps.Stdout, fmt.Sprintf("%s · %s · %s · %d volume(s) · %d Agent integration(s)", summary.Format, summary.Provider, summary.Platform, summary.Volumes, len(summary.Agents)))
	return 0
}

func runWorkspaceRestore(ctx context.Context, args []string, deps Dependencies, input *bufio.Reader) int {
	application, ok := deps.App.(workspaceBackupApplication)
	if !ok || len(args) == 0 || !filepath.IsAbs(args[0]) {
		return 2
	}
	options, valid := parseWorkspaceBackupOptions(args[1:], false, false)
	if !valid {
		return 2
	}
	passphrase, err := readBoundedLine(input, 4096)
	if err != nil || len(passphrase) < 12 {
		wipeBytes(passphrase)
		return 2
	}
	request := app.WorkspaceRestoreRequest{BundlePath: args[0], Passphrase: passphrase}
	defer request.Wipe()
	plan, err := application.PlanWorkspaceRestore(ctx, request)
	if err != nil {
		writeLine(deps.Stderr, "mlink workspace restore planning failed")
		return exitCodeFor(err)
	}
	if err := renderPlan(deps.Stdout, plan, options.json); err != nil {
		return 1
	}
	if options.applyPlan == "" {
		return 0
	}
	if !options.yes || options.applyPlan != plan.PlanID {
		return 3
	}
	if err := application.ApplyWorkspaceRestore(ctx, plan.PlanID, request); err != nil {
		writeLine(deps.Stderr, "mlink workspace restore failed")
		return exitCodeFor(err)
	}
	return 0
}

func safeWorkspaceManifestSummary(manifest workspacebackup.Manifest) workspaceManifestSummary {
	return workspaceManifestSummary{
		Format: manifest.Format, CreatedAt: manifest.CreatedAt, MLinkVersion: manifest.MLink.Version,
		Platform: manifest.MLink.GOOS + "/" + manifest.MLink.GOARCH, Provider: manifest.Provider.ProviderID,
		Volumes: len(manifest.Provider.Volumes), Agents: append([]string(nil), manifest.Agents...), PrincipalAgents: len(manifest.PrincipalAgents),
	}
}

func containsFlag(args []string, flag string) bool {
	for _, argument := range args {
		if argument == flag {
			return true
		}
	}
	return false
}
