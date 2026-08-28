package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"mlink/internal/adapter/codex"
	"mlink/internal/adapter/hermes"
	"mlink/internal/adapter/pi"
	"mlink/internal/config"
	"mlink/internal/install"
	"mlink/internal/launchagent"
)

func (service *Service) PlanInstall(ctx context.Context, request InstallRequest) (install.ChangeSet, error) {
	if service == nil || service.Target == nil || service.Ledger == nil {
		return install.ChangeSet{}, errors.New("installation service target and ledger are required")
	}
	agents, err := normalizeAgents(request.Agents)
	if err != nil {
		return install.ChangeSet{}, err
	}
	if err := validateConnection(request.Connection); err != nil {
		return install.ChangeSet{}, err
	}
	if len(request.SecretInputs[MemoryCoreTokenSecret]) == 0 {
		return install.ChangeSet{}, errors.New("MemoryCore token input is required")
	}

	binary, _, err := service.Target.Read(ctx, service.Paths.SourceExecutable)
	if err != nil {
		return install.ChangeSet{}, fmt.Errorf("read candidate MLink binary: %w", err)
	}
	resources := []install.DesiredResource{{
		OwnerID: "dev.mlink.binary",
		Target:  service.Paths.Binary,
		Content: binary,
		Mode:    0o700,
		SemanticDiff: []install.SemanticDiff{{
			Path: "binary:mlink", Before: "absent or owned", After: "verified candidate binary",
		}},
	}}

	configuration := desiredConfig(request.Connection, agents)
	configData, err := yaml.Marshal(configuration)
	if err != nil {
		return install.ChangeSet{}, fmt.Errorf("render MLink config: %w", err)
	}
	resources = append(resources, install.DesiredResource{
		OwnerID: "dev.mlink.config", Target: service.Paths.Config, Content: configData, Mode: 0o600,
		SemanticDiff: []install.SemanticDiff{{Path: "connection:" + request.Connection.ID, Before: "absent or owned", After: "TencentDB with Keychain secret reference"}},
		Verify: func(content []byte) error {
			if bytes.Contains(content, request.SecretInputs[MemoryCoreTokenSecret]) {
				return errors.New("MemoryCore token leaked into MLink config")
			}
			return nil
		},
	})

	home := filepath.Dir(service.Paths.Home)
	for _, agent := range agents {
		switch agent {
		case Codex:
			target := filepath.Join(home, ".codex", "hooks.json")
			existing, err := readOptional(ctx, service.Target, target)
			if err != nil {
				return install.ChangeSet{}, err
			}
			resource, err := codex.DesiredHooksResource(existing, target, service.Paths.Binary)
			if err != nil {
				return install.ChangeSet{}, err
			}
			resources = append(resources, resource)
		case Pi:
			resource, err := pi.PlanExtension(service.Paths.Binary, service.Paths.Socket)
			if err != nil {
				return install.ChangeSet{}, err
			}
			resources = append(resources, resource)
		case Hermes:
			hermesResources, err := service.planHermes(ctx, request)
			if err != nil {
				return install.ChangeSet{}, err
			}
			resources = append(resources, hermesResources...)
		}
	}

	launchResources, err := launchagent.Plan(service.Paths, service.UID)
	if err != nil {
		return install.ChangeSet{}, err
	}
	resources = append(resources, launchResources...)
	changeSet, err := install.BuildChangeSet(service.Target, resources)
	if err != nil {
		return install.ChangeSet{}, err
	}
	changeSet.DetectedAgents = make([]string, len(agents))
	for index, agent := range agents {
		changeSet.DetectedAgents[index] = string(agent)
	}
	changeSet.SelectedConnection = request.Connection.ID
	return changeSet, nil
}

func (service *Service) ApplyInstall(ctx context.Context, planID string, request InstallRequest) error {
	plan, err := service.PlanInstall(ctx, request)
	if err != nil {
		return err
	}
	if plan.PlanID != planID {
		return fmt.Errorf("%w: supplied plan %q no longer matches %q", install.ErrPlanStale, planID, plan.PlanID)
	}
	if service.Secrets == nil {
		return errors.New("secret store is required")
	}
	account := "connection/" + request.Connection.ID + "/token"
	previous, getErr := service.Secrets.Get(ctx, account)
	previousExisted := getErr == nil
	if getErr != nil && !errors.Is(getErr, fs.ErrNotExist) {
		return fmt.Errorf("read previous MemoryCore token: %w", getErr)
	}
	secretValue := append([]byte(nil), request.SecretInputs[MemoryCoreTokenSecret]...)
	defer wipe(secretValue)
	defer wipe(previous)
	if err := service.Secrets.Put(ctx, account, secretValue); err != nil {
		return err
	}
	transaction := install.NewTransaction(service.Target, service.Ledger)
	if err := transaction.Apply(ctx, plan); err != nil {
		secretErr := service.restoreSecret(ctx, account, previous, previousExisted)
		if secretErr != nil {
			return errors.Join(err, fmt.Errorf("restore MemoryCore token: %w", secretErr))
		}
		return err
	}
	if recorder, ok := service.Ledger.(InstallPlanRecorder); ok {
		agents := make([]string, len(plan.DetectedAgents))
		copy(agents, plan.DetectedAgents)
		if err := recorder.RecordInstallPlan(ctx, plan.PlanID, agents); err != nil {
			rollbackErr := transaction.Rollback(ctx, plan)
			secretErr := service.restoreSecret(ctx, account, previous, previousExisted)
			return errors.Join(err, rollbackErr, secretErr)
		}
	}
	return nil
}

func (service *Service) planHermes(ctx context.Context, request InstallRequest) ([]install.DesiredResource, error) {
	if strings.TrimSpace(request.HermesMachine) == "" || !filepath.IsAbs(request.HermesHome) {
		return nil, errors.New("detected Hermes machine and home are required")
	}
	if len(service.HermesGrantToken) == 0 {
		return nil, errors.New("Hermes delegated Broker grant is required")
	}
	resources, err := hermes.PlanProvider(request.HermesHome, hermes.HTTPGrant{
		Endpoint: service.HermesEndpoint,
		Token:    string(service.HermesGrantToken),
	})
	if err != nil {
		return nil, err
	}
	configPath := filepath.Join(request.HermesHome, "config.yaml")
	before, _, err := service.Target.Read(ctx, configPath)
	if err != nil {
		return nil, fmt.Errorf("read Hermes config: %w", err)
	}
	after, _, err := hermes.MergeConfig(before)
	if err != nil {
		return nil, err
	}
	beforeProtected, err := protectedHermesHash(before)
	if err != nil {
		return nil, err
	}
	afterProtected, err := protectedHermesHash(after)
	if err != nil {
		return nil, err
	}
	preserved := beforeProtected == afterProtected
	resources = append(resources, install.DesiredResource{
		OwnerID: "dev.mlink.adapter.hermes.config",
		Target:  configPath,
		Content: after,
		Mode:    0o600,
		SemanticDiff: []install.SemanticDiff{
			{Path: "memory.memory_enabled", Before: "true or configured", After: "false"},
			{Path: "memory.user_profile_enabled", Before: "true or configured", After: "false"},
			{Path: "memory.provider", Before: "existing provider", After: "mlink"},
			{Path: "agent.disabled_toolsets[memory]", Before: "absent or preserved", After: "present"},
		},
		ProtectedInvariants: []install.Invariant{{
			Name: "hermes.model_auth_and_unowned_config", BeforeHash: beforeProtected, ProposedHash: afterProtected, Preserved: preserved,
		}},
		Verify: func(content []byte) error {
			actualProtected, err := protectedHermesHash(content)
			if err != nil {
				return err
			}
			if actualProtected != beforeProtected {
				return errors.New("Hermes protected model/auth configuration changed")
			}
			merged, _, err := hermes.MergeConfig(content)
			if err != nil {
				return err
			}
			if !bytes.Equal(merged, content) {
				return errors.New("Hermes managed memory settings are not active")
			}
			return nil
		},
	})
	return resources, nil
}

func desiredConfig(connection config.Connection, agents []Agent) config.Config {
	connection.ProviderConfig = cloneAnyMap(connection.ProviderConfig)
	connection.SecretRefs = map[string]string{
		"token": "keychain://dev.mlink/connection/" + connection.ID + "/token",
	}
	adapters := make(map[string]config.Adapter, len(agents))
	for _, agent := range agents {
		adapters[string(agent)] = config.Adapter{ID: string(agent), Enabled: true, ConnectionID: connection.ID}
	}
	return config.Config{
		SchemaVersion:      1,
		NamespaceID:        connection.TenantID,
		ActiveConnectionID: connection.ID,
		Connections:        map[string]config.Connection{connection.ID: connection},
		Adapters:           adapters,
	}
}

func validateConnection(connection config.Connection) error {
	if connection.ID == "" || connection.ProviderID != "dev.mlink.tencentdb" || connection.ProviderVersion == "" || connection.ConfigRevision == "" ||
		connection.TenantID == "" || connection.AgentID == "" || connection.UserID == "" {
		return errors.New("complete TencentDB connection and identity are required")
	}
	for key := range connection.ProviderConfig {
		normalized := strings.ToLower(strings.ReplaceAll(key, "-", "_"))
		switch normalized {
		case "token", "api_key", "apikey", "secret", "password":
			return fmt.Errorf("Provider secret %q must use SecretInputs", key)
		}
	}
	return nil
}

func normalizeAgents(input []Agent) ([]Agent, error) {
	if len(input) == 0 {
		return nil, errors.New("at least one Agent must be selected")
	}
	seen := make(map[Agent]bool, len(input))
	for _, agent := range input {
		switch agent {
		case Codex, Pi, Hermes:
			seen[agent] = true
		default:
			return nil, fmt.Errorf("unsupported Agent %q", agent)
		}
	}
	result := make([]Agent, 0, len(seen))
	for _, agent := range []Agent{Codex, Pi, Hermes} {
		if seen[agent] {
			result = append(result, agent)
		}
	}
	return result, nil
}

func readOptional(ctx context.Context, target install.Target, path string) ([]byte, error) {
	content, _, err := target.Read(ctx, path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %q: %w", path, err)
	}
	return content, nil
}

func protectedHermesHash(data []byte) (string, error) {
	var document map[string]any
	if err := yaml.Unmarshal(data, &document); err != nil {
		return "", fmt.Errorf("parse protected Hermes config: %w", err)
	}
	memory, _ := document["memory"].(map[string]any)
	if memory != nil {
		delete(memory, "memory_enabled")
		delete(memory, "user_profile_enabled")
		delete(memory, "provider")
		if len(memory) == 0 {
			delete(document, "memory")
		}
	}
	agent, _ := document["agent"].(map[string]any)
	if agent != nil {
		if values, ok := agent["disabled_toolsets"].([]any); ok {
			filtered := values[:0]
			for _, value := range values {
				if value != "memory" {
					filtered = append(filtered, value)
				}
			}
			if len(filtered) == 0 {
				delete(agent, "disabled_toolsets")
			} else {
				agent["disabled_toolsets"] = filtered
			}
		} else if value, exists := agent["disabled_toolsets"]; exists && value == nil {
			delete(agent, "disabled_toolsets")
		}
		if len(agent) == 0 {
			delete(document, "agent")
		}
	}
	encoded, err := yaml.Marshal(document)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func cloneAnyMap(input map[string]any) map[string]any {
	if input == nil {
		return nil
	}
	keys := make([]string, 0, len(input))
	for key := range input {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	output := make(map[string]any, len(input))
	for _, key := range keys {
		output[key] = input[key]
	}
	return output
}

func (service *Service) restoreSecret(ctx context.Context, account string, previous []byte, existed bool) error {
	if existed {
		return service.Secrets.Put(ctx, account, previous)
	}
	return service.Secrets.Delete(ctx, account)
}

func wipe(value []byte) {
	for index := range value {
		value[index] = 0
	}
}
