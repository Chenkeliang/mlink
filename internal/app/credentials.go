package app

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"sort"
	"strings"

	"mlink/internal/adapter/hermes"
	"mlink/internal/config"
	"mlink/internal/controlplane"
	"mlink/internal/install"
)

const hermesGrantAccount = "adapter/hermes/token"

type CredentialRole string

const (
	CredentialPanelOwner       CredentialRole = "panel-owner"
	CredentialPanelAdmin       CredentialRole = "panel-admin"
	CredentialGateway          CredentialRole = "gateway"
	CredentialMemoryLLM        CredentialRole = "memory-llm"
	CredentialIdentityHMAC     CredentialRole = "identity-hmac"
	CredentialHermesGrant      CredentialRole = "hermes-grant"
	CredentialIdentityBindings CredentialRole = "identity-bindings"
)

type CredentialStatus struct {
	Role        CredentialRole `json:"role"`
	Present     bool           `json:"present"`
	Fingerprint string         `json:"fingerprint,omitempty"`
	CopyAllowed bool           `json:"copy_allowed"`
}

func (service *Service) CredentialStatuses(ctx context.Context) ([]CredentialStatus, error) {
	if service == nil || service.Secrets == nil || service.Target == nil || service.Paths.Config == "" {
		return nil, errors.New("credential inventory dependencies are required")
	}
	content, _, err := service.Target.Read(ctx, service.Paths.Config)
	if err != nil {
		return nil, err
	}
	configuration, err := config.Decode(content)
	if err != nil {
		return nil, err
	}
	connection, exists := configuration.Connections[configuration.ActiveConnectionID]
	if !exists {
		return nil, errors.New("active credential connection is unavailable")
	}
	gatewayAccount, err := credentialAccount(connection.SecretRefs["token"])
	if err != nil {
		return nil, err
	}
	definitions := []struct {
		role        CredentialRole
		account     string
		copyAllowed bool
	}{
		{CredentialPanelOwner, controlplane.OwnerUserKeyAccount, true},
		{CredentialPanelAdmin, controlplane.AdminUserKeyAccount, true},
		{CredentialGateway, gatewayAccount, false},
		{CredentialMemoryLLM, "provider/tencentdb/llm-api-key", false},
		{CredentialIdentityHMAC, "identity/hmac-key", false},
		{CredentialHermesGrant, hermesGrantAccount, false},
	}
	result := make([]CredentialStatus, 0, len(definitions)+1)
	for _, definition := range definitions {
		status, err := service.credentialStatus(ctx, definition.role, definition.account, definition.copyAllowed)
		if err != nil {
			return nil, err
		}
		result = append(result, status)
	}
	bindingStatus, err := service.bindingCredentialStatus(ctx, configuration.Bindings)
	if err != nil {
		return nil, err
	}
	return append(result, bindingStatus), nil
}

func (service *Service) CopyCredential(ctx context.Context, role CredentialRole, destination io.Writer) error {
	if service == nil || service.Secrets == nil || destination == nil {
		return errors.New("credential copy dependencies are required")
	}
	account := ""
	switch role {
	case CredentialPanelOwner:
		account = controlplane.OwnerUserKeyAccount
	case CredentialPanelAdmin:
		account = controlplane.AdminUserKeyAccount
	default:
		return errors.New("credential role is not allowed to copy")
	}
	value, err := service.Secrets.Get(ctx, account)
	if err != nil {
		return errors.New("load Panel credential")
	}
	defer wipe(value)
	_, err = io.Copy(destination, bytes.NewReader(value))
	if err != nil {
		return errors.New("copy Panel credential")
	}
	return nil
}

func (service *Service) credentialStatus(ctx context.Context, role CredentialRole, account string, copyAllowed bool) (CredentialStatus, error) {
	status := CredentialStatus{Role: role, CopyAllowed: copyAllowed}
	value, err := service.Secrets.Get(ctx, account)
	if errors.Is(err, fs.ErrNotExist) {
		return status, nil
	}
	if err != nil {
		return CredentialStatus{}, fmt.Errorf("inspect credential %q", role)
	}
	defer wipe(value)
	status.Present = true
	status.Fingerprint = credentialFingerprint(value)
	return status, nil
}

func (service *Service) bindingCredentialStatus(ctx context.Context, bindings map[string]config.BindingRef) (CredentialStatus, error) {
	status := CredentialStatus{Role: CredentialIdentityBindings}
	ids := make([]string, 0, len(bindings))
	for id := range bindings {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	if len(ids) == 0 {
		return status, nil
	}
	digest := sha256.New()
	for _, id := range ids {
		account, err := credentialAccount(bindings[id].SecretRef)
		if err != nil {
			return CredentialStatus{}, err
		}
		value, err := service.Secrets.Get(ctx, account)
		if errors.Is(err, fs.ErrNotExist) {
			return status, nil
		}
		if err != nil {
			return CredentialStatus{}, errors.New("inspect identity binding credential")
		}
		_, _ = digest.Write([]byte(id))
		_, _ = digest.Write([]byte{0})
		_, _ = digest.Write([]byte(credentialFingerprint(value)))
		wipe(value)
	}
	status.Present = true
	status.Fingerprint = hex.EncodeToString(digest.Sum(nil))[:12]
	return status, nil
}

func credentialAccount(reference string) (string, error) {
	const prefix = "keychain://dev.mlink/"
	if !strings.HasPrefix(reference, prefix) || len(reference) == len(prefix) {
		return "", errors.New("credential secret reference is invalid")
	}
	return strings.TrimPrefix(reference, prefix), nil
}

func (service *Service) PlanHermesGrantRotation(ctx context.Context) (install.ChangeSet, error) {
	current, _, grant, err := service.currentHermesGrant(ctx)
	if err != nil {
		return install.ChangeSet{}, err
	}
	defer wipe(grant)
	var configuration hermes.HTTPGrant
	if err := json.Unmarshal(current, &configuration); err != nil {
		return install.ChangeSet{}, errors.New("decode Hermes grant configuration")
	}
	intent, err := json.Marshal(struct {
		ConfigFingerprint string `json:"config_fingerprint"`
		GrantFingerprint  string `json:"grant_fingerprint"`
		Endpoint          string `json:"endpoint"`
	}{credentialFingerprint(current), credentialFingerprint(grant), configuration.Endpoint})
	if err != nil {
		return install.ChangeSet{}, err
	}
	return install.BuildChangeSet(service.Target, []install.DesiredResource{{
		OwnerID: "dev.mlink.adapter.hermes", Target: "credential:adapter/hermes/token", Action: install.ActionService,
		Command: []string{"mlink-internal", "rotate-hermes-grant"}, CommandInput: intent,
		SemanticDiff: []install.SemanticDiff{
			{Path: "adapter:hermes-grant", Before: "fingerprint:" + credentialFingerprint(grant), After: "new random grant"},
			{Path: "secret.transport", Before: "Keychain and protected Orb file", After: "Keychain and protected Orb file"},
		},
	}})
}

func (service *Service) ApplyHermesGrantRotation(ctx context.Context, planID string) error {
	plan, err := service.PlanHermesGrantRotation(ctx)
	if err != nil {
		return fmt.Errorf("%w: cannot reproduce Hermes grant plan", install.ErrPlanStale)
	}
	if plan.PlanID != planID {
		return fmt.Errorf("%w: Hermes grant changed after preview", install.ErrPlanStale)
	}
	if service.Restarter == nil || service.HermesGrantVerifier == nil {
		return errors.New("Hermes rotation restarter and verifier are required")
	}
	oldConfig, mode, oldGrant, err := service.currentHermesGrant(ctx)
	if err != nil {
		return err
	}
	defer wipe(oldGrant)
	newGrant, err := generateHermesGrant(service.RandomSource)
	if err != nil {
		return err
	}
	defer wipe(newGrant)
	var configuration hermes.HTTPGrant
	if err := json.Unmarshal(oldConfig, &configuration); err != nil {
		return errors.New("decode Hermes grant configuration")
	}
	configuration.Token = string(newGrant)
	newConfig, err := json.Marshal(configuration)
	if err != nil {
		return err
	}
	newConfig = append(newConfig, '\n')
	rollback := func(cause error) error {
		var rollbackErrors []error
		if err := service.Target.WriteAtomic(ctx, service.HermesConfigPath, oldConfig, mode); err != nil {
			rollbackErrors = append(rollbackErrors, fmt.Errorf("restore Hermes config: %w", err))
		}
		if err := service.Secrets.Put(ctx, hermesGrantAccount, oldGrant); err != nil {
			rollbackErrors = append(rollbackErrors, fmt.Errorf("restore Hermes Keychain grant: %w", err))
		}
		if err := service.Restarter.RestartBroker(ctx); err != nil {
			rollbackErrors = append(rollbackErrors, fmt.Errorf("restart restored Broker: %w", err))
		}
		if err := service.Restarter.RestartHermes(ctx); err != nil {
			rollbackErrors = append(rollbackErrors, fmt.Errorf("restart restored Hermes: %w", err))
		}
		return errors.Join(cause, errors.Join(rollbackErrors...))
	}
	if err := service.Secrets.Put(ctx, hermesGrantAccount, newGrant); err != nil {
		return rollback(fmt.Errorf("store new Hermes grant: %w", err))
	}
	if err := service.Target.WriteAtomic(ctx, service.HermesConfigPath, newConfig, mode); err != nil {
		return rollback(fmt.Errorf("write new Hermes grant config: %w", err))
	}
	if err := service.Restarter.RestartBroker(ctx); err != nil {
		return rollback(fmt.Errorf("restart Broker: %w", err))
	}
	if err := service.Restarter.RestartHermes(ctx); err != nil {
		return rollback(fmt.Errorf("restart Hermes: %w", err))
	}
	if err := service.HermesGrantVerifier.VerifyHermesGrant(ctx, configuration.Endpoint, newGrant, oldGrant); err != nil {
		return rollback(fmt.Errorf("verify Hermes grant rotation: %w", err))
	}
	return nil
}

func (service *Service) currentHermesGrant(ctx context.Context) ([]byte, fs.FileMode, []byte, error) {
	if service.Target == nil || service.Secrets == nil || !strings.HasPrefix(service.HermesConfigPath, "/") {
		return nil, 0, nil, errors.New("Hermes rotation target, secrets, and absolute config path are required")
	}
	current, mode, err := service.Target.Read(ctx, service.HermesConfigPath)
	if err != nil {
		return nil, 0, nil, err
	}
	var configuration hermes.HTTPGrant
	if err := json.Unmarshal(current, &configuration); err != nil || strings.TrimSpace(configuration.Endpoint) == "" || strings.TrimSpace(configuration.Token) == "" {
		return nil, 0, nil, errors.New("Hermes grant configuration is invalid")
	}
	grant, err := service.Secrets.Get(ctx, hermesGrantAccount)
	if err != nil {
		return nil, 0, nil, errors.New("load Hermes Keychain grant")
	}
	if configuration.Token != string(grant) {
		wipe(grant)
		return nil, 0, nil, errors.New("Hermes grant fingerprint mismatch")
	}
	return current, mode, grant, nil
}

func generateHermesGrant(source io.Reader) ([]byte, error) {
	if source == nil {
		source = rand.Reader
	}
	raw := make([]byte, 32)
	if _, err := io.ReadFull(source, raw); err != nil {
		wipe(raw)
		return nil, fmt.Errorf("generate Hermes grant: %w", err)
	}
	encoded := make([]byte, base64.RawURLEncoding.EncodedLen(len(raw)))
	base64.RawURLEncoding.Encode(encoded, raw)
	wipe(raw)
	return encoded, nil
}

func credentialFingerprint(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])[:12]
}
