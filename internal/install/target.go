package install

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strings"
	"time"
)

var (
	ErrPlanStale          = errors.New("installation plan is stale")
	ErrBackupCorrupt      = errors.New("installation backup is corrupt")
	ErrInvariantViolation = errors.New("protected configuration invariant was violated")
)

type Target interface {
	Read(context.Context, string) ([]byte, fs.FileMode, error)
	WriteAtomic(context.Context, string, []byte, fs.FileMode) error
	Remove(context.Context, string) error
	Run(context.Context, []string, io.Reader) ([]byte, error)
}

func BuildChangeSet(target Target, desired []DesiredResource) (ChangeSet, error) {
	if target == nil {
		return ChangeSet{}, errors.New("installation target is required")
	}
	changeSet := ChangeSet{GeneratedAt: time.Now().UTC(), MLinkVersion: "dev"}
	operationIDs := make([]string, 0, len(desired))
	targets := make(map[string]struct{}, len(desired))
	for _, resource := range desired {
		if _, exists := targets[resource.Target]; exists {
			return ChangeSet{}, fmt.Errorf("duplicate installation target %q", resource.Target)
		}
		targets[resource.Target] = struct{}{}
		operation, err := buildOperation(context.Background(), target, resource)
		if err != nil {
			return ChangeSet{}, err
		}
		changeSet.Operations = append(changeSet.Operations, operation)
		changeSet.ProtectedInvariants = append(changeSet.ProtectedInvariants, operation.ProtectedInvariants...)
		operationIDs = append(operationIDs, operation.ID)
	}
	changeSet.PlanID = "plan_" + hashParts(operationIDs...)[:26]
	return changeSet, nil
}

func buildOperation(ctx context.Context, target Target, resource DesiredResource) (Operation, error) {
	if strings.TrimSpace(resource.OwnerID) == "" || strings.TrimSpace(resource.Target) == "" {
		return Operation{}, errors.New("resource owner and target are required")
	}
	before, mode, err := target.Read(ctx, resource.Target)
	exists := true
	if errors.Is(err, fs.ErrNotExist) {
		exists = false
		before = nil
		mode = 0
	} else if err != nil {
		return Operation{}, fmt.Errorf("read %q while planning: %w", resource.Target, err)
	}
	action := resource.Action
	if action == "" {
		switch {
		case bytesEqual(before, resource.Content) && exists:
			action = ActionUnchanged
		case exists:
			action = ActionSemanticMerge
		default:
			action = ActionCreate
		}
	}
	if resource.Mode == 0 && action != ActionRemoveOwned && action != ActionService {
		resource.Mode = 0o600
	}
	beforeHash := hashBytes(before)
	proposedHash := hashBytes(resource.Content)
	operation := Operation{
		OwnerID:             resource.OwnerID,
		Target:              resource.Target,
		Action:              action,
		BeforeHash:          beforeHash,
		ProposedHash:        proposedHash,
		SemanticDiff:        append([]SemanticDiff(nil), resource.SemanticDiff...),
		ProtectedInvariants: append([]Invariant(nil), resource.ProtectedInvariants...),
		RollbackAction:      rollbackAction(exists),
		Content:             append([]byte(nil), resource.Content...),
		Mode:                resource.Mode,
		Command:             append([]string(nil), resource.Command...),
		CommandInput:        append([]byte(nil), resource.CommandInput...),
		Verify:              resource.Verify,
		beforeExists:        exists,
		beforeMode:          mode,
	}
	operation.ID = "op_" + hashParts(operation.OwnerID, operation.Target, string(operation.Action), operation.BeforeHash, operation.ProposedHash)[:26]
	return operation, nil
}

func rollbackAction(existed bool) string {
	if existed {
		return "restore_backup"
	}
	return "remove_created"
}

func hashBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func hashParts(parts ...string) string {
	return hashBytes([]byte(strings.Join(parts, "\x00")))
}

func bytesEqual(left, right []byte) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}
