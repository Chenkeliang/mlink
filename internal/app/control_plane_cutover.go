package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"

	"gopkg.in/yaml.v3"

	"mlink/internal/config"
	"mlink/internal/install"
	"mlink/internal/journal"
)

type ControlPlaneCutoverRequest struct {
	DynamicAgentLimit int
}

func (service *Service) PlanControlPlaneCutover(ctx context.Context, request ControlPlaneCutoverRequest) (install.ChangeSet, error) {
	if service == nil || service.Target == nil || service.Ledger == nil || service.ControlPlaneStates == nil || service.UID <= 0 {
		return install.ChangeSet{}, errors.New("cutover target, ledger, and control-plane state are required")
	}
	if request.DynamicAgentLimit <= 0 || request.DynamicAgentLimit > 10_000 {
		return install.ChangeSet{}, errors.New("explicit dynamic Agent limit between 1 and 10000 is required")
	}
	currentData, currentMode, err := service.Target.Read(ctx, service.Paths.Config)
	if err != nil {
		return install.ChangeSet{}, fmt.Errorf("read active MLink config: %w", err)
	}
	current, err := config.Decode(currentData)
	if err != nil {
		return install.ChangeSet{}, fmt.Errorf("decode active MLink config: %w", err)
	}
	if current.SchemaVersion != 2 {
		return install.ChangeSet{}, errors.New("control-plane cutover requires an active schema v2 config")
	}
	state, err := service.ControlPlaneStates.LoadControlPlane(ctx)
	if err != nil {
		return install.ChangeSet{}, fmt.Errorf("load provisioned control plane: %w", err)
	}
	if state.State != "provisioned" || state.InstallationID != current.NamespaceID {
		return install.ChangeSet{}, errors.New("matching provisioned Stage A state is required")
	}
	proposed, err := buildCutoverConfig(current, state, request.DynamicAgentLimit)
	if err != nil {
		return install.ChangeSet{}, err
	}
	proposedData, err := yaml.Marshal(proposed)
	if err != nil {
		return install.ChangeSet{}, fmt.Errorf("render schema v3 config: %w", err)
	}
	beforeProtected, err := cutoverProtectedHash(current)
	if err != nil {
		return install.ChangeSet{}, err
	}
	afterProtected, err := cutoverProtectedHash(proposed)
	if err != nil {
		return install.ChangeSet{}, err
	}
	resources := []install.DesiredResource{
		{
			OwnerID: "dev.mlink.config", Target: service.Paths.Config, Content: proposedData, Mode: currentMode,
			SemanticDiff: []install.SemanticDiff{
				{Path: "schema_version", Before: "2", After: "3"},
				{Path: "legacy_scope_migration", Before: "not applicable", After: "disabled; retained backend memory stays inactive"},
				{Path: "routing:owner", Before: "legacy local IDs", After: "Core-generated Owner · L1/L2/L3"},
				{Path: "routing:hermes-private", Before: "HMAC-derived User ID", After: "dynamic Agent per principal · L1 only"},
				{Path: "routing:hermes-groups", Before: "HMAC-derived group User ID", After: "dynamic Agent per group · topic session · L1 only"},
			},
			ProtectedInvariants: []install.Invariant{{
				Name: "connection_secrets_broker_and_bindings", BeforeHash: beforeProtected, ProposedHash: afterProtected,
				Preserved: beforeProtected == afterProtected,
			}},
			Verify: func(content []byte) error {
				active, err := config.Decode(content)
				if err != nil {
					return err
				}
				return verifyGeneratedControlPlane(active, state)
			},
		},
		{
			OwnerID: "dev.mlink.broker", Target: "service:kickstart:dev.mlink.broker", Action: install.ActionService,
			Command:      []string{"launchctl", "kickstart", "-k", fmt.Sprintf("gui/%d/dev.mlink.broker", service.UID)},
			SemanticDiff: []install.SemanticDiff{{Path: "service:broker", Before: "schema v2 runtime", After: "schema v3 runtime"}},
		},
	}
	if adapter, enabled := current.Adapters[string(Hermes)]; enabled && adapter.Enabled {
		resources = append(resources, install.DesiredResource{
			OwnerID: "dev.mlink.adapter.hermes", Target: "service:restart:hermes-gateway", Action: install.ActionService,
			Command:      []string{"hermes", "gateway", "restart"},
			SemanticDiff: []install.SemanticDiff{{Path: "service:hermes", Before: "schema v2 Broker session", After: "schema v3 Broker session"}},
		})
	}
	changeSet, err := install.BuildChangeSet(service.Target, resources)
	if err != nil {
		return install.ChangeSet{}, err
	}
	changeSet.SelectedConnection = current.ActiveConnectionID
	return changeSet, nil
}

func (service *Service) ApplyControlPlaneCutover(ctx context.Context, planID string, request ControlPlaneCutoverRequest) error {
	plan, err := service.PlanControlPlaneCutover(ctx, request)
	if err != nil {
		return err
	}
	if plan.PlanID != planID {
		return fmt.Errorf("%w: supplied cutover plan no longer matches", install.ErrPlanStale)
	}
	configPlan := plan
	configPlan.Operations = nil
	configPlan.ProtectedInvariants = nil
	var serviceOperations []install.Operation
	for _, operation := range plan.Operations {
		if operation.Action == install.ActionService {
			serviceOperations = append(serviceOperations, operation)
			continue
		}
		configPlan.Operations = append(configPlan.Operations, operation)
		configPlan.ProtectedInvariants = append(configPlan.ProtectedInvariants, operation.ProtectedInvariants...)
	}
	transaction := install.NewTransaction(service.Target, service.Ledger)
	applied, err := transaction.ApplyDeferredOwnership(ctx, configPlan)
	if err != nil {
		return err
	}
	for _, operation := range serviceOperations {
		if _, err := service.Target.Run(ctx, operation.Command, bytes.NewReader(operation.CommandInput)); err != nil {
			return service.rollbackCutover(ctx, transaction, configPlan, serviceOperations, fmt.Errorf("restart %q: %w", operation.Target, err))
		}
	}
	if err := service.ControlPlaneStates.MarkControlPlaneState(ctx, "active"); err != nil {
		return service.rollbackCutover(ctx, transaction, configPlan, serviceOperations, fmt.Errorf("mark control plane active: %w", err))
	}
	if err := transaction.RecordOwnership(ctx, applied); err != nil {
		stateErr := service.ControlPlaneStates.MarkControlPlaneState(ctx, "provisioned")
		return service.rollbackCutover(ctx, transaction, configPlan, serviceOperations, errors.Join(fmt.Errorf("record cutover ownership: %w", err), stateErr))
	}
	return nil
}

func (service *Service) rollbackCutover(ctx context.Context, transaction install.Transaction, configPlan install.ChangeSet, services []install.Operation, cause error) error {
	rollbackErr := transaction.Rollback(ctx, configPlan)
	var restartErrors []error
	for _, operation := range services {
		if _, err := service.Target.Run(ctx, operation.Command, bytes.NewReader(operation.CommandInput)); err != nil {
			restartErrors = append(restartErrors, fmt.Errorf("restart restored %q: %w", operation.Target, err))
		}
	}
	return errors.Join(cause, rollbackErr, errors.Join(restartErrors...))
}

func buildCutoverConfig(current config.Config, state journal.ControlPlaneState, dynamicAgentLimit int) (config.Config, error) {
	connection, exists := current.Connections[current.ActiveConnectionID]
	if !exists || connection.ProviderID != "dev.mlink.tencentdb" || state.InstanceID == "" {
		return config.Config{}, errors.New("active TencentDB connection and Core instance are required")
	}
	connection.ProviderConfig = cloneAnyMap(connection.ProviderConfig)
	for _, key := range []string{"team_id", "tenant_id", "agent_id", "user_id", "include_agent_shared"} {
		delete(connection.ProviderConfig, key)
	}
	connection.ProviderConfig["base_url"] = "http://127.0.0.1:8420"
	connection.ProviderConfig["service_id"] = state.InstanceID
	connection.ConfigRevision = "rev-control-plane-1"
	connections := make(map[string]config.Connection, len(current.Connections))
	for id, value := range current.Connections {
		connections[id] = value
	}
	connections[current.ActiveConnectionID] = connection
	adapters := make(map[string]config.Adapter, len(current.Adapters))
	for id, adapter := range current.Adapters {
		adapter.ConnectionID = ""
		adapter.Config = nil
		if id == string(Hermes) {
			adapter.SpaceID = ""
			adapter.HermesRouting = &config.HermesRouting{OwnerSpaceID: "owner", PrivateSpaceID: "hermes-private", GroupSpaceID: "hermes-groups"}
		} else {
			adapter.SpaceID = "owner"
			adapter.HermesRouting = nil
		}
		adapters[id] = adapter
	}
	proposed := config.Config{
		SchemaVersion: 3, NamespaceID: current.NamespaceID, ActiveConnectionID: current.ActiveConnectionID,
		Connections: connections,
		Principals: map[string]config.Principal{"owner": {
			ID: "owner", CanonicalUserID: state.OwnerUserID, Kind: config.PrincipalPerson,
		}},
		Adapters: adapters, Bindings: current.Bindings, Broker: current.Broker,
		ControlPlane: &config.ControlPlane{
			ProviderID: "dev.mlink.tencentdb", InstanceID: state.InstanceID, PanelURL: "http://127.0.0.1:8125",
			OwnerUserID: state.OwnerUserID, OwnerTeamID: state.OwnerTeamID, OwnerAgentID: state.OwnerAgentID,
			OwnerAssetID: state.OwnerAssetID, DynamicAgentLimit: dynamicAgentLimit,
		},
		RoutingPolicies: map[string]config.RoutingPolicy{
			"owner":          {ID: "owner", Layers: []config.MemoryLayer{config.LayerL1, config.LayerL2, config.LayerL3}, AgentPolicy: config.AgentFixed},
			"hermes-private": {ID: "hermes-private", Layers: []config.MemoryLayer{config.LayerL1}, AgentPolicy: config.AgentDynamicPrincipal},
			"hermes-groups":  {ID: "hermes-groups", Layers: []config.MemoryLayer{config.LayerL1}, AgentPolicy: config.AgentDynamicGroup, SessionPolicy: config.SessionPerTopic},
		},
	}
	if err := config.Validate(proposed); err != nil {
		return config.Config{}, err
	}
	return proposed, nil
}

func verifyGeneratedControlPlane(configuration config.Config, state journal.ControlPlaneState) error {
	if configuration.SchemaVersion != 3 || configuration.ControlPlane == nil || len(configuration.Spaces) != 0 ||
		configuration.ControlPlane.OwnerUserID != state.OwnerUserID || configuration.ControlPlane.OwnerTeamID != state.OwnerTeamID ||
		configuration.ControlPlane.OwnerAgentID != state.OwnerAgentID || configuration.ControlPlane.OwnerAssetID != state.OwnerAssetID {
		return errors.New("generated control-plane IDs are not exclusively active")
	}
	for _, connection := range configuration.Connections {
		if connection.TenantID != "" || connection.AgentID != "" || connection.UserID != "" || connection.IncludeAgentShared {
			return errors.New("legacy connection identity remains active")
		}
		for _, key := range []string{"team_id", "tenant_id", "agent_id", "user_id", "include_agent_shared"} {
			if _, exists := connection.ProviderConfig[key]; exists {
				return errors.New("legacy Provider identity remains active")
			}
		}
	}
	return nil
}

func cutoverProtectedHash(configuration config.Config) (string, error) {
	document := struct {
		SecretRefs map[string]map[string]string `yaml:"secret_refs"`
		Broker     config.Broker                `yaml:"broker"`
		Bindings   map[string]config.BindingRef `yaml:"bindings"`
	}{Broker: configuration.Broker, Bindings: configuration.Bindings, SecretRefs: map[string]map[string]string{}}
	for id, connection := range configuration.Connections {
		document.SecretRefs[id] = connection.SecretRefs
	}
	encoded, err := yaml.Marshal(document)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}
