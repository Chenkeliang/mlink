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
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"mlink/internal/adapter/codex"
	cursoradapter "mlink/internal/adapter/cursor"
	"mlink/internal/adapter/hermes"
	"mlink/internal/adapter/pi"
	"mlink/internal/config"
	"mlink/internal/identity"
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
	if len(service.IdentityKey) != 32 {
		return install.ChangeSet{}, errors.New("32-byte MLink identity key is required")
	}
	namespaceID, _, err := normalizeOwner(request)
	if err != nil {
		return install.ChangeSet{}, err
	}
	if service.ControlPlaneStates == nil {
		return install.ChangeSet{}, errors.New("fresh install requires provisioned Core identity")
	}
	controlState, err := service.ControlPlaneStates.LoadControlPlane(ctx)
	if err != nil {
		return install.ChangeSet{}, fmt.Errorf("load provisioned Core identity: %w", err)
	}
	if (controlState.State != "provisioned" && controlState.State != "active") || controlState.InstallationID != namespaceID {
		return install.ChangeSet{}, errors.New("matching Core identity and explicit dynamic Agent limit are required")
	}
	if controlState.DynamicAgentLimit <= 0 || controlState.DynamicAgentLimit > 10_000 || request.DynamicAgentLimit != controlState.DynamicAgentLimit {
		return install.ChangeSet{}, errors.New("installation capacity must match the provisioned Core identity")
	}
	if agentSelected(agents, Hermes) {
		if err := validateOwnerBinding(request); err != nil {
			return install.ChangeSet{}, err
		}
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

	configuration, err := service.desiredConfig(request, agents)
	if err != nil {
		return install.ChangeSet{}, err
	}
	configuration, err = buildCutoverConfig(configuration, controlState, request.DynamicAgentLimit)
	if err != nil {
		return install.ChangeSet{}, err
	}
	configData, err := yaml.Marshal(configuration)
	if err != nil {
		return install.ChangeSet{}, fmt.Errorf("render MLink config: %w", err)
	}
	configDiff := []install.SemanticDiff{
		{Path: "schema_version", Before: "absent or owned", After: "3"},
		{Path: "connection:" + request.Connection.ID, Before: "absent or owned", After: "TencentDB with Keychain secret reference"},
		{Path: "principal:owner", Before: "absent or owned", After: "Core-generated Owner"},
		{Path: "routing:owner", Before: "absent", After: "Core Owner L1/L2/L3"},
		{Path: "routing:hermes-private", Before: "absent", After: "dynamic Agent per principal; L1"},
		{Path: "routing:hermes-groups", Before: "absent", After: "dynamic Agent per group; topic session; L1"},
	}
	if agentSelected(agents, Hermes) {
		configDiff = append(configDiff, install.SemanticDiff{
			Path: "binding:" + request.OwnerBindingSlot.ID, Before: "absent or owned", After: "redacted " + request.OwnerBindingSlot.Kind + " alias -> owner",
		})
	}
	for _, agent := range agents {
		after := "fixed Core Owner"
		if agent == Hermes {
			after = "dynamic owner/private/group"
		}
		configDiff = append(configDiff, install.SemanticDiff{
			Path: "adapter:" + string(agent) + ".route", Before: "absent or owned", After: after,
		})
	}
	resources = append(resources, install.DesiredResource{
		OwnerID: "dev.mlink.config", Target: service.Paths.Config, Content: configData, Mode: 0o600,
		SemanticDiff: configDiff,
		Verify: func(content []byte) error {
			if bytes.Contains(content, request.SecretInputs[MemoryCoreTokenSecret]) {
				return errors.New("MemoryCore token leaked into MLink config")
			}
			active, err := config.Decode(content)
			if err != nil {
				return err
			}
			return verifyGeneratedControlPlane(active, controlState)
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
		case Cursor:
			hooksTarget := filepath.Join(home, ".cursor", "hooks.json")
			existing, err := readOptional(ctx, service.Target, hooksTarget)
			if err != nil {
				return install.ChangeSet{}, err
			}
			resource, err := cursoradapter.DesiredHooksResource(existing, hooksTarget, service.Paths.Binary)
			if err != nil {
				return install.ChangeSet{}, err
			}
			resources = append(resources, resource)
			mcpTarget := filepath.Join(home, ".cursor", "mcp.json")
			existing, err = readOptional(ctx, service.Target, mcpTarget)
			if err != nil {
				return install.ChangeSet{}, err
			}
			resource, err = cursoradapter.DesiredMCPResource(existing, mcpTarget, service.Paths.Binary)
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
	managedSecrets, err := service.installSecrets(ctx, request)
	if err != nil {
		return err
	}
	defer wipeManagedSecrets(managedSecrets)
	if err := service.putManagedSecrets(ctx, managedSecrets); err != nil {
		_ = service.restoreManagedSecrets(ctx, managedSecrets)
		return err
	}
	transaction := install.NewTransaction(service.Target, service.Ledger)
	if err := transaction.Apply(ctx, plan); err != nil {
		secretErr := service.restoreManagedSecrets(ctx, managedSecrets)
		if secretErr != nil {
			return errors.Join(err, fmt.Errorf("restore MLink secrets: %w", secretErr))
		}
		return err
	}
	if service.ControlPlaneStates != nil {
		if err := service.ControlPlaneStates.MarkControlPlaneState(ctx, "active"); err != nil {
			rollbackErr := transaction.Rollback(ctx, plan)
			secretErr := service.restoreManagedSecrets(ctx, managedSecrets)
			return errors.Join(err, rollbackErr, secretErr)
		}
	}
	if recorder, ok := service.Ledger.(InstallPlanRecorder); ok {
		agents := make([]string, len(plan.DetectedAgents))
		copy(agents, plan.DetectedAgents)
		if err := recorder.RecordInstallPlan(ctx, plan.PlanID, agents); err != nil {
			rollbackErr := transaction.Rollback(ctx, plan)
			secretErr := service.restoreManagedSecrets(ctx, managedSecrets)
			stateErr := service.ControlPlaneStates.MarkControlPlaneState(ctx, "provisioned")
			return errors.Join(err, rollbackErr, secretErr, stateErr)
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
			{Path: "group_sessions_per_user", Before: "true or configured", After: "false"},
			{Path: "thread_sessions_per_user", Before: "false or configured", After: "false"},
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

func (service *Service) desiredConfig(request InstallRequest, agents []Agent) (config.Config, error) {
	connection := request.Connection
	namespaceID, ownerSlug, err := normalizeOwner(request)
	if err != nil {
		return config.Config{}, err
	}
	connection.ProviderConfig = cloneAnyMap(connection.ProviderConfig)
	connection.SecretRefs = map[string]string{
		"token": "keychain://dev.mlink/connection/" + connection.ID + "/token",
	}
	connection.TenantID = ""
	connection.AgentID = ""
	connection.UserID = ""
	connection.IncludeAgentShared = false
	principals := map[string]config.Principal{"owner": {
		ID: "owner", CanonicalUserID: "usr_owner_" + ownerSlug, Kind: config.PrincipalPerson,
	}}
	spaces := map[string]config.MemorySpace{
		"personal-owner": {
			ID: "personal-owner", ConnectionID: connection.ID, TenantID: namespaceID, AgentID: ownerSlug + "-personal",
			IncludeAgentShared: true, PrincipalPolicy: config.PolicyFixed, PrincipalID: "owner",
		},
		"hermes-private": {
			ID: "hermes-private", ConnectionID: connection.ID, TenantID: namespaceID, AgentID: "hermes-private", PrincipalPolicy: config.PolicyExternalHMAC,
		},
		"hermes-groups": {
			ID: "hermes-groups", ConnectionID: connection.ID, TenantID: namespaceID, AgentID: "hermes-groups", PrincipalPolicy: config.PolicyGroupHMAC,
		},
	}
	adapters := make(map[string]config.Adapter, len(agents))
	for _, agent := range agents {
		adapter := config.Adapter{ID: string(agent), Enabled: true, SpaceID: "personal-owner"}
		if agent == Hermes {
			adapter.SpaceID = ""
			adapter.HermesRouting = &config.HermesRouting{OwnerSpaceID: "personal-owner", PrivateSpaceID: "hermes-private", GroupSpaceID: "hermes-groups"}
		}
		adapters[string(agent)] = adapter
	}
	bindings := make(map[string]config.BindingRef)
	if agentSelected(agents, Hermes) {
		bindings[request.OwnerBindingSlot.ID] = request.OwnerBindingSlot
	}
	configuration := config.Config{
		SchemaVersion:      2,
		NamespaceID:        namespaceID,
		ActiveConnectionID: connection.ID,
		Connections:        map[string]config.Connection{connection.ID: connection},
		Principals:         principals,
		Spaces:             spaces,
		Adapters:           adapters,
		Bindings:           bindings,
		Broker: config.Broker{
			HermesEndpoint: service.HermesEndpoint,
			ListenAddress:  service.HermesListenAddress,
		},
	}
	if err := config.Validate(configuration); err != nil {
		return config.Config{}, err
	}
	return configuration, nil
}

func validateConnection(connection config.Connection) error {
	if connection.ID == "" || connection.ProviderID != "dev.mlink.tencentdb" || connection.ProviderVersion == "" || connection.ConfigRevision == "" {
		return errors.New("complete TencentDB connection is required")
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

var ownerSlugInvalid = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

func normalizeOwner(request InstallRequest) (string, string, error) {
	namespaceID := strings.TrimSpace(request.Connection.TenantID)
	if namespaceID == "" {
		namespaceID = "personal"
	}
	slug := strings.Trim(ownerSlugInvalid.ReplaceAllString(strings.TrimSpace(request.OwnerSlug), "-"), "-")
	if slug == "" {
		slug = strings.Trim(ownerSlugInvalid.ReplaceAllString(strings.TrimSpace(request.Connection.UserID), "-"), "-")
	}
	if slug == "" || len(slug) > 96 {
		return "", "", errors.New("valid stable owner slug is required")
	}
	return namespaceID, slug, nil
}

func validateOwnerBinding(request InstallRequest) error {
	if request.OwnerBindingSlot.PrincipalID != "owner" {
		return errors.New("Hermes owner Binding must reference owner")
	}
	if err := config.ValidateBindingRef(request.OwnerBindingSlot); err != nil {
		return err
	}
	value := request.SecretInputs[OwnerBindingSecret]
	if len(value) == 0 || len(value) > 16*1024 || bytes.ContainsAny(value, "\r\n") {
		return errors.New("Hermes owner Binding input is required")
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
		case Codex, Pi, Hermes, Cursor:
			seen[agent] = true
		default:
			return nil, fmt.Errorf("unsupported Agent %q", agent)
		}
	}
	result := make([]Agent, 0, len(seen))
	for _, agent := range []Agent{Codex, Pi, Hermes, Cursor} {
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
	delete(document, "group_sessions_per_user")
	delete(document, "thread_sessions_per_user")
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

func wipe(value []byte) {
	for index := range value {
		value[index] = 0
	}
}

type managedSecret struct {
	account  string
	value    []byte
	previous []byte
	existed  bool
	written  bool
	delete   bool
}

func (service *Service) installSecrets(ctx context.Context, request InstallRequest) ([]managedSecret, error) {
	values := []managedSecret{
		{account: "connection/" + request.Connection.ID + "/token", value: append([]byte(nil), request.SecretInputs[MemoryCoreTokenSecret]...)},
		{account: "identity/hmac-key", value: append([]byte(nil), service.IdentityKey...)},
	}
	if agentSelected(request.Agents, Hermes) {
		bindingAccount, err := identity.BindingAccount(request.OwnerBindingSlot.ID)
		if err != nil {
			wipeManagedSecrets(values)
			return nil, err
		}
		values = append(values,
			managedSecret{account: "adapter/hermes/token", value: append([]byte(nil), service.HermesGrantToken...)},
			managedSecret{account: bindingAccount, value: append([]byte(nil), request.SecretInputs[OwnerBindingSecret]...)},
		)
	}
	for index := range values {
		previous, err := service.Secrets.Get(ctx, values[index].account)
		if err == nil {
			values[index].previous = previous
			values[index].existed = true
			if values[index].account == "identity/hmac-key" {
				if len(previous) != 32 {
					wipeManagedSecrets(values)
					return nil, errors.New("stored MLink identity key is invalid")
				}
				wipe(values[index].value)
				values[index].value = append([]byte(nil), previous...)
			}
			continue
		}
		if !errors.Is(err, fs.ErrNotExist) {
			wipeManagedSecrets(values)
			return nil, fmt.Errorf("read previous MLink secret for %q: %w", values[index].account, err)
		}
	}
	return values, nil
}

func agentSelected(agents []Agent, want Agent) bool {
	for _, agent := range agents {
		if agent == want {
			return true
		}
	}
	return false
}

func (service *Service) putManagedSecrets(ctx context.Context, values []managedSecret) error {
	for index := range values {
		var err error
		if values[index].delete {
			if !values[index].existed {
				continue
			}
			err = service.Secrets.Delete(ctx, values[index].account)
		} else {
			err = service.Secrets.Put(ctx, values[index].account, values[index].value)
		}
		if err != nil {
			return err
		}
		values[index].written = true
	}
	return nil
}

func (service *Service) restoreManagedSecrets(ctx context.Context, values []managedSecret) error {
	var restoreErrors []error
	for index := len(values) - 1; index >= 0; index-- {
		if !values[index].written {
			continue
		}
		var err error
		if values[index].existed {
			err = service.Secrets.Put(ctx, values[index].account, values[index].previous)
		} else {
			err = service.Secrets.Delete(ctx, values[index].account)
		}
		if err != nil {
			restoreErrors = append(restoreErrors, fmt.Errorf("%s: %w", values[index].account, err))
		}
	}
	return errors.Join(restoreErrors...)
}

func wipeManagedSecrets(values []managedSecret) {
	for index := range values {
		wipe(values[index].value)
		wipe(values[index].previous)
	}
}
