package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"mlink/internal/install"
)

type UpgradeRequest struct {
	CandidatePath string
}

func (service *Service) PlanUpgrade(ctx context.Context, request UpgradeRequest) (install.ChangeSet, error) {
	if service.Target == nil || service.Ledger == nil || service.UpgradeCandidates == nil || !filepath.IsAbs(request.CandidatePath) {
		return install.ChangeSet{}, errors.New("upgrade target, ledger, and absolute candidate path are required")
	}
	candidate, err := service.UpgradeCandidates.LoadUpgradeCandidate(ctx, request.CandidatePath, service.UID)
	if err != nil {
		return install.ChangeSet{}, err
	}
	if candidate.Path != request.CandidatePath || len(candidate.Content) == 0 || candidate.Mode.Perm()&0o022 != 0 || !safeVersion(candidate.Info.Version) {
		return install.ChangeSet{}, errors.New("upgrade candidate is invalid")
	}
	if service.ActiveSchema < candidate.Info.SchemaMin || service.ActiveSchema > candidate.Info.SchemaMax {
		return install.ChangeSet{}, fmt.Errorf("candidate does not support active config schema %d", service.ActiveSchema)
	}
	plan, err := install.BuildChangeSet(service.Target, []install.DesiredResource{{
		OwnerID: "dev.mlink.binary", Target: service.Paths.Binary, Content: candidate.Content, Mode: 0o700,
		SemanticDiff: []install.SemanticDiff{
			{Path: "binary.version", Before: "installed", After: candidate.Info.Version},
			{Path: "binary.sha256", Before: "current", After: fingerprintBytes(candidate.Content)},
			{Path: "config.schema", Before: fmt.Sprintf("%d", service.ActiveSchema), After: fmt.Sprintf("supported:%d-%d", candidate.Info.SchemaMin, candidate.Info.SchemaMax)},
		},
	}})
	if err != nil {
		return install.ChangeSet{}, err
	}
	plan.MLinkVersion = candidate.Info.Version
	return plan, nil
}

func (service *Service) ApplyUpgrade(ctx context.Context, planID string, request UpgradeRequest) error {
	plan, err := service.PlanUpgrade(ctx, request)
	if err != nil {
		return err
	}
	if plan.PlanID != planID {
		return fmt.Errorf("%w: installed binary or candidate changed after preview", install.ErrPlanStale)
	}
	if service.InstalledVerifier == nil || service.Restarter == nil {
		return errors.New("installed binary verifier and Broker restarter are required")
	}
	transaction := install.NewTransaction(service.Target, service.Ledger)
	applied, err := transaction.ApplyDeferredOwnership(ctx, plan)
	if err != nil {
		return err
	}
	rollback := func(cause error) error {
		rollbackErr := transaction.Rollback(ctx, plan)
		restartErr := service.Restarter.RestartBroker(ctx)
		return errors.Join(cause, rollbackErr, restartErr)
	}
	if err := service.InstalledVerifier.VerifyInstalledBinary(ctx, service.Paths.Binary, plan.Operations[0].ProposedHash); err != nil {
		return rollback(fmt.Errorf("verify installed candidate: %w", err))
	}
	if err := service.Restarter.RestartBroker(ctx); err != nil {
		return rollback(fmt.Errorf("restart Broker with candidate: %w", err))
	}
	if err := transaction.RecordOwnership(ctx, applied); err != nil {
		return rollback(fmt.Errorf("record upgraded binary: %w", err))
	}
	return nil
}

func fingerprintBytes(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

func safeVersion(value string) bool {
	value = strings.TrimSpace(value)
	return value != "" && len(value) <= 128 && !strings.ContainsAny(value, "\r\n\x00")
}
