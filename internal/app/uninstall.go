package app

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"mlink/internal/config"
	"mlink/internal/install"
	"mlink/internal/journal"
	"mlink/internal/launchagent"
)

type latestBackupReader interface {
	LatestBackupID(context.Context) (string, error)
}

func (service *Service) PlanUninstall(ctx context.Context, request UninstallRequest) (install.ChangeSet, error) {
	if request.RemoveState && service.BlockingEvents != nil {
		events, err := service.BlockingEvents.ListStateDeletionBlockers(ctx)
		if err != nil {
			return install.ChangeSet{}, err
		}
		if len(events) != 0 {
			return install.ChangeSet{}, fmt.Errorf("%w: %d queued or unresolved event(s)", journal.ErrBlockingEvents, len(events))
		}
	}
	agents, err := normalizeAgents(request.Agents)
	if err != nil {
		return install.ChangeSet{}, err
	}
	backupID := request.BackupID
	if backupID == "" {
		if active, ok := service.Ledger.(ActiveInstallStore); ok {
			backupID, err = active.ActiveInstallPlan(ctx)
		} else if reader, ok := service.Ledger.(latestBackupReader); ok {
			backupID, err = reader.LatestBackupID(ctx)
		} else {
			err = errors.New("installation ledger cannot locate the active backup")
		}
		if err != nil {
			return install.ChangeSet{}, err
		}
	}
	reader, ok := service.Ledger.(backupReader)
	if !ok {
		return install.ChangeSet{}, errors.New("installation ledger cannot list backups")
	}
	backups, err := reader.ListBackups(ctx, backupID)
	if err != nil {
		return install.ChangeSet{}, err
	}
	full := isFullAgentSet(agents)
	resources := make([]install.DesiredResource, 0, len(backups)+1)
	if full && containsLaunchAgentBackup(backups) {
		unload, err := launchagent.PlanUnload(service.Paths, service.UID)
		if err != nil {
			return install.ChangeSet{}, err
		}
		resources = append(resources, unload[0])
	}
	for _, backup := range backups {
		if !uninstallIncludesTarget(agents, full, backup.Target) {
			continue
		}
		resource, include, err := service.restoreResource(ctx, backup)
		if err != nil {
			return install.ChangeSet{}, err
		}
		if include {
			resources = append(resources, resource)
		}
	}
	return install.BuildChangeSet(service.Target, resources)
}

func (service *Service) ApplyUninstall(ctx context.Context, planID string, request UninstallRequest) error {
	plan, err := service.PlanUninstall(ctx, request)
	if err != nil {
		return err
	}
	if plan.PlanID != planID {
		return fmt.Errorf("%w: uninstall plan changed", install.ErrPlanStale)
	}
	agents, err := normalizeAgents(request.Agents)
	if err != nil {
		return err
	}
	var removedSecrets []managedSecret
	if isFullAgentSet(agents) && service.Secrets != nil {
		connectionID, err := service.activeConnectionID(ctx)
		if err != nil {
			return err
		}
		removedSecrets, err = service.removeInstallSecrets(ctx, connectionID)
		if err != nil {
			return err
		}
	}
	defer wipeManagedSecrets(removedSecrets)
	if err := install.NewTransaction(service.Target, service.Ledger).Apply(ctx, plan); err != nil {
		if restoreErr := service.restoreManagedSecrets(ctx, removedSecrets); restoreErr != nil {
			return errors.Join(err, fmt.Errorf("restore MLink secrets: %w", restoreErr))
		}
		return err
	}
	if isFullAgentSet(agents) {
		if active, ok := service.Ledger.(ActiveInstallStore); ok {
			if err := active.MarkInstallRemoved(ctx); err != nil {
				return err
			}
		}
	}
	return nil
}

func (service *Service) removeInstallSecrets(ctx context.Context, connectionID string) ([]managedSecret, error) {
	values := []managedSecret{
		{account: "connection/" + connectionID + "/token"},
		{account: "identity/hmac-key"},
		{account: "adapter/hermes/token"},
	}
	for index := range values {
		previous, err := service.Secrets.Get(ctx, values[index].account)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			wipeManagedSecrets(values)
			return nil, err
		}
		values[index].previous = previous
		values[index].existed = true
	}
	for index := range values {
		if !values[index].existed {
			continue
		}
		if err := service.Secrets.Delete(ctx, values[index].account); err != nil {
			_ = service.restoreManagedSecrets(ctx, values)
			wipeManagedSecrets(values)
			return nil, err
		}
		values[index].written = true
	}
	return values, nil
}

func isFullAgentSet(agents []Agent) bool {
	return len(agents) == 3 && agents[0] == Codex && agents[1] == Pi && agents[2] == Hermes
}

func uninstallIncludesTarget(agents []Agent, full bool, target string) bool {
	clean := filepath.Clean(target)
	for _, agent := range agents {
		switch agent {
		case Codex:
			if isCodexHooksPath(clean) {
				return true
			}
		case Pi:
			if filepath.Base(clean) == "mlink.ts" && strings.Contains(clean, string(filepath.Separator)+".pi"+string(filepath.Separator)) {
				return true
			}
		case Hermes:
			if isHermesConfigPath(clean) || filepath.Base(clean) == "mlink.json" || strings.Contains(clean, string(filepath.Separator)+"plugins"+string(filepath.Separator)+"mlink"+string(filepath.Separator)) {
				return true
			}
		}
	}
	return full
}

func (service *Service) activeConnectionID(ctx context.Context) (string, error) {
	content, _, err := service.Target.Read(ctx, service.Paths.Config)
	if err != nil {
		return "", fmt.Errorf("read MLink config before uninstall: %w", err)
	}
	var configuration config.Config
	if err := yaml.Unmarshal(content, &configuration); err != nil {
		return "", fmt.Errorf("parse MLink config before uninstall: %w", err)
	}
	if strings.TrimSpace(configuration.ActiveConnectionID) == "" {
		return "", errors.New("MLink active connection is missing")
	}
	return configuration.ActiveConnectionID, nil
}
