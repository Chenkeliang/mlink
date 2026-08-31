package app

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"sort"
	"time"

	"gopkg.in/yaml.v3"

	"mlink/internal/config"
	"mlink/internal/identity"
	"mlink/internal/install"
)

var ErrIdentityMutationConflict = errors.New("MLink identity mutation conflicts with current state")
var ErrIdentityImportConflict = errors.New("MLink identity import conflicts with current identity")

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

func (service *Service) ExportIdentity(ctx context.Context, passphrase []byte, randomSource io.Reader) ([]byte, error) {
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
	exported := make([]identity.BindingExport, 0, len(bindings.Values))
	for _, binding := range bindings.Values {
		exported = append(exported, identity.BindingExport{Ref: configuration.Bindings[binding.RefID], Value: append([]byte(nil), binding.Value...)})
	}
	bundle := identity.BundleV1{
		SchemaVersion: 1, Principals: clonePrincipals(configuration.Principals), Spaces: cloneSpaces(configuration.Spaces),
		IdentityKey: append([]byte(nil), identityKey...), Bindings: exported, CreatedAt: time.Now().UTC(),
	}
	defer bundle.Wipe()
	if randomSource == nil {
		randomSource = rand.Reader
	}
	return identity.EncryptBundle(bundle, passphrase, randomSource)
}

func (service *Service) PlanIdentityImport(ctx context.Context, bundle identity.BundleV1) (install.ChangeSet, error) {
	if err := identity.ValidateBundle(bundle); err != nil {
		return install.ChangeSet{}, err
	}
	configuration, err := service.loadIdentityConfiguration(ctx)
	if err != nil {
		return install.ChangeSet{}, err
	}
	currentOwner, currentExists := configuration.Principals["owner"]
	incomingOwner, incomingExists := bundle.Principals["owner"]
	if !currentExists || !incomingExists || currentOwner.CanonicalUserID != incomingOwner.CanonicalUserID {
		return install.ChangeSet{}, ErrIdentityImportConflict
	}
	bindings := make(map[string]config.BindingRef, len(bundle.Bindings))
	for _, binding := range bundle.Bindings {
		if _, exists := bindings[binding.Ref.ID]; exists {
			return install.ChangeSet{}, ErrIdentityImportConflict
		}
		bindings[binding.Ref.ID] = binding.Ref
	}
	configuration.Principals = clonePrincipals(bundle.Principals)
	configuration.Spaces = cloneSpaces(bundle.Spaces)
	configuration.Bindings = bindings
	if err := config.Validate(configuration); err != nil {
		return install.ChangeSet{}, ErrIdentityImportConflict
	}
	content, err := yaml.Marshal(configuration)
	if err != nil {
		return install.ChangeSet{}, err
	}
	for _, binding := range bundle.Bindings {
		if bytes.Contains(content, binding.Value) {
			return install.ChangeSet{}, errors.New("identity import leaked a Binding into config")
		}
	}
	return install.BuildChangeSet(service.Target, []install.DesiredResource{{
		OwnerID: "dev.mlink.identity", Target: service.Paths.Config, Content: content, Mode: fs.FileMode(0o600),
		SemanticDiff: []install.SemanticDiff{{Path: "identity-bundle", Before: "current stable identity", After: "verified imported identity"}},
	}})
}

func (service *Service) ApplyIdentityImport(ctx context.Context, planID string, bundle identity.BundleV1) error {
	plan, err := service.PlanIdentityImport(ctx, bundle)
	if err != nil {
		return err
	}
	if plan.PlanID != planID {
		return fmt.Errorf("%w: identity import plan changed", install.ErrPlanStale)
	}
	current, err := service.loadIdentityConfiguration(ctx)
	if err != nil {
		return err
	}
	desired := map[string][]byte{"identity/hmac-key": append([]byte(nil), bundle.IdentityKey...)}
	for _, binding := range bundle.Bindings {
		account, err := identity.BindingAccount(binding.Ref.ID)
		if err != nil {
			return err
		}
		desired[account] = append([]byte(nil), binding.Value...)
	}
	var values []managedSecret
	for account, value := range desired {
		values = append(values, managedSecret{account: account, value: value})
	}
	for _, binding := range current.Bindings {
		account, err := identity.BindingAccount(binding.ID)
		if err != nil {
			wipeManagedSecrets(values)
			return err
		}
		if _, exists := desired[account]; !exists {
			values = append(values, managedSecret{account: account, delete: true})
		}
	}
	sort.Slice(values, func(left, right int) bool { return values[left].account < values[right].account })
	for index := range values {
		previous, err := service.Secrets.Get(ctx, values[index].account)
		if err == nil {
			values[index].previous = previous
			values[index].existed = true
		} else if !errors.Is(err, fs.ErrNotExist) {
			wipeManagedSecrets(values)
			return err
		}
	}
	if err := service.putManagedSecrets(ctx, values); err != nil {
		_ = service.restoreManagedSecrets(ctx, values)
		wipeManagedSecrets(values)
		return err
	}
	defer wipeManagedSecrets(values)
	if err := install.NewTransaction(service.Target, service.Ledger).Apply(ctx, plan); err != nil {
		return errors.Join(err, service.restoreManagedSecrets(ctx, values))
	}
	return nil
}

func clonePrincipals(input map[string]config.Principal) map[string]config.Principal {
	output := make(map[string]config.Principal, len(input))
	for id, principal := range input {
		output[id] = principal
	}
	return output
}

func cloneSpaces(input map[string]config.MemorySpace) map[string]config.MemorySpace {
	output := make(map[string]config.MemorySpace, len(input))
	for id, space := range input {
		output[id] = space
	}
	return output
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
