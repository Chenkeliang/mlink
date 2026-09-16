package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"

	"gopkg.in/yaml.v3"

	claudeadapter "mlink/internal/adapter/claude"
	"mlink/internal/config"
	"mlink/internal/install"
)

func (service *Service) PlanClaudeEnable(ctx context.Context) (install.ChangeSet, error) {
	if service == nil || service.Target == nil || service.Ledger == nil {
		return install.ChangeSet{}, errors.New("Claude Code lifecycle target and ledger are required")
	}
	current, mode, err := service.Target.Read(ctx, service.Paths.Config)
	if err != nil {
		return install.ChangeSet{}, fmt.Errorf("read active MLink config: %w", err)
	}
	var configuration config.Config
	if err := yaml.Unmarshal(current, &configuration); err != nil || (configuration.SchemaVersion != 2 && configuration.SchemaVersion != 3) || configuration.Adapters == nil {
		return install.ChangeSet{}, errors.New("active MLink config is invalid")
	}
	spaceID := "personal-owner"
	if configuration.SchemaVersion == 3 {
		spaceID = "owner"
	}
	configuration.Adapters[string(Claude)] = config.Adapter{ID: string(Claude), Enabled: true, SpaceID: spaceID}
	proposed, err := yaml.Marshal(configuration)
	if err != nil {
		return install.ChangeSet{}, err
	}
	beforeProtected, err := protectedClaudeLifecycleHash(current)
	if err != nil {
		return install.ChangeSet{}, err
	}
	afterProtected, err := protectedClaudeLifecycleHash(proposed)
	if err != nil {
		return install.ChangeSet{}, err
	}
	settingsPath := filepath.Join(filepath.Dir(service.Paths.Home), ".claude", "settings.json")
	settings, err := readOptional(ctx, service.Target, settingsPath)
	if err != nil {
		return install.ChangeSet{}, err
	}
	hooksResource, err := claudeadapter.DesiredHooksResource(settings, settingsPath, service.Paths.Binary)
	if err != nil {
		return install.ChangeSet{}, err
	}
	return install.BuildChangeSet(service.Target, []install.DesiredResource{
		{
			OwnerID: "dev.mlink.config", Target: service.Paths.Config, Content: proposed, Mode: mode,
			SemanticDiff:        []install.SemanticDiff{{Path: "adapter:claude.route", Before: "absent or disabled", After: "fixed Owner memory"}},
			ProtectedInvariants: []install.Invariant{{Name: "all_non_claude_configuration", BeforeHash: beforeProtected, ProposedHash: afterProtected, Preserved: beforeProtected == afterProtected}},
		},
		hooksResource,
		{
			OwnerID: "dev.mlink.broker", Target: "service:kickstart:dev.mlink.broker", Action: install.ActionService,
			Command:         []string{"launchctl", "kickstart", "-k", fmt.Sprintf("gui/%d/dev.mlink.broker", service.UID)},
			RollbackCommand: []string{"launchctl", "kickstart", "-k", fmt.Sprintf("gui/%d/dev.mlink.broker", service.UID)},
			SemanticDiff:    []install.SemanticDiff{{Path: "service:broker", Before: "previous adapter registry", After: "Claude Code adapter active"}},
		},
	})
}

func (service *Service) ApplyClaudeEnable(ctx context.Context, planID string) error {
	plan, err := service.PlanClaudeEnable(ctx)
	if err != nil {
		return err
	}
	if plan.PlanID != planID {
		return fmt.Errorf("%w: Claude Code lifecycle changed after preview", install.ErrPlanStale)
	}
	return install.NewTransaction(service.Target, service.Ledger).Apply(ctx, plan)
}

func protectedClaudeLifecycleHash(content []byte) (string, error) {
	var document map[string]any
	if err := yaml.Unmarshal(content, &document); err != nil {
		return "", err
	}
	if adapters, ok := document["adapters"].(map[string]any); ok {
		delete(adapters, string(Claude))
	}
	normalized, err := yaml.Marshal(document)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(normalized)
	return hex.EncodeToString(digest[:]), nil
}
