package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"mlink/internal/install"
	"mlink/internal/journal"
	"mlink/internal/version"
	"mlink/internal/workspacebackup"
)

type WorkspaceRestoreRequest struct {
	BundlePath     string
	Passphrase     []byte
	SelectedAgents []Agent
}

func (request WorkspaceRestoreRequest) String() string {
	return fmt.Sprintf("WorkspaceRestoreRequest{Bundle:%q Passphrase:<redacted> Agents:%v}", filepath.Base(request.BundlePath), request.SelectedAgents)
}

func (request WorkspaceRestoreRequest) GoString() string { return request.String() }

func (request *WorkspaceRestoreRequest) Wipe() {
	if request == nil {
		return
	}
	wipe(request.Passphrase)
	request.Passphrase = nil
}

func (service *Service) PlanWorkspaceRestore(ctx context.Context, request WorkspaceRestoreRequest) (install.ChangeSet, error) {
	if service == nil || service.Target == nil || service.Ledger == nil || service.WorkspacePacker == nil || service.WorkspaceFingerprinter == nil || service.WorkspaceStager == nil || service.SnapshotDriver == nil || service.WorkspaceRestorer == nil {
		return install.ChangeSet{}, errors.New("full workspace restore dependencies are required")
	}
	if !filepath.IsAbs(request.BundlePath) || len(request.Passphrase) < 12 || len(request.Passphrase) > 4096 {
		return install.ChangeSet{}, errors.New("absolute restore bundle and protected passphrase are required")
	}
	manifest, err := service.WorkspacePacker.Open(ctx, request.BundlePath, request.Passphrase, func(workspacebackup.Section, io.Reader) error { return nil })
	if err != nil {
		return install.ChangeSet{}, err
	}
	if err := validateWorkspaceRestoreManifest(manifest); err != nil {
		return install.ChangeSet{}, err
	}
	bundleFingerprint, err := service.WorkspaceFingerprinter.Fingerprint(ctx, request.BundlePath)
	if err != nil {
		return install.ChangeSet{}, err
	}
	if len(request.SelectedAgents) == 0 {
		request.SelectedAgents = make([]Agent, len(manifest.Agents))
		for index, agent := range manifest.Agents {
			request.SelectedAgents[index] = Agent(agent)
		}
	}
	agents, err := normalizeAgents(request.SelectedAgents)
	if err != nil {
		return install.ChangeSet{}, err
	}
	request.SelectedAgents = agents
	providerRequest, err := service.WorkspaceRestorer.ProviderRequest(ctx, manifest)
	if err != nil {
		return install.ChangeSet{}, err
	}
	defer providerRequest.Wipe()
	providerPlan, err := service.SnapshotDriver.PlanRestore(ctx, providerRequest, manifest.Provider)
	if err != nil {
		return install.ChangeSet{}, err
	}
	localPlan, err := service.WorkspaceRestorer.PlanRestore(ctx, request, manifest)
	if err != nil {
		return install.ChangeSet{}, err
	}
	bindingPlan, err := install.BuildChangeSet(service.Target, []install.DesiredResource{{
		OwnerID: "dev.mlink.workspace-restore", Target: "artifact:workspace-restore:" + restorePathFingerprint(request.BundlePath), Action: install.ActionUnchanged,
		Content:      []byte(bundleFingerprint),
		SemanticDiff: []install.SemanticDiff{{Path: "restore.bundle", Before: "encrypted workspace", After: strings.Join(agentStrings(agents), ",")}},
	}})
	if err != nil {
		return install.ChangeSet{}, err
	}
	return install.ComposeChangeSets(bindingPlan, providerPlan, localPlan)
}

func (service *Service) ApplyWorkspaceRestore(ctx context.Context, planID string, request WorkspaceRestoreRequest) error {
	plan, err := service.PlanWorkspaceRestore(ctx, request)
	if err != nil {
		return err
	}
	if plan.PlanID != planID {
		return fmt.Errorf("%w: workspace restore plan changed", install.ErrPlanStale)
	}
	expectedFingerprint, err := restorePlanBundleFingerprint(plan)
	if err != nil {
		return err
	}
	stagedPath, cleanupStage, err := service.WorkspaceStager.Stage(ctx, request.BundlePath, expectedFingerprint)
	if err != nil {
		return err
	}
	defer cleanupStage()
	request.BundlePath = stagedPath
	operation := workspaceRestoreOperation(plan, request.BundlePath)
	if err := service.saveRestoreOperation(ctx, operation, journal.RestorePhasePlanned, ""); err != nil {
		return err
	}
	if err := service.saveRestoreOperation(ctx, operation, journal.RestorePhaseApplying, ""); err != nil {
		return err
	}
	manifest, err := service.WorkspacePacker.Open(ctx, request.BundlePath, request.Passphrase, func(workspacebackup.Section, io.Reader) error { return nil })
	if err != nil {
		return errors.Join(err, service.saveRestoreOperation(context.Background(), operation, journal.RestorePhaseRolledBack, "bundle_open_failed"))
	}
	providerRequest, err := service.WorkspaceRestorer.ProviderRequest(ctx, manifest)
	if err != nil {
		return errors.Join(err, service.saveRestoreOperation(context.Background(), operation, journal.RestorePhaseRolledBack, "provider_request_failed"))
	}
	transaction := install.NewTransaction(service.Target, service.Ledger)
	applied, err := transaction.ApplyDeferredOwnership(ctx, plan)
	if err != nil {
		return errors.Join(err, service.saveRestoreOperation(context.Background(), operation, journal.RestorePhaseRolledBack, "resource_apply_failed"))
	}
	rollback := func(cause error) error {
		localErr := service.WorkspaceRestorer.RollbackRestore(context.Background())
		transactionErr := transaction.Rollback(context.Background(), plan)
		stateErr := service.saveRestoreOperation(context.Background(), operation, journal.RestorePhaseRolledBack, "restore_failed")
		return errors.Join(cause, localErr, transactionErr, stateErr)
	}
	_, err = service.WorkspacePacker.Open(ctx, request.BundlePath, request.Passphrase, func(section workspacebackup.Section, reader io.Reader) error {
		switch section {
		case workspacebackup.SectionCore, workspacebackup.SectionKnowledge:
			return service.SnapshotDriver.ApplySection(ctx, providerRequest, section, reader)
		case workspacebackup.SectionSecrets, workspacebackup.SectionMLink:
			return service.WorkspaceRestorer.StageSection(ctx, section, reader)
		default:
			return nil
		}
	})
	if err != nil {
		return rollback(err)
	}
	providerRequest.Wipe()
	providerRequest, err = service.WorkspaceRestorer.ProviderRequest(ctx, manifest)
	if err != nil {
		return rollback(err)
	}
	defer providerRequest.Wipe()
	if err := service.saveRestoreOperation(ctx, operation, journal.RestorePhaseVerifying, ""); err != nil {
		return rollback(err)
	}
	if err := service.SnapshotDriver.VerifyRestore(ctx, providerRequest, manifest); err != nil {
		return rollback(err)
	}
	if err := service.saveRestoreOperation(ctx, operation, journal.RestorePhaseInstallingAgents, ""); err != nil {
		return rollback(err)
	}
	_, err = service.WorkspacePacker.Open(ctx, request.BundlePath, request.Passphrase, func(section workspacebackup.Section, reader io.Reader) error {
		switch section {
		case workspacebackup.SectionMLink, workspacebackup.SectionIdentity, workspacebackup.SectionSecrets, workspacebackup.SectionAgents:
			return service.WorkspaceRestorer.ApplySection(ctx, section, reader)
		default:
			return nil
		}
	})
	if err != nil {
		return rollback(err)
	}
	if err := service.WorkspaceRestorer.VerifyRestore(ctx, manifest); err != nil {
		return rollback(err)
	}
	if err := transaction.RecordOwnership(ctx, applied); err != nil {
		return rollback(err)
	}
	if err := service.saveRestoreOperation(ctx, operation, journal.RestorePhaseComplete, ""); err != nil {
		return rollback(err)
	}
	return nil
}

func restorePlanBundleFingerprint(plan install.ChangeSet) (string, error) {
	for _, operation := range plan.Operations {
		if operation.OwnerID == "dev.mlink.workspace-restore" && len(operation.Content) == 64 {
			return string(operation.Content), nil
		}
	}
	return "", errors.New("confirmed restore Plan has no bundle fingerprint")
}

func validateWorkspaceRestoreManifest(manifest workspacebackup.Manifest) error {
	if err := workspacebackup.VerifyManifest(manifest); err != nil {
		return err
	}
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		return errors.New("workspace restore is supported only on macOS arm64")
	}
	if manifest.MLink.GOOS != runtime.GOOS || manifest.MLink.GOARCH != runtime.GOARCH {
		return errors.New("workspace backup platform is incompatible with this restore target")
	}
	current := version.Current()
	if manifest.MLink.SchemaMin <= 0 || manifest.MLink.SchemaMax < manifest.MLink.SchemaMin || manifest.MLink.SchemaMin > current.SchemaMax || manifest.MLink.SchemaMax < current.SchemaMin {
		return errors.New("workspace backup schema range is invalid")
	}
	return nil
}

func restorePathFingerprint(path string) string {
	digest := sha256.Sum256([]byte(filepath.Clean(path)))
	return hex.EncodeToString(digest[:])[:16]
}
func agentStrings(values []Agent) []string {
	result := make([]string, len(values))
	for i, v := range values {
		result[i] = string(v)
	}
	sort.Strings(result)
	return result
}

func workspaceRestoreOperation(plan install.ChangeSet, bundlePath string) journal.RestoreOperation {
	owners := make(map[string]bool)
	for _, operation := range plan.Operations {
		if operation.Action != install.ActionUnchanged && strings.HasPrefix(operation.OwnerID, "dev.mlink.") {
			owners[operation.OwnerID] = true
		}
	}
	ownerIDs := make([]string, 0, len(owners))
	bundleFingerprint := restorePathFingerprint(bundlePath)
	for ownerID := range owners {
		ownerIDs = append(ownerIDs, ownerID)
	}
	for _, operation := range plan.Operations {
		if operation.OwnerID == "dev.mlink.workspace-restore" && len(operation.ProposedHash) >= 16 {
			bundleFingerprint = operation.ProposedHash[:16]
			break
		}
	}
	sort.Strings(ownerIDs)
	digest := sha256.Sum256([]byte(plan.PlanID + "\x00" + bundleFingerprint))
	return journal.RestoreOperation{
		OperationID: "restore_" + hex.EncodeToString(digest[:])[:16], BundleFingerprint: bundleFingerprint,
		PlanID: plan.PlanID, CreatedOwnerIDs: ownerIDs,
	}
}

func (service *Service) saveRestoreOperation(ctx context.Context, operation journal.RestoreOperation, phase journal.RestorePhase, errorCode string) error {
	if service.RestoreOperations == nil {
		return nil
	}
	operation.Phase, operation.ErrorCode = phase, errorCode
	return service.RestoreOperations.SaveRestoreOperation(ctx, operation)
}
