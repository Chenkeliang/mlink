package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"sort"

	"gopkg.in/yaml.v3"

	"mlink/internal/config"
	"mlink/internal/identity"
	"mlink/internal/install"
)

var ErrIdentityMutationConflict = errors.New("MLink identity mutation conflicts with current state")

type IdentityBindRequest struct {
	SlotID      string
	Source      string
	Kind        string
	PrincipalID string
	Value       []byte
}

func (request IdentityBindRequest) String() string {
	return fmt.Sprintf("IdentityBindRequest{SlotID:%q Source:%q Kind:%q PrincipalID:%q Value:<redacted>}", request.SlotID, request.Source, request.Kind, request.PrincipalID)
}

func (request IdentityBindRequest) GoString() string { return request.String() }

type IdentityRebindRequest struct {
	OldSlotID  string
	NewBinding IdentityBindRequest
}

type IdentityRevokeRequest struct {
	SlotID string
}

type IdentityDescriptor struct {
	SlotID      string `json:"slot_id"`
	Source      string `json:"source"`
	Kind        string `json:"kind"`
	PrincipalID string `json:"principal_id"`
	Status      string `json:"status"`
	Fingerprint string `json:"fingerprint"`
	Suffix      string `json:"suffix"`
}

func (service *Service) PlanIdentityBind(ctx context.Context, request IdentityBindRequest) (install.ChangeSet, error) {
	configuration, err := service.loadIdentityConfiguration(ctx)
	if err != nil {
		return install.ChangeSet{}, err
	}
	if _, exists := configuration.Bindings[request.SlotID]; exists {
		return install.ChangeSet{}, ErrIdentityMutationConflict
	}
	identityKey, err := service.loadIdentityKey(ctx)
	if err != nil {
		return install.ChangeSet{}, err
	}
	defer wipe(identityKey)
	plan, err := (identity.Repository{Secrets: service.Secrets, IdentityKey: identityKey}).PlanBind(identity.BindRequest{
		SlotID: request.SlotID, Source: request.Source, Kind: request.Kind, PrincipalID: request.PrincipalID, Value: request.Value,
	})
	if err != nil {
		return install.ChangeSet{}, err
	}
	configuration.Bindings[request.SlotID] = plan.Ref
	return service.planIdentityConfiguration(configuration, "binding:"+request.SlotID, "absent", "active", request.Value)
}

func (service *Service) ApplyIdentityBind(ctx context.Context, planID string, request IdentityBindRequest) error {
	plan, err := service.PlanIdentityBind(ctx, request)
	if err != nil {
		return err
	}
	if plan.PlanID != planID {
		return fmt.Errorf("%w: identity bind plan changed", install.ErrPlanStale)
	}
	account, err := identity.BindingAccount(request.SlotID)
	if err != nil {
		return err
	}
	return service.applyIdentityPlan(ctx, plan, []managedSecret{{account: account, value: append([]byte(nil), request.Value...)}})
}

func (service *Service) PlanIdentityRebind(ctx context.Context, request IdentityRebindRequest) (install.ChangeSet, error) {
	configuration, err := service.loadIdentityConfiguration(ctx)
	if err != nil {
		return install.ChangeSet{}, err
	}
	old, exists := configuration.Bindings[request.OldSlotID]
	if !exists || old.Status != config.BindingActive {
		return install.ChangeSet{}, ErrIdentityMutationConflict
	}
	if _, exists := configuration.Bindings[request.NewBinding.SlotID]; exists {
		return install.ChangeSet{}, ErrIdentityMutationConflict
	}
	identityKey, err := service.loadIdentityKey(ctx)
	if err != nil {
		return install.ChangeSet{}, err
	}
	defer wipe(identityKey)
	newPlan, err := (identity.Repository{Secrets: service.Secrets, IdentityKey: identityKey}).PlanBind(identity.BindRequest{
		SlotID: request.NewBinding.SlotID, Source: request.NewBinding.Source, Kind: request.NewBinding.Kind,
		PrincipalID: request.NewBinding.PrincipalID, Value: request.NewBinding.Value,
	})
	if err != nil {
		return install.ChangeSet{}, err
	}
	if old.PrincipalID != newPlan.Ref.PrincipalID {
		return install.ChangeSet{}, ErrIdentityMutationConflict
	}
	old.Status = config.BindingRevoked
	configuration.Bindings[request.OldSlotID] = old
	configuration.Bindings[request.NewBinding.SlotID] = newPlan.Ref
	return service.planIdentityConfiguration(configuration, "binding:"+request.NewBinding.SlotID, "absent; old active", "active; old revoked", request.NewBinding.Value)
}

func (service *Service) ApplyIdentityRebind(ctx context.Context, planID string, request IdentityRebindRequest) error {
	plan, err := service.PlanIdentityRebind(ctx, request)
	if err != nil {
		return err
	}
	if plan.PlanID != planID {
		return fmt.Errorf("%w: identity rebind plan changed", install.ErrPlanStale)
	}
	account, err := identity.BindingAccount(request.NewBinding.SlotID)
	if err != nil {
		return err
	}
	return service.applyIdentityPlan(ctx, plan, []managedSecret{{account: account, value: append([]byte(nil), request.NewBinding.Value...)}})
}

func (service *Service) PlanIdentityRevoke(ctx context.Context, request IdentityRevokeRequest) (install.ChangeSet, error) {
	configuration, err := service.loadIdentityConfiguration(ctx)
	if err != nil {
		return install.ChangeSet{}, err
	}
	binding, exists := configuration.Bindings[request.SlotID]
	if !exists || binding.Status != config.BindingActive {
		return install.ChangeSet{}, ErrIdentityMutationConflict
	}
	binding.Status = config.BindingRevoked
	configuration.Bindings[request.SlotID] = binding
	return service.planIdentityConfiguration(configuration, "binding:"+request.SlotID, "active", "revoked", nil)
}

func (service *Service) ApplyIdentityRevoke(ctx context.Context, planID string, request IdentityRevokeRequest) error {
	plan, err := service.PlanIdentityRevoke(ctx, request)
	if err != nil {
		return err
	}
	if plan.PlanID != planID {
		return fmt.Errorf("%w: identity revoke plan changed", install.ErrPlanStale)
	}
	return service.applyIdentityPlan(ctx, plan, nil)
}

func (service *Service) IdentityList(ctx context.Context) ([]IdentityDescriptor, error) {
	configuration, err := service.loadIdentityConfiguration(ctx)
	if err != nil {
		return nil, err
	}
	identityKey, err := service.loadIdentityKey(ctx)
	if err != nil {
		return nil, err
	}
	defer wipe(identityKey)
	bindings, err := (identity.Repository{Secrets: service.Secrets, IdentityKey: identityKey}).Load(ctx, configuration.Bindings)
	if err != nil {
		return nil, err
	}
	defer bindings.Wipe()
	result := make([]IdentityDescriptor, 0, len(bindings.Values))
	for _, binding := range bindings.Values {
		status := string(config.BindingActive)
		if binding.Revoked {
			status = string(config.BindingRevoked)
		}
		result = append(result, IdentityDescriptor{
			SlotID: binding.RefID, Source: binding.Source, Kind: binding.Kind, PrincipalID: binding.PrincipalID,
			Status: status, Fingerprint: identity.BindingFingerprint(identityKey, binding.Source, binding.Value), Suffix: identitySuffix(binding.Value),
		})
	}
	sort.Slice(result, func(left, right int) bool { return result[left].SlotID < result[right].SlotID })
	return result, nil
}

func (service *Service) loadIdentityConfiguration(ctx context.Context) (config.Config, error) {
	if service == nil || service.Target == nil || service.Ledger == nil || service.Secrets == nil {
		return config.Config{}, errors.New("identity service dependencies are required")
	}
	content, _, err := service.Target.Read(ctx, service.Paths.Config)
	if err != nil {
		return config.Config{}, err
	}
	return config.Decode(content)
}

func (service *Service) loadIdentityKey(ctx context.Context) ([]byte, error) {
	key, err := service.Secrets.Get(ctx, "identity/hmac-key")
	if err != nil {
		return nil, err
	}
	if len(key) != 32 {
		wipe(key)
		return nil, errors.New("stored MLink identity key is invalid")
	}
	return key, nil
}

func (service *Service) planIdentityConfiguration(configuration config.Config, path, before, after string, secretValue []byte) (install.ChangeSet, error) {
	if err := config.Validate(configuration); err != nil {
		return install.ChangeSet{}, err
	}
	content, err := yaml.Marshal(configuration)
	if err != nil {
		return install.ChangeSet{}, err
	}
	if len(secretValue) != 0 && bytes.Contains(content, secretValue) {
		return install.ChangeSet{}, errors.New("external identity leaked into MLink config")
	}
	return install.BuildChangeSet(service.Target, []install.DesiredResource{{
		OwnerID: "dev.mlink.identity", Target: service.Paths.Config, Content: content, Mode: fs.FileMode(0o600),
		SemanticDiff: []install.SemanticDiff{{Path: path, Before: before, After: after}},
	}})
}

func (service *Service) applyIdentityPlan(ctx context.Context, plan install.ChangeSet, secrets []managedSecret) error {
	if len(secrets) != 0 {
		for index := range secrets {
			previous, err := service.Secrets.Get(ctx, secrets[index].account)
			if err == nil {
				wipeManagedSecrets(secrets)
				return ErrIdentityMutationConflict
			}
			if !errors.Is(err, fs.ErrNotExist) {
				wipeManagedSecrets(secrets)
				return err
			}
			wipe(previous)
		}
		if err := service.putManagedSecrets(ctx, secrets); err != nil {
			_ = service.restoreManagedSecrets(ctx, secrets)
			wipeManagedSecrets(secrets)
			return err
		}
	}
	defer wipeManagedSecrets(secrets)
	if err := install.NewTransaction(service.Target, service.Ledger).Apply(ctx, plan); err != nil {
		return errors.Join(err, service.restoreManagedSecrets(ctx, secrets))
	}
	return nil
}

func identitySuffix(value []byte) string {
	if len(value) <= 4 {
		return string(value)
	}
	return string(value[len(value)-4:])
}
