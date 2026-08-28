package journal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

type Installation struct {
	AdapterID   string
	AgentType   string
	Version     string
	Status      string
	Manifest    json.RawMessage
	InstalledAt time.Time
}

type OwnedResource struct {
	OwnerID             string
	Target              string
	SemanticFingerprint string
	PostApplyHash       string
}

type Backup struct {
	BackupID         string
	Target           string
	BackupPath       string
	BeforeHash       string
	AfterHash        string
	SemanticSnapshot []byte
	CreatedAt        time.Time
}

func (s *Store) RecordInstallation(ctx context.Context, installation Installation) error {
	if installation.AdapterID == "" || installation.AgentType == "" || installation.Version == "" || installation.Status == "" {
		return errors.New("installation identity and status are required")
	}
	if !json.Valid(installation.Manifest) {
		return errors.New("installation manifest must be valid JSON")
	}
	installedAt := installation.InstalledAt
	if installedAt.IsZero() {
		installedAt = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO adapter_installations(adapter_id, agent_type, version, status, manifest_json, installed_at)
		VALUES(?, ?, ?, ?, ?, ?)
		ON CONFLICT(adapter_id) DO UPDATE SET
			agent_type = excluded.agent_type,
			version = excluded.version,
			status = excluded.status,
			manifest_json = excluded.manifest_json,
			installed_at = excluded.installed_at`,
		installation.AdapterID,
		installation.AgentType,
		installation.Version,
		installation.Status,
		[]byte(installation.Manifest),
		formatTime(installedAt),
	)
	if err != nil {
		return fmt.Errorf("record adapter installation: %w", err)
	}
	return nil
}

func (s *Store) RecordOwnedResource(ctx context.Context, resource OwnedResource) error {
	if resource.OwnerID == "" || resource.Target == "" || resource.SemanticFingerprint == "" || resource.PostApplyHash == "" {
		return errors.New("owned resource fields are required")
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO owned_resources(owner_id, target, semantic_fingerprint, post_apply_hash)
		VALUES(?, ?, ?, ?)
		ON CONFLICT(owner_id, target) DO UPDATE SET
			semantic_fingerprint = excluded.semantic_fingerprint,
			post_apply_hash = excluded.post_apply_hash`,
		resource.OwnerID, resource.Target, resource.SemanticFingerprint, resource.PostApplyHash)
	if err != nil {
		return fmt.Errorf("record owned resource: %w", err)
	}
	return nil
}

func (s *Store) RecordBackup(ctx context.Context, backup Backup) error {
	if backup.BackupID == "" || backup.Target == "" || backup.BackupPath == "" || backup.BeforeHash == "" || backup.AfterHash == "" {
		return errors.New("backup identity and hashes are required")
	}
	createdAt := backup.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO backup_artifacts(
			backup_id, target, backup_path, before_hash, after_hash, semantic_snapshot, created_at
		) VALUES(?, ?, ?, ?, ?, ?, ?)`,
		backup.BackupID,
		backup.Target,
		backup.BackupPath,
		backup.BeforeHash,
		backup.AfterHash,
		backup.SemanticSnapshot,
		formatTime(createdAt),
	)
	if err != nil {
		return fmt.Errorf("record backup artifact: %w", err)
	}
	return nil
}
