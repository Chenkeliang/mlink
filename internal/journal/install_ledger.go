package journal

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"mlink/internal/install"
)

var planIDPattern = regexp.MustCompile(`^plan_[A-Za-z0-9_-]+$`)

type InstallationLedger struct {
	store     *Store
	directory string
}

type backupMetadata struct {
	OperationID string `json:"operation_id"`
	Mode        uint32 `json:"mode"`
	Existed     bool   `json:"existed"`
}

type BackupSummary struct {
	BackupID  string    `json:"backup_id"`
	Resources int       `json:"resources"`
	CreatedAt time.Time `json:"created_at"`
}

func NewInstallationLedger(store *Store, directory string) (*InstallationLedger, error) {
	clean := filepath.Clean(directory)
	if store == nil {
		return nil, errors.New("journal store is required")
	}
	if !filepath.IsAbs(clean) || clean == string(filepath.Separator) {
		return nil, errors.New("backup directory must be an absolute non-root path")
	}
	return &InstallationLedger{store: store, directory: clean}, nil
}

func (ledger *InstallationLedger) SaveBackup(ctx context.Context, backup install.Backup) error {
	if !planIDPattern.MatchString(backup.PlanID) || backup.OperationID == "" || backup.Target == "" || backup.BeforeHash == "" || backup.ProposedHash == "" {
		return errors.New("valid backup identity and hashes are required")
	}
	if existing, err := ledger.LoadBackup(ctx, backup.PlanID, backup.Target); err == nil {
		if sameInstallBackup(existing, backup) {
			return nil
		}
		return errors.New("different backup already exists for plan target")
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}

	planDirectory := filepath.Join(ledger.directory, backup.PlanID)
	if err := os.MkdirAll(planDirectory, 0o700); err != nil {
		return fmt.Errorf("create backup directory: %w", err)
	}
	if err := os.Chmod(planDirectory, 0o700); err != nil {
		return fmt.Errorf("protect backup directory: %w", err)
	}
	targetDigest := sha256.Sum256([]byte(backup.Target))
	backupPath := filepath.Join(planDirectory, hex.EncodeToString(targetDigest[:])+".backup")
	if err := writePrivateAtomic(backupPath, backup.Content); err != nil {
		return err
	}
	metadata, err := json.Marshal(backupMetadata{
		OperationID: backup.OperationID,
		Mode:        uint32(backup.Mode.Perm()),
		Existed:     backup.Existed,
	})
	if err != nil {
		_ = os.Remove(backupPath)
		return fmt.Errorf("encode backup metadata: %w", err)
	}
	err = ledger.store.RecordBackup(ctx, Backup{
		BackupID:         backup.PlanID,
		Target:           backup.Target,
		BackupPath:       backupPath,
		BeforeHash:       backup.BeforeHash,
		AfterHash:        backup.ProposedHash,
		SemanticSnapshot: metadata,
	})
	if err != nil {
		_ = os.Remove(backupPath)
		return err
	}
	return nil
}

func (ledger *InstallationLedger) LoadBackup(ctx context.Context, planID, target string) (install.Backup, error) {
	var record Backup
	err := ledger.store.db.QueryRowContext(ctx, `
		SELECT backup_id, target, backup_path, before_hash, after_hash, semantic_snapshot
		FROM backup_artifacts WHERE backup_id = ? AND target = ?`, planID, target).Scan(
		&record.BackupID,
		&record.Target,
		&record.BackupPath,
		&record.BeforeHash,
		&record.AfterHash,
		&record.SemanticSnapshot,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return install.Backup{}, fs.ErrNotExist
	}
	if err != nil {
		return install.Backup{}, fmt.Errorf("load backup index: %w", err)
	}
	return ledger.loadRecord(record)
}

func (ledger *InstallationLedger) ListBackups(ctx context.Context, planID string) ([]install.Backup, error) {
	rows, err := ledger.store.db.QueryContext(ctx, `
		SELECT backup_id, target, backup_path, before_hash, after_hash, semantic_snapshot
		FROM backup_artifacts WHERE backup_id = ? ORDER BY target`, planID)
	if err != nil {
		return nil, fmt.Errorf("list backup indexes: %w", err)
	}
	defer rows.Close()
	var backups []install.Backup
	for rows.Next() {
		var record Backup
		if err := rows.Scan(
			&record.BackupID,
			&record.Target,
			&record.BackupPath,
			&record.BeforeHash,
			&record.AfterHash,
			&record.SemanticSnapshot,
		); err != nil {
			return nil, fmt.Errorf("scan backup index: %w", err)
		}
		backup, err := ledger.loadRecord(record)
		if err != nil {
			return nil, err
		}
		backups = append(backups, backup)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate backup indexes: %w", err)
	}
	return backups, nil
}

func (ledger *InstallationLedger) LatestBackupID(ctx context.Context) (string, error) {
	var backupID string
	err := ledger.store.db.QueryRowContext(ctx, `
		SELECT backup_id FROM backup_artifacts ORDER BY created_at DESC, backup_id DESC LIMIT 1`).Scan(&backupID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fs.ErrNotExist
	}
	if err != nil {
		return "", fmt.Errorf("load latest backup ID: %w", err)
	}
	return backupID, nil
}

func (ledger *InstallationLedger) ListBackupSummaries(ctx context.Context) ([]BackupSummary, error) {
	rows, err := ledger.store.db.QueryContext(ctx, `
		SELECT backup_id, count(*), min(created_at)
		FROM backup_artifacts GROUP BY backup_id ORDER BY min(created_at) DESC, backup_id DESC`)
	if err != nil {
		return nil, fmt.Errorf("list backup summaries: %w", err)
	}
	defer rows.Close()
	var summaries []BackupSummary
	for rows.Next() {
		var summary BackupSummary
		var createdAt string
		if err := rows.Scan(&summary.BackupID, &summary.Resources, &createdAt); err != nil {
			return nil, err
		}
		parsed, err := parseTime(createdAt)
		if err != nil {
			return nil, err
		}
		summary.CreatedAt = parsed
		summaries = append(summaries, summary)
	}
	return summaries, rows.Err()
}

func (ledger *InstallationLedger) RecordInstallPlan(ctx context.Context, planID string, agents []string) error {
	if !planIDPattern.MatchString(planID) || len(agents) == 0 {
		return errors.New("valid install plan and Agents are required")
	}
	manifest, err := json.Marshal(struct {
		PlanID string   `json:"plan_id"`
		Agents []string `json:"agents"`
	}{PlanID: planID, Agents: append([]string(nil), agents...)})
	if err != nil {
		return err
	}
	return ledger.store.RecordInstallation(ctx, Installation{
		AdapterID: "mlink", AgentType: "mlink", Version: "0.1.0", Status: "active", Manifest: manifest,
	})
}

func (ledger *InstallationLedger) ActiveInstallPlan(ctx context.Context) (string, error) {
	var manifest []byte
	err := ledger.store.db.QueryRowContext(ctx, `
		SELECT manifest_json FROM adapter_installations WHERE adapter_id = 'mlink' AND status = 'active'`).Scan(&manifest)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fs.ErrNotExist
	}
	if err != nil {
		return "", fmt.Errorf("load active install plan: %w", err)
	}
	var value struct {
		PlanID string `json:"plan_id"`
	}
	if err := json.Unmarshal(manifest, &value); err != nil {
		return "", fmt.Errorf("decode active install manifest: %w", err)
	}
	if !planIDPattern.MatchString(value.PlanID) {
		return "", errors.New("active install manifest has invalid plan ID")
	}
	return value.PlanID, nil
}

func (ledger *InstallationLedger) MarkInstallRemoved(ctx context.Context) error {
	result, err := ledger.store.db.ExecContext(ctx, `
		UPDATE adapter_installations SET status = 'removed' WHERE adapter_id = 'mlink' AND status = 'active'`)
	if err != nil {
		return fmt.Errorf("mark install removed: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed == 0 {
		return fs.ErrNotExist
	}
	return nil
}

func (ledger *InstallationLedger) RecordOwned(ctx context.Context, resource install.OwnedResource) error {
	return ledger.store.RecordOwnedResource(ctx, OwnedResource{
		OwnerID:             resource.OwnerID,
		Target:              resource.Target,
		SemanticFingerprint: resource.SemanticFingerprint,
		PostApplyHash:       resource.PostApplyHash,
	})
}

func (ledger *InstallationLedger) ListOwned(ctx context.Context) ([]install.OwnedResource, error) {
	rows, err := ledger.store.db.QueryContext(ctx, `
		SELECT owner_id, target, semantic_fingerprint, post_apply_hash
		FROM owned_resources ORDER BY owner_id, target`)
	if err != nil {
		return nil, fmt.Errorf("list owned resources: %w", err)
	}
	defer rows.Close()
	var resources []install.OwnedResource
	for rows.Next() {
		var resource install.OwnedResource
		if err := rows.Scan(&resource.OwnerID, &resource.Target, &resource.SemanticFingerprint, &resource.PostApplyHash); err != nil {
			return nil, fmt.Errorf("scan owned resource: %w", err)
		}
		resources = append(resources, resource)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate owned resources: %w", err)
	}
	return resources, nil
}

func (ledger *InstallationLedger) loadRecord(record Backup) (install.Backup, error) {
	cleanPath := filepath.Clean(record.BackupPath)
	if !stringsHasPathPrefix(cleanPath, ledger.directory) {
		return install.Backup{}, errors.New("backup index points outside backup directory")
	}
	content, err := os.ReadFile(cleanPath)
	if err != nil {
		return install.Backup{}, fmt.Errorf("read backup artifact: %w", err)
	}
	var metadata backupMetadata
	if err := json.Unmarshal(record.SemanticSnapshot, &metadata); err != nil {
		return install.Backup{}, fmt.Errorf("decode backup metadata: %w", err)
	}
	return install.Backup{
		PlanID:       record.BackupID,
		OperationID:  metadata.OperationID,
		Target:       record.Target,
		Content:      content,
		Mode:         fs.FileMode(metadata.Mode),
		Existed:      metadata.Existed,
		BeforeHash:   record.BeforeHash,
		ProposedHash: record.AfterHash,
	}, nil
}

func writePrivateAtomic(path string, content []byte) error {
	temporary, err := os.CreateTemp(filepath.Dir(path), ".mlink-backup-*")
	if err != nil {
		return fmt.Errorf("create backup artifact: %w", err)
	}
	temporaryPath := temporary.Name()
	committed := false
	defer func() {
		_ = temporary.Close()
		if !committed {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return fmt.Errorf("protect backup artifact: %w", err)
	}
	if _, err := temporary.Write(content); err != nil {
		return fmt.Errorf("write backup artifact: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync backup artifact: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close backup artifact: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("commit backup artifact: %w", err)
	}
	committed = true
	return nil
}

func stringsHasPathPrefix(path, root string) bool {
	return path == root || len(path) > len(root) && path[:len(root)] == root && path[len(root)] == filepath.Separator
}

func sameInstallBackup(left, right install.Backup) bool {
	return left.PlanID == right.PlanID && left.OperationID == right.OperationID && left.Target == right.Target &&
		string(left.Content) == string(right.Content) && left.Mode.Perm() == right.Mode.Perm() && left.Existed == right.Existed &&
		left.BeforeHash == right.BeforeHash && left.ProposedHash == right.ProposedHash
}
