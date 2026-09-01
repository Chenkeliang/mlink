package app

import (
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
	"strings"

	"mlink/internal/adapter/hermes"
	"mlink/internal/install"
)

const hermesGrantAccount = "adapter/hermes/token"

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
