package identity

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"mlink/internal/config"
	"mlink/internal/secret"
)

const bindingSecretPrefix = "keychain://dev.mlink/"

type Repository struct {
	Secrets     secret.Store
	IdentityKey []byte
}

type BindRequest struct {
	SlotID      string
	Source      string
	Kind        string
	PrincipalID string
	Value       []byte
}

type BindPlan struct {
	Account string            `json:"account"`
	Ref     config.BindingRef `json:"binding"`
	Action  string            `json:"action"`
	Value   []byte            `json:"-"`
}

func (plan BindPlan) String() string {
	return fmt.Sprintf("BindPlan{Account:%q Ref:%q Action:%q Value:<redacted>}", plan.Account, plan.Ref.ID, plan.Action)
}

func (plan BindPlan) GoString() string { return plan.String() }

type Change struct {
	Account string
	Value   []byte
	Delete  bool
}

type secretSnapshot struct {
	account string
	value   []byte
	existed bool
}

func (repository Repository) PlanBind(request BindRequest) (BindPlan, error) {
	if len(repository.IdentityKey) != 32 {
		return BindPlan{}, errors.New("32-byte MLink identity key is required")
	}
	if len(request.Value) == 0 || len(request.Value) > 16*1024 || bytes.ContainsAny(request.Value, "\r\n") {
		return BindPlan{}, errors.New("valid single-line external identity is required")
	}
	account, err := BindingAccount(request.SlotID)
	if err != nil {
		return BindPlan{}, err
	}
	ref := config.BindingRef{
		ID: request.SlotID, Source: request.Source, Kind: request.Kind, PrincipalID: request.PrincipalID,
		SecretRef: bindingSecretPrefix + account, Status: config.BindingActive,
	}
	if err := config.ValidateBindingRef(ref); err != nil {
		return BindPlan{}, err
	}
	return BindPlan{Account: account, Ref: ref, Action: "activate", Value: append([]byte(nil), request.Value...)}, nil
}

func BindingAccount(slotID string) (string, error) {
	if slotID == "" || strings.ContainsAny(slotID, "/\\\r\n") {
		return "", errors.New("valid binding slot is required")
	}
	return "identity/binding/" + slotID, nil
}

func (repository Repository) Load(ctx context.Context, refs map[string]config.BindingRef) (BindingSet, error) {
	if repository.Secrets == nil || len(repository.IdentityKey) != 32 {
		return BindingSet{}, errors.New("binding secret store and 32-byte identity key are required")
	}
	ids := make([]string, 0, len(refs))
	for id := range refs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	result := BindingSet{Values: make([]BindingValue, 0, len(ids))}
	for _, id := range ids {
		ref := refs[id]
		if id != ref.ID {
			result.Wipe()
			return BindingSet{}, fmt.Errorf("binding map key %q differs from its ID", id)
		}
		if err := config.ValidateBindingRef(ref); err != nil {
			result.Wipe()
			return BindingSet{}, err
		}
		account := strings.TrimPrefix(ref.SecretRef, bindingSecretPrefix)
		if account == ref.SecretRef {
			result.Wipe()
			return BindingSet{}, errors.New("binding secret reference is unsupported")
		}
		value, err := repository.Secrets.Get(ctx, account)
		if err != nil {
			result.Wipe()
			return BindingSet{}, fmt.Errorf("load binding %q: %w", id, err)
		}
		if len(value) == 0 || len(value) > 16*1024 || bytes.ContainsAny(value, "\r\n") {
			wipeDigest(value)
			result.Wipe()
			return BindingSet{}, fmt.Errorf("binding %q has an invalid value", id)
		}
		result.Values = append(result.Values, BindingValue{
			RefID: id, Source: ref.Source, Kind: ref.Kind, Value: value, PrincipalID: ref.PrincipalID, Revoked: ref.Status == config.BindingRevoked,
		})
	}
	return result, nil
}

func (bindings *BindingSet) Wipe() {
	if bindings == nil {
		return
	}
	for index := range bindings.Values {
		wipeDigest(bindings.Values[index].Value)
		bindings.Values[index].Value = nil
	}
	bindings.Values = nil
}

func (repository Repository) ApplyChanges(ctx context.Context, changes []Change) error {
	if repository.Secrets == nil {
		return errors.New("binding secret store is required")
	}
	snapshots := make([]secretSnapshot, len(changes))
	seen := make(map[string]bool, len(changes))
	for index, change := range changes {
		if change.Account == "" || seen[change.Account] || !strings.HasPrefix(change.Account, "identity/binding/") {
			wipeSnapshots(snapshots)
			return errors.New("changes require unique binding accounts")
		}
		seen[change.Account] = true
		if !change.Delete && (len(change.Value) == 0 || bytes.ContainsAny(change.Value, "\r\n")) {
			wipeSnapshots(snapshots)
			return errors.New("binding change has an invalid value")
		}
		previous, err := repository.Secrets.Get(ctx, change.Account)
		if err == nil {
			snapshots[index] = secretSnapshot{account: change.Account, value: previous, existed: true}
		} else if errors.Is(err, fs.ErrNotExist) {
			snapshots[index] = secretSnapshot{account: change.Account}
		} else {
			wipeSnapshots(snapshots)
			return err
		}
	}
	defer wipeSnapshots(snapshots)
	applied := 0
	for index, change := range changes {
		var err error
		if change.Delete {
			if snapshots[index].existed {
				err = repository.Secrets.Delete(ctx, change.Account)
			}
		} else {
			err = repository.Secrets.Put(ctx, change.Account, change.Value)
		}
		if err != nil {
			rollbackErr := repository.rollbackChanges(ctx, snapshots[:applied])
			return errors.Join(err, rollbackErr)
		}
		applied++
	}
	return nil
}

func (repository Repository) rollbackChanges(ctx context.Context, snapshots []secretSnapshot) error {
	var rollbackErrors []error
	for index := len(snapshots) - 1; index >= 0; index-- {
		snapshot := snapshots[index]
		var err error
		if snapshot.existed {
			err = repository.Secrets.Put(ctx, snapshot.account, snapshot.value)
		} else {
			err = repository.Secrets.Delete(ctx, snapshot.account)
			if errors.Is(err, fs.ErrNotExist) {
				err = nil
			}
		}
		if err != nil {
			rollbackErrors = append(rollbackErrors, fmt.Errorf("restore %q: %w", snapshot.account, err))
		}
	}
	return errors.Join(rollbackErrors...)
}

func wipeSnapshots(snapshots []secretSnapshot) {
	for index := range snapshots {
		wipeDigest(snapshots[index].value)
		snapshots[index].value = nil
	}
}
