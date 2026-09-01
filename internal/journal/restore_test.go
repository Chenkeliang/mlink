package journal

import (
	"context"
	"strings"
	"testing"
)

func TestRestoreOperationLifecycleStoresOnlyRedactedEvidence(t *testing.T) {
	store := openTestStore(t)
	operation := RestoreOperation{
		OperationID: "restore_0123456789ab", BundleFingerprint: "0123456789abcdef", PlanID: "plan_0123456789abcdef",
		Phase: RestorePhasePlanned, CreatedOwnerIDs: []string{"dev.mlink.memorycore.volume"},
	}
	if err := store.SaveRestoreOperation(context.Background(), operation); err != nil {
		t.Fatal(err)
	}
	operation.Phase, operation.ErrorCode = RestorePhaseVerifying, ""
	if err := store.SaveRestoreOperation(context.Background(), operation); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.LatestRestoreOperation(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if loaded.OperationID != operation.OperationID || loaded.Phase != RestorePhaseVerifying || strings.Contains(loaded.String(), "/Users/") {
		t.Fatalf("loaded operation = %#v", loaded)
	}
}

func TestRestoreOperationRejectsPathsSecretsRawIDsAndInvalidTransitions(t *testing.T) {
	store := openTestStore(t)
	base := RestoreOperation{OperationID: "restore_0123456789ab", BundleFingerprint: "0123456789abcdef", PlanID: "plan_0123456789abcdef", Phase: RestorePhasePlanned}
	for name, mutate := range map[string]func(*RestoreOperation){
		"path":   func(value *RestoreOperation) { value.ErrorCode = "/Users/private/bundle" },
		"secret": func(value *RestoreOperation) { value.ErrorCode = "gateway-token-secret" },
		"raw id": func(value *RestoreOperation) { value.CreatedOwnerIDs = []string{"ou_actual_feishu_id"} },
	} {
		t.Run(name, func(t *testing.T) {
			value := base
			mutate(&value)
			if err := store.SaveRestoreOperation(context.Background(), value); err == nil {
				t.Fatal("unsafe restore operation was accepted")
			}
		})
	}
	if err := store.SaveRestoreOperation(context.Background(), base); err != nil {
		t.Fatal(err)
	}
	base.Phase = RestorePhaseComplete
	if err := store.SaveRestoreOperation(context.Background(), base); err != nil {
		t.Fatal(err)
	}
	base.Phase = RestorePhaseApplying
	if err := store.SaveRestoreOperation(context.Background(), base); err == nil {
		t.Fatal("completed restore operation regressed to applying")
	}
}

func TestWorkspaceBackupEvidenceStoresFingerprintWithoutPath(t *testing.T) {
	store := openTestStore(t)
	value := WorkspaceBackupEvidence{BundleFingerprint: "0123456789abcdef", Format: "mlink-full-backup/v1"}
	if err := store.RecordWorkspaceBackupEvidence(context.Background(), value); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.LatestWorkspaceBackupEvidence(context.Background())
	if err != nil || loaded.BundleFingerprint != value.BundleFingerprint || loaded.Format != value.Format {
		t.Fatalf("evidence = %#v/%v", loaded, err)
	}
	value.BundleFingerprint = "/Users/private/full.mlink-backup"
	if err := store.RecordWorkspaceBackupEvidence(context.Background(), value); err == nil {
		t.Fatal("backup path was accepted as evidence")
	}
}
