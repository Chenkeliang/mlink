package journal

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

type RestorePhase string

const (
	RestorePhasePlanned          RestorePhase = "planned"
	RestorePhaseApplying         RestorePhase = "applying"
	RestorePhaseVerifying        RestorePhase = "verifying"
	RestorePhaseInstallingAgents RestorePhase = "installing_agents"
	RestorePhaseComplete         RestorePhase = "complete"
	RestorePhaseFailed           RestorePhase = "failed"
	RestorePhaseRolledBack       RestorePhase = "rolled_back"
)

type RestoreOperation struct {
	OperationID       string       `json:"operation_id"`
	BundleFingerprint string       `json:"bundle_fingerprint"`
	PlanID            string       `json:"plan_id"`
	Phase             RestorePhase `json:"phase"`
	CreatedOwnerIDs   []string     `json:"created_owner_ids,omitempty"`
	ErrorCode         string       `json:"error_code,omitempty"`
	StartedAt         time.Time    `json:"started_at"`
	UpdatedAt         time.Time    `json:"updated_at"`
}

type WorkspaceBackupEvidence struct {
	BundleFingerprint string    `json:"bundle_fingerprint"`
	Format            string    `json:"format"`
	VerifiedAt        time.Time `json:"verified_at"`
}

func (operation RestoreOperation) String() string {
	return fmt.Sprintf("RestoreOperation{Operation:%q Bundle:%q Plan:%q Phase:%q CreatedOwners:%d ErrorCode:%q}", operation.OperationID, operation.BundleFingerprint, operation.PlanID, operation.Phase, len(operation.CreatedOwnerIDs), operation.ErrorCode)
}

func (operation RestoreOperation) GoString() string { return operation.String() }

var (
	restoreIDPattern          = regexp.MustCompile(`^restore_[a-f0-9]{12,64}$`)
	restoreFingerprintPattern = regexp.MustCompile(`^[a-f0-9]{16,64}$`)
	restorePlanPattern        = regexp.MustCompile(`^plan_[A-Za-z0-9_-]{12,80}$`)
	restoreOwnerPattern       = regexp.MustCompile(`^dev\.mlink\.[A-Za-z0-9_.-]{1,120}$`)
	restoreErrorPattern       = regexp.MustCompile(`^[a-z0-9_.-]{0,96}$`)
)

func (store *Store) SaveRestoreOperation(ctx context.Context, operation RestoreOperation) error {
	if store == nil || store.db == nil || !validRestoreOperation(operation) {
		return errors.New("valid redacted restore operation is required")
	}
	var previous RestorePhase
	err := store.db.QueryRowContext(ctx, "SELECT phase FROM restore_operations WHERE operation_id = ?", operation.OperationID).Scan(&previous)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("inspect restore operation: %w", err)
	}
	if (previous == RestorePhaseComplete || previous == RestorePhaseRolledBack) && operation.Phase != previous {
		return errors.New("terminal restore operation cannot change phase")
	}
	now := time.Now().UTC()
	if operation.StartedAt.IsZero() {
		operation.StartedAt = now
	}
	operation.UpdatedAt = now
	owners, err := json.Marshal(operation.CreatedOwnerIDs)
	if err != nil {
		return errors.New("encode restore operation owners")
	}
	_, err = store.db.ExecContext(ctx, `
		INSERT INTO restore_operations(operation_id, bundle_fingerprint, plan_id, phase, created_owner_ids_json, error_code, started_at, updated_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(operation_id) DO UPDATE SET
			bundle_fingerprint = excluded.bundle_fingerprint,
			plan_id = excluded.plan_id,
			phase = excluded.phase,
			created_owner_ids_json = excluded.created_owner_ids_json,
			error_code = excluded.error_code,
			updated_at = excluded.updated_at`,
		operation.OperationID, operation.BundleFingerprint, operation.PlanID, operation.Phase, owners, operation.ErrorCode,
		formatTime(operation.StartedAt), formatTime(operation.UpdatedAt),
	)
	if err != nil {
		return fmt.Errorf("save restore operation: %w", err)
	}
	return nil
}

func (store *Store) LatestRestoreOperation(ctx context.Context) (RestoreOperation, error) {
	if store == nil || store.db == nil {
		return RestoreOperation{}, errors.New("restore operation store is unavailable")
	}
	var operation RestoreOperation
	var owners []byte
	var startedAt, updatedAt string
	err := store.db.QueryRowContext(ctx, `
		SELECT operation_id, bundle_fingerprint, plan_id, phase, created_owner_ids_json, error_code, started_at, updated_at
		FROM restore_operations ORDER BY updated_at DESC LIMIT 1`).Scan(
		&operation.OperationID, &operation.BundleFingerprint, &operation.PlanID, &operation.Phase, &owners, &operation.ErrorCode, &startedAt, &updatedAt,
	)
	if err != nil {
		return RestoreOperation{}, err
	}
	if err := json.Unmarshal(owners, &operation.CreatedOwnerIDs); err != nil {
		return RestoreOperation{}, errors.New("decode restore operation owners")
	}
	operation.StartedAt, err = parseTime(startedAt)
	if err != nil {
		return RestoreOperation{}, err
	}
	operation.UpdatedAt, err = parseTime(updatedAt)
	if err != nil {
		return RestoreOperation{}, err
	}
	return operation, nil
}

func validRestoreOperation(operation RestoreOperation) bool {
	if !restoreIDPattern.MatchString(operation.OperationID) || !restoreFingerprintPattern.MatchString(operation.BundleFingerprint) ||
		!restorePlanPattern.MatchString(operation.PlanID) || !validRestorePhase(operation.Phase) || !restoreErrorPattern.MatchString(operation.ErrorCode) {
		return false
	}
	lowerError := strings.ToLower(operation.ErrorCode)
	if strings.Contains(lowerError, "secret") || strings.Contains(lowerError, "token") || strings.Contains(lowerError, "passphrase") || strings.Contains(lowerError, "key=") {
		return false
	}
	seen := make(map[string]bool, len(operation.CreatedOwnerIDs))
	for _, ownerID := range operation.CreatedOwnerIDs {
		if !restoreOwnerPattern.MatchString(ownerID) || seen[ownerID] {
			return false
		}
		seen[ownerID] = true
	}
	return true
}

func ValidateRestoreOperation(operation RestoreOperation) error {
	if !validRestoreOperation(operation) {
		return errors.New("valid redacted restore operation is required")
	}
	return nil
}

func validRestorePhase(phase RestorePhase) bool {
	switch phase {
	case RestorePhasePlanned, RestorePhaseApplying, RestorePhaseVerifying, RestorePhaseInstallingAgents, RestorePhaseComplete, RestorePhaseFailed, RestorePhaseRolledBack:
		return true
	default:
		return false
	}
}

func (store *Store) RecordWorkspaceBackupEvidence(ctx context.Context, evidence WorkspaceBackupEvidence) error {
	if store == nil || store.db == nil || !restoreFingerprintPattern.MatchString(evidence.BundleFingerprint) || evidence.Format != "mlink-full-backup/v1" {
		return errors.New("valid redacted workspace backup evidence is required")
	}
	if evidence.VerifiedAt.IsZero() {
		evidence.VerifiedAt = time.Now().UTC()
	}
	_, err := store.db.ExecContext(ctx, `
		INSERT INTO workspace_backup_evidence(bundle_fingerprint, format, verified_at)
		VALUES(?, ?, ?)
		ON CONFLICT(bundle_fingerprint) DO UPDATE SET format = excluded.format, verified_at = excluded.verified_at`,
		evidence.BundleFingerprint, evidence.Format, formatTime(evidence.VerifiedAt),
	)
	if err != nil {
		return fmt.Errorf("record workspace backup evidence: %w", err)
	}
	return nil
}

func (store *Store) LatestWorkspaceBackupEvidence(ctx context.Context) (WorkspaceBackupEvidence, error) {
	if store == nil || store.db == nil {
		return WorkspaceBackupEvidence{}, errors.New("workspace backup evidence store is unavailable")
	}
	var evidence WorkspaceBackupEvidence
	var verifiedAt string
	err := store.db.QueryRowContext(ctx, `
		SELECT bundle_fingerprint, format, verified_at
		FROM workspace_backup_evidence ORDER BY verified_at DESC LIMIT 1`).Scan(&evidence.BundleFingerprint, &evidence.Format, &verifiedAt)
	if err != nil {
		return WorkspaceBackupEvidence{}, err
	}
	evidence.VerifiedAt, err = parseTime(verifiedAt)
	if err != nil {
		return WorkspaceBackupEvidence{}, err
	}
	return evidence, nil
}
