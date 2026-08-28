package install

import (
	"context"
	"io/fs"
)

type Backup struct {
	PlanID       string
	OperationID  string
	Target       string
	Content      []byte
	Mode         fs.FileMode
	Existed      bool
	BeforeHash   string
	ProposedHash string
}

func (b Backup) clone() Backup {
	b.Content = append([]byte(nil), b.Content...)
	return b
}

type OwnedResource struct {
	OwnerID             string
	Target              string
	SemanticFingerprint string
	PostApplyHash       string
}

type Ledger interface {
	SaveBackup(context.Context, Backup) error
	LoadBackup(context.Context, string, string) (Backup, error)
	RecordOwned(context.Context, OwnedResource) error
}

func backupKey(planID, target string) string {
	return planID + "\x00" + target
}

func verifyBackup(backup Backup, operation Operation) error {
	if backup.PlanID == "" || backup.OperationID != operation.ID || backup.Target != operation.Target ||
		backup.Existed != operation.beforeExists || backup.BeforeHash != operation.BeforeHash ||
		backup.ProposedHash != operation.ProposedHash || hashBytes(backup.Content) != operation.BeforeHash {
		return ErrBackupCorrupt
	}
	if backup.Existed && backup.Mode.Perm() != operation.beforeMode.Perm() {
		return ErrBackupCorrupt
	}
	return nil
}
