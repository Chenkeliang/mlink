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
	"mlink/internal/controlplane"
	"mlink/internal/install"
	"mlink/internal/journal"
	"mlink/internal/launchagent"
	"mlink/internal/panel"
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
	var activeConfiguration config.Config
	if full {
		activeConfiguration, err = service.activeConfiguration(ctx)
		if err != nil {
			return install.ChangeSet{}, err
		}
	}
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
	if full && activeConfiguration.SchemaVersion == 3 {
		resources = append(resources, panelUninstallResources(service.Paths.PanelRegistry)...)
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
		configuration, err := service.activeConfiguration(ctx)
		if err != nil {
			return err
		}
		removedSecrets, err = service.removeInstallSecrets(ctx, configuration)
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
		if service.ControlPlaneStates != nil {
			if err := service.ControlPlaneStates.MarkControlPlaneState(ctx, "inactive"); err != nil {
				return err
			}
		}
		if service.PrincipalAgentStates != nil {
			mappings, err := service.PrincipalAgentStates.ListPrincipalAgents(ctx)
			if err != nil {
				return err
			}
			for _, mapping := range mappings {
				mapping.State = "inactive"
				if err := service.PrincipalAgentStates.PutPrincipalAgent(ctx, mapping); err != nil {
					return err
				}
			}
		}
		if active, ok := service.Ledger.(ActiveInstallStore); ok {
			if err := active.MarkInstallRemoved(ctx); err != nil {
				return err
			}
		}
	}
	return nil
}

func (service *Service) removeInstallSecrets(ctx context.Context, configuration config.Config) ([]managedSecret, error) {
	values := []managedSecret{
		{account: "connection/" + configuration.ActiveConnectionID + "/token"},
		{account: "identity/hmac-key"},
		{account: "adapter/hermes/token"},
	}
	if configuration.SchemaVersion == 3 {
		values = append(values, managedSecret{account: controlplane.AdminUserKeyAccount}, managedSecret{account: controlplane.OwnerUserKeyAccount})
	}
	for _, binding := range configuration.Bindings {
		const prefix = "keychain://dev.mlink/"
		if !strings.HasPrefix(binding.SecretRef, prefix) {
			wipeManagedSecrets(values)
			return nil, errors.New("unsupported MLink Binding secret reference")
		}
		values = append(values, managedSecret{account: strings.TrimPrefix(binding.SecretRef, prefix)})
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

func panelUninstallResources(registryPath string) []install.DesiredResource {
	run := []string{
		"docker", "run", "-d", "--name", panel.ContainerName, "--restart", "unless-stopped",
		"--label", "dev.mlink.component=memory-hub", "--add-host", "host.docker.internal:host-gateway",
		"-p", "127.0.0.1:8125:8125", "-p", "127.0.0.1:8424:8424",
		"-e", "KNOWLEDGE_PUBLIC_BASE_URL=http://host.docker.internal:8424/v3",
		"-e", "KNOWLEDGE_LLM_PROXY_BASE_URL=http://host.docker.internal:8420",
		"-e", "LLM_MODE=proxy", "-e", "KNOWLEDGE_LLM_BINDING_SYNC=true",
		"-e", "LOG_LEVEL=info", "-e", "LOG_FORMAT=json",
		"-v", panel.VolumeName + ":/data/knowledge",
		"-v", registryPath + ":/app/panel/config/metadata-instances.json:ro", panel.ImageReference,
	}
	return []install.DesiredResource{
		{
			OwnerID: "dev.mlink.hub.container", Target: "service:remove:" + panel.ContainerName, Action: install.ActionService,
			Command: []string{"docker", "rm", "-f", panel.ContainerName}, RollbackCommand: run,
			SemanticDiff: []install.SemanticDiff{{Path: "hub:container", Before: "MLink-owned", After: "removed; Knowledge volume and backend metadata retained"}},
		},
		{
			OwnerID: "dev.mlink.panel.registry", Target: registryPath, Action: install.ActionRemoveOwned,
			SemanticDiff: []install.SemanticDiff{{Path: "panel:registry", Before: "protected local credential registry", After: "removed"}},
		},
	}
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
		case Cursor:
			if isCursorHooksPath(clean) {
				return true
			}
		}
	}
	return full
}

func isCursorHooksPath(path string) bool {
	return filepath.Base(path) == "hooks.json" && filepath.Base(filepath.Dir(path)) == ".cursor"
}

func (service *Service) activeConfiguration(ctx context.Context) (config.Config, error) {
	content, _, err := service.Target.Read(ctx, service.Paths.Config)
	if err != nil {
		return config.Config{}, fmt.Errorf("read MLink config before uninstall: %w", err)
	}
	var configuration config.Config
	if err := yaml.Unmarshal(content, &configuration); err != nil {
		return config.Config{}, fmt.Errorf("parse MLink config before uninstall: %w", err)
	}
	if strings.TrimSpace(configuration.ActiveConnectionID) == "" {
		return config.Config{}, errors.New("MLink active connection is missing")
	}
	return configuration, nil
}
