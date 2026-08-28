package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"

	"mlink/internal/adapter/codex"
	"mlink/internal/adapter/hermes"
	"mlink/internal/install"
	"mlink/internal/launchagent"
)

var ErrOwnedResourceChanged = errors.New("MLink-owned resource changed after installation")

type backupReader interface {
	ListBackups(context.Context, string) ([]install.Backup, error)
}

func (service *Service) PlanRestore(ctx context.Context, request RestoreRequest) (install.ChangeSet, error) {
	if strings.TrimSpace(request.BackupID) == "" {
		return install.ChangeSet{}, errors.New("backup ID is required")
	}
	reader, ok := service.Ledger.(backupReader)
	if !ok {
		return install.ChangeSet{}, errors.New("installation ledger cannot list backups")
	}
	backups, err := reader.ListBackups(ctx, request.BackupID)
	if err != nil {
		return install.ChangeSet{}, err
	}
	resources := make([]install.DesiredResource, 0, len(backups)+1)
	if containsLaunchAgentBackup(backups) {
		unload, err := launchagent.PlanUnload(service.Paths, service.UID)
		if err != nil {
			return install.ChangeSet{}, err
		}
		resources = append(resources, unload[0])
	}
	for _, backup := range backups {
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

func (service *Service) ApplyRestore(ctx context.Context, planID string, request RestoreRequest) error {
	plan, err := service.PlanRestore(ctx, request)
	if err != nil {
		return err
	}
	if plan.PlanID != planID {
		return fmt.Errorf("%w: restore plan changed", install.ErrPlanStale)
	}
	return install.NewTransaction(service.Target, service.Ledger).Apply(ctx, plan)
}

func (service *Service) restoreResource(ctx context.Context, backup install.Backup) (install.DesiredResource, bool, error) {
	current, mode, err := service.Target.Read(ctx, backup.Target)
	currentExists := err == nil
	if errors.Is(err, fs.ErrNotExist) {
		current = nil
		mode = 0
	} else if err != nil {
		return install.DesiredResource{}, false, err
	}
	ownerID := "dev.mlink.restore"
	if isCodexHooksPath(backup.Target) {
		if !currentExists {
			if backup.Existed {
				return install.DesiredResource{}, false, fmt.Errorf("%w: %s is missing", ErrOwnedResourceChanged, backup.Target)
			}
			return install.DesiredResource{}, false, nil
		}
		content, err := codex.RemoveOwnedHooks(current)
		if err != nil {
			return install.DesiredResource{}, false, err
		}
		return install.DesiredResource{
			OwnerID: ownerID, Target: backup.Target, Content: content, Mode: mode,
			SemanticDiff: []install.SemanticDiff{{Path: "hooks", Before: "MLink Hooks present", After: "MLink Hooks removed; other Hooks preserved"}},
		}, true, nil
	}
	if isHermesConfigPath(backup.Target) {
		if !currentExists {
			return install.DesiredResource{}, false, fmt.Errorf("%w: %s is missing", ErrOwnedResourceChanged, backup.Target)
		}
		_, ownership, err := hermes.MergeConfig(backup.Content)
		if err != nil {
			return install.DesiredResource{}, false, err
		}
		content, err := hermes.RestoreConfig(current, ownership)
		if err != nil {
			return install.DesiredResource{}, false, fmt.Errorf("%w: %s: %v", ErrOwnedResourceChanged, backup.Target, err)
		}
		return install.DesiredResource{
			OwnerID: ownerID, Target: backup.Target, Content: content, Mode: mode,
			SemanticDiff: []install.SemanticDiff{{Path: "memory", Before: "MLink managed values", After: "pre-install values; unrelated changes preserved"}},
		}, true, nil
	}

	if currentExists && contentHash(current) != backup.ProposedHash {
		return install.DesiredResource{}, false, fmt.Errorf("%w: %s", ErrOwnedResourceChanged, backup.Target)
	}
	if !currentExists && backup.Existed {
		return install.DesiredResource{}, false, fmt.Errorf("%w: %s is missing", ErrOwnedResourceChanged, backup.Target)
	}
	if backup.Existed {
		return install.DesiredResource{
			OwnerID: ownerID, Target: backup.Target, Content: append([]byte(nil), backup.Content...), Mode: backup.Mode,
			SemanticDiff: []install.SemanticDiff{{Path: "owned-file", Before: "installed value", After: "pre-install backup"}},
		}, true, nil
	}
	if !currentExists {
		return install.DesiredResource{}, false, nil
	}
	return install.DesiredResource{
		OwnerID: ownerID, Target: backup.Target, Action: install.ActionRemoveOwned,
		SemanticDiff: []install.SemanticDiff{{Path: "owned-file", Before: "installed value", After: "removed"}},
	}, true, nil
}

func containsLaunchAgentBackup(backups []install.Backup) bool {
	for _, backup := range backups {
		if filepath.Base(backup.Target) == "dev.mlink.broker.plist" {
			return true
		}
	}
	return false
}

func isCodexHooksPath(path string) bool {
	return filepath.Base(path) == "hooks.json" && strings.Contains(filepath.Clean(path), string(filepath.Separator)+".codex"+string(filepath.Separator))
}

func isHermesConfigPath(path string) bool {
	return filepath.Base(path) == "config.yaml" && strings.Contains(filepath.Clean(path), string(filepath.Separator)+".hermes"+string(filepath.Separator))
}

func contentHash(content []byte) string {
	digest := sha256.Sum256(content)
	return hex.EncodeToString(digest[:])
}
