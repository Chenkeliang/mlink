package install

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
)

type Transaction struct {
	target Target
	ledger Ledger
}

func NewTransaction(target Target, ledger Ledger) Transaction {
	return Transaction{target: target, ledger: ledger}
}

func (t Transaction) Apply(ctx context.Context, changeSet ChangeSet) error {
	if t.target == nil || t.ledger == nil {
		return errors.New("transaction target and ledger are required")
	}
	if err := validateInvariants(changeSet); err != nil {
		return err
	}
	if err := t.ensureFresh(ctx, changeSet); err != nil {
		return err
	}
	if err := t.backupAll(ctx, changeSet); err != nil {
		return err
	}
	var applied []Operation
	for _, operation := range changeSet.Operations {
		if operation.Action == ActionUnchanged {
			continue
		}
		if err := t.applyOperation(ctx, operation); err != nil {
			rollbackErr := t.rollbackOperations(ctx, changeSet.PlanID, applied)
			if rollbackErr != nil {
				return errors.Join(fmt.Errorf("apply %q: %w", operation.Target, err), fmt.Errorf("rollback: %w", rollbackErr))
			}
			return fmt.Errorf("apply %q: %w", operation.Target, err)
		}
		applied = append(applied, operation)
	}
	for _, operation := range applied {
		resource := OwnedResource{
			OwnerID:             operation.OwnerID,
			Target:              operation.Target,
			SemanticFingerprint: hashSemanticDiff(operation.SemanticDiff),
			PostApplyHash:       operation.ProposedHash,
		}
		if err := t.ledger.RecordOwned(ctx, resource); err != nil {
			rollbackErr := t.rollbackOperations(ctx, changeSet.PlanID, applied)
			if rollbackErr != nil {
				return errors.Join(fmt.Errorf("record ownership: %w", err), fmt.Errorf("rollback: %w", rollbackErr))
			}
			return fmt.Errorf("record ownership: %w", err)
		}
	}
	return nil
}

func (t Transaction) Rollback(ctx context.Context, changeSet ChangeSet) error {
	operations := make([]Operation, 0, len(changeSet.Operations))
	for _, operation := range changeSet.Operations {
		if operation.Action != ActionUnchanged {
			operations = append(operations, operation)
		}
	}
	return t.rollbackOperations(ctx, changeSet.PlanID, operations)
}

func (t Transaction) ensureFresh(ctx context.Context, changeSet ChangeSet) error {
	for _, operation := range changeSet.Operations {
		if operation.Action == ActionService {
			continue
		}
		current, _, err := t.target.Read(ctx, operation.Target)
		if errors.Is(err, fs.ErrNotExist) {
			if operation.beforeExists {
				return fmt.Errorf("%w: %s no longer exists", ErrPlanStale, operation.Target)
			}
			current = nil
		} else if err != nil {
			return fmt.Errorf("check %q: %w", operation.Target, err)
		} else if !operation.beforeExists {
			return fmt.Errorf("%w: %s was created after preview", ErrPlanStale, operation.Target)
		}
		if hashBytes(current) != operation.BeforeHash {
			return fmt.Errorf("%w: %s changed after preview", ErrPlanStale, operation.Target)
		}
	}
	return nil
}

func (t Transaction) backupAll(ctx context.Context, changeSet ChangeSet) error {
	for _, operation := range changeSet.Operations {
		if operation.Action == ActionUnchanged || operation.Action == ActionService {
			continue
		}
		content, mode, err := t.target.Read(ctx, operation.Target)
		if errors.Is(err, fs.ErrNotExist) {
			content = nil
			mode = 0
		} else if err != nil {
			return fmt.Errorf("read %q for backup: %w", operation.Target, err)
		}
		backup := Backup{
			PlanID:       changeSet.PlanID,
			OperationID:  operation.ID,
			Target:       operation.Target,
			Content:      append([]byte(nil), content...),
			Mode:         mode,
			Existed:      operation.beforeExists,
			BeforeHash:   operation.BeforeHash,
			ProposedHash: operation.ProposedHash,
		}
		if err := t.ledger.SaveBackup(ctx, backup); err != nil {
			return fmt.Errorf("save backup for %q: %w", operation.Target, err)
		}
		stored, err := t.ledger.LoadBackup(ctx, changeSet.PlanID, operation.Target)
		if err != nil {
			return fmt.Errorf("reload backup for %q: %w", operation.Target, err)
		}
		if err := verifyBackup(stored, operation); err != nil {
			return fmt.Errorf("%w: %s", err, operation.Target)
		}
	}
	return nil
}

func (t Transaction) applyOperation(ctx context.Context, operation Operation) error {
	switch operation.Action {
	case ActionCreate, ActionSemanticMerge:
		if err := t.target.WriteAtomic(ctx, operation.Target, operation.Content, operation.Mode); err != nil {
			return err
		}
		content, _, err := t.target.Read(ctx, operation.Target)
		if err != nil {
			return fmt.Errorf("verify written target: %w", err)
		}
		if hashBytes(content) != operation.ProposedHash {
			return errors.New("written target hash differs from preview")
		}
		if operation.Verify != nil {
			if err := operation.Verify(content); err != nil {
				return fmt.Errorf("semantic verification: %w", err)
			}
		}
		return nil
	case ActionRemoveOwned:
		if err := t.target.Remove(ctx, operation.Target); err != nil {
			return err
		}
		if _, _, err := t.target.Read(ctx, operation.Target); !errors.Is(err, fs.ErrNotExist) {
			if err == nil {
				return errors.New("removed target still exists")
			}
			return err
		}
		return nil
	case ActionService:
		_, err := t.target.Run(ctx, operation.Command, bytes.NewReader(operation.CommandInput))
		return err
	default:
		return fmt.Errorf("unsupported installation action %q", operation.Action)
	}
}

func (t Transaction) rollbackOperations(ctx context.Context, planID string, operations []Operation) error {
	var rollbackErrors []error
	for i := len(operations) - 1; i >= 0; i-- {
		operation := operations[i]
		if operation.Action == ActionService {
			if len(operation.RollbackCommand) != 0 {
				if _, err := t.target.Run(ctx, operation.RollbackCommand, bytes.NewReader(operation.RollbackCommandInput)); err != nil {
					rollbackErrors = append(rollbackErrors, fmt.Errorf("compensate %q: %w", operation.Target, err))
				}
			}
			continue
		}
		backup, err := t.ledger.LoadBackup(ctx, planID, operation.Target)
		if err != nil {
			rollbackErrors = append(rollbackErrors, fmt.Errorf("load %q: %w", operation.Target, err))
			continue
		}
		if err := verifyBackup(backup, operation); err != nil {
			rollbackErrors = append(rollbackErrors, fmt.Errorf("verify %q: %w", operation.Target, err))
			continue
		}
		if backup.Existed {
			err = t.target.WriteAtomic(ctx, operation.Target, backup.Content, backup.Mode)
		} else {
			err = t.target.Remove(ctx, operation.Target)
		}
		if err != nil {
			rollbackErrors = append(rollbackErrors, fmt.Errorf("restore %q: %w", operation.Target, err))
		}
	}
	return errors.Join(rollbackErrors...)
}

func validateInvariants(changeSet ChangeSet) error {
	for _, operation := range changeSet.Operations {
		for _, invariant := range operation.ProtectedInvariants {
			if !invariant.Preserved || (invariant.BeforeHash != "" && invariant.ProposedHash != "" && invariant.BeforeHash != invariant.ProposedHash) {
				return fmt.Errorf("%w: %s", ErrInvariantViolation, invariant.Name)
			}
		}
	}
	return nil
}

func hashSemanticDiff(diff []SemanticDiff) string {
	parts := make([]string, 0, len(diff)*3)
	for _, item := range diff {
		parts = append(parts, item.Path, item.Before, item.After)
	}
	return hashParts(parts...)
}
