package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"

	"gopkg.in/yaml.v3"

	cursoradapter "mlink/internal/adapter/cursor"
	"mlink/internal/config"
	"mlink/internal/install"
)

func (service *Service) PlanCursorEnable(ctx context.Context) (install.ChangeSet, error) {
	if service == nil || service.Target == nil || service.Ledger == nil {
		return install.ChangeSet{}, errors.New("Cursor lifecycle target and ledger are required")
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
	configuration.Adapters[string(Cursor)] = config.Adapter{ID: string(Cursor), Enabled: true, SpaceID: spaceID}
	proposed, err := yaml.Marshal(configuration)
	if err != nil {
		return install.ChangeSet{}, err
	}
	beforeProtected, err := protectedCursorLifecycleHash(current)
	if err != nil {
		return install.ChangeSet{}, err
	}
	afterProtected, err := protectedCursorLifecycleHash(proposed)
	if err != nil {
		return install.ChangeSet{}, err
	}
	home := filepath.Dir(service.Paths.Home)
	hooksPath := filepath.Join(home, ".cursor", "hooks.json")
	hooks, err := readOptional(ctx, service.Target, hooksPath)
	if err != nil {
		return install.ChangeSet{}, err
	}
	hooksResource, err := cursoradapter.DesiredHooksResource(hooks, hooksPath, service.Paths.Binary)
	if err != nil {
		return install.ChangeSet{}, err
	}
	mcpPath := filepath.Join(home, ".cursor", "mcp.json")
	mcpConfig, err := readOptional(ctx, service.Target, mcpPath)
	if err != nil {
		return install.ChangeSet{}, err
	}
	mcpResource, err := cursoradapter.DesiredMCPResource(mcpConfig, mcpPath, service.Paths.Binary)
	if err != nil {
		return install.ChangeSet{}, err
	}
	return install.BuildChangeSet(service.Target, []install.DesiredResource{
		{
			OwnerID: "dev.mlink.config", Target: service.Paths.Config, Content: proposed, Mode: mode,
			SemanticDiff:        []install.SemanticDiff{{Path: "adapter:cursor.route", Before: "absent or disabled", After: "fixed Owner memory"}},
			ProtectedInvariants: []install.Invariant{{Name: "all_non_cursor_configuration", BeforeHash: beforeProtected, ProposedHash: afterProtected, Preserved: beforeProtected == afterProtected}},
		},
		hooksResource,
		mcpResource,
		{
			OwnerID: "dev.mlink.broker", Target: "service:kickstart:dev.mlink.broker", Action: install.ActionService,
			Command:         []string{"launchctl", "kickstart", "-k", fmt.Sprintf("gui/%d/dev.mlink.broker", service.UID)},
			RollbackCommand: []string{"launchctl", "kickstart", "-k", fmt.Sprintf("gui/%d/dev.mlink.broker", service.UID)},
			SemanticDiff:    []install.SemanticDiff{{Path: "service:broker", Before: "previous adapter registry", After: "Cursor adapter active"}},
		},
	})
}

func (service *Service) ApplyCursorEnable(ctx context.Context, planID string) error {
	plan, err := service.PlanCursorEnable(ctx)
	if err != nil {
		return err
	}
	if plan.PlanID != planID {
		return fmt.Errorf("%w: Cursor lifecycle changed after preview", install.ErrPlanStale)
	}
	return install.NewTransaction(service.Target, service.Ledger).Apply(ctx, plan)
}

func protectedCursorLifecycleHash(content []byte) (string, error) {
	var document map[string]any
	if err := yaml.Unmarshal(content, &document); err != nil {
		return "", err
	}
	if adapters, ok := document["adapters"].(map[string]any); ok {
		delete(adapters, string(Cursor))
	}
	normalized, err := yaml.Marshal(document)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(normalized)
	return hex.EncodeToString(digest[:]), nil
}
