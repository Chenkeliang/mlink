package main

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"mlink/internal/app"
	"mlink/internal/config"
	"mlink/internal/controlplane"
	"mlink/internal/identity"
	"mlink/internal/install"
	"mlink/internal/journal"
	"mlink/internal/layout"
	"mlink/internal/panel"
	"mlink/internal/provider/lifecycle"
	"mlink/internal/provider/tencentdb"
	"mlink/internal/secret"
	"mlink/internal/workspacebackup"
)

const maxRestoreSecretSection = 1 << 20

type restoreSecretMaterial struct {
	GatewayToken []byte `json:"gateway_token"`
	LLMAPIKey    []byte `json:"memory_llm_api_key"`
	HermesGrant  []byte `json:"hermes_grant,omitempty"`
	AdminUserKey []byte `json:"admin_user_key"`
	OwnerUserKey []byte `json:"owner_user_key"`
}

func (material *restoreSecretMaterial) wipe() {
	if material == nil {
		return
	}
	for _, value := range [][]byte{material.GatewayToken, material.LLMAPIKey, material.HermesGrant, material.AdminUserKey, material.OwnerUserKey} {
		wipeRuntimeSecret(value)
	}
	*material = restoreSecretMaterial{}
}

type localWorkspaceRestorer struct {
	paths          layout.Paths
	secrets        secret.Store
	passphrase     []byte
	manifest       workspacebackup.Manifest
	provider       lifecycle.RestoreRequest
	bundle         app.WorkspaceBundle
	statePath      string
	selectedAgents []app.Agent
	agents         restoredAgentLifecycle
	staged         restoreSecretMaterial
	createdFiles   []string
	createdSecrets []string
}

type restoredAgentLifecycle interface {
	Apply(context.Context, []app.Agent) error
	Verify(context.Context, []app.Agent) error
	Rollback(context.Context) error
}

type restoreMemoryLedger struct {
	mu      sync.Mutex
	backups map[string]install.Backup
	owned   []install.OwnedResource
}

type deferredRestoreOperationStore struct {
	path      string
	statePath string
	mu        sync.Mutex
	latest    journal.RestoreOperation
}

func (store *deferredRestoreOperationStore) SaveRestoreOperation(ctx context.Context, operation journal.RestoreOperation) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := journal.ValidateRestoreOperation(operation); err != nil {
		return err
	}
	store.latest = operation
	encoded, err := json.Marshal(operation)
	if err != nil {
		return errors.New("encode restore operation sidecar")
	}
	encoded = append(encoded, '\n')
	if err := (install.LocalTarget{}).WriteAtomic(ctx, store.statePath, encoded, 0o600); err != nil {
		return err
	}
	if _, err := os.Stat(store.path); errors.Is(err, fs.ErrNotExist) {
		if operation.Phase == journal.RestorePhaseComplete || operation.Phase == journal.RestorePhaseRolledBack {
			if removeErr := os.Remove(store.statePath); removeErr != nil && !errors.Is(removeErr, fs.ErrNotExist) {
				return removeErr
			}
		}
		return nil
	} else if err != nil {
		return err
	}
	destination, err := journal.Open(ctx, store.path)
	if err != nil {
		return err
	}
	defer destination.Close()
	if err := destination.SaveRestoreOperation(ctx, operation); err != nil {
		return err
	}
	if operation.Phase == journal.RestorePhaseComplete || operation.Phase == journal.RestorePhaseRolledBack {
		if err := os.Remove(store.statePath); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return nil
}

func (ledger *restoreMemoryLedger) SaveBackup(_ context.Context, backup install.Backup) error {
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	if ledger.backups == nil {
		ledger.backups = map[string]install.Backup{}
	}
	backup.Content = append([]byte(nil), backup.Content...)
	ledger.backups[backup.PlanID+"\x00"+backup.Target] = backup
	return nil
}

func (ledger *restoreMemoryLedger) LoadBackup(_ context.Context, planID, target string) (install.Backup, error) {
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	backup, exists := ledger.backups[planID+"\x00"+target]
	if !exists {
		return install.Backup{}, fs.ErrNotExist
	}
	backup.Content = append([]byte(nil), backup.Content...)
	return backup, nil
}

func (ledger *restoreMemoryLedger) RecordOwned(_ context.Context, resource install.OwnedResource) error {
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	ledger.owned = append(ledger.owned, resource)
	return nil
}

type runtimeRestoreAgents struct {
	runtime      *runtimeApplication
	plan         install.ChangeSet
	target       install.Target
	applied      bool
	panelStarted bool
}

type runtimeWorkspaceResumer struct {
	target install.Target
	driver lifecycle.SnapshotDriver
	uid    int
	plist  string
}

func (resumer runtimeWorkspaceResumer) ResumeWorkspaceServices(ctx context.Context) error {
	domain := fmt.Sprintf("gui/%d", resumer.uid)
	service := domain + "/dev.mlink.broker"
	if _, err := resumer.target.Run(ctx, []string{"launchctl", "print", service}, nil); err != nil {
		deadline := time.Now().Add(3 * time.Second)
		for {
			_, bootstrapErr := resumer.target.Run(ctx, []string{"launchctl", "bootstrap", domain, resumer.plist}, nil)
			if bootstrapErr == nil {
				break
			}
			if _, printErr := resumer.target.Run(ctx, []string{"launchctl", "print", service}, nil); printErr == nil {
				break
			}
			if time.Now().After(deadline) {
				return errors.New("Broker did not resume after workspace snapshot")
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(100 * time.Millisecond):
			}
		}
	}
	if resumer.driver == nil {
		return errors.New("snapshot runtime verification is unavailable")
	}
	source, err := resumer.driver.Detect(ctx)
	if err != nil || !source.CoreRunning || source.HubContainer != "" && !source.HubRunning {
		return errors.New("memory services did not resume after workspace snapshot")
	}
	return nil
}

func (lifecycle *runtimeRestoreAgents) Apply(ctx context.Context, selected []app.Agent) error {
	if lifecycle == nil || lifecycle.runtime == nil || len(selected) == 0 {
		return errors.New("restored Agent lifecycle is unavailable")
	}
	configuration, err := (config.Store{Path: lifecycle.runtime.paths.Config}).Load()
	if err != nil {
		return err
	}
	store, ledger, err := lifecycle.runtime.openLedger(ctx)
	if err != nil {
		return err
	}
	defer store.Close()
	state, err := store.LoadControlPlane(ctx)
	if err != nil {
		return err
	}
	connection := configuration.Connections[configuration.ActiveConnectionID]
	connection.TenantID = configuration.NamespaceID
	request := app.InstallRequest{
		Agents: selected, Connection: connection, OwnerSlug: "restored-owner", DynamicAgentLimit: state.DynamicAgentLimit,
		SecretInputs: map[string][]byte{},
	}
	gatewayAccount, err := runtimeCredentialAccount(connection.SecretRefs["token"])
	if err != nil {
		return err
	}
	gateway, err := lifecycle.runtime.secretStore().Get(ctx, gatewayAccount)
	if err != nil {
		return err
	}
	defer wipeRuntimeSecret(gateway)
	request.SecretInputs[app.MemoryCoreTokenSecret] = append([]byte(nil), gateway...)
	defer wipeRuntimeSecret(request.SecretInputs[app.MemoryCoreTokenSecret])
	if containsAgent(selected, app.Hermes) {
		ids := make([]string, 0, len(configuration.Bindings))
		for id := range configuration.Bindings {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		if len(ids) == 0 {
			return errors.New("restored Hermes binding is unavailable")
		}
		request.OwnerBindingSlot = configuration.Bindings[ids[0]]
		account, err := identity.BindingAccount(ids[0])
		if err != nil {
			return err
		}
		binding, err := lifecycle.runtime.secretStore().Get(ctx, account)
		if err != nil {
			return err
		}
		defer wipeRuntimeSecret(binding)
		request.SecretInputs[app.OwnerBindingSecret] = append([]byte(nil), binding...)
		defer wipeRuntimeSecret(request.SecretInputs[app.OwnerBindingSecret])
	}
	service, request, err := lifecycle.runtime.prepare(ctx, request, ledger)
	if err != nil {
		return err
	}
	identityKey, err := lifecycle.runtime.secretStore().Get(ctx, "identity/hmac-key")
	if err != nil || len(identityKey) != 32 {
		wipeRuntimeSecret(identityKey)
		return errors.New("restored identity key is unavailable")
	}
	defer wipeRuntimeSecret(identityKey)
	service.IdentityKey = append([]byte(nil), identityKey...)
	defer wipeRuntimeSecret(service.IdentityKey)
	if containsAgent(selected, app.Hermes) {
		grant, err := lifecycle.runtime.secretStore().Get(ctx, "adapter/hermes/token")
		if err != nil {
			return err
		}
		defer wipeRuntimeSecret(grant)
		service.HermesGrantToken = append([]byte(nil), grant...)
		defer wipeRuntimeSecret(service.HermesGrantToken)
	}
	service.ControlPlaneStates = store
	service.BlockingEvents = store
	plan, err := service.PlanInstall(ctx, request)
	if err != nil {
		return err
	}
	if err := service.ApplyInstall(ctx, plan.PlanID, request); err != nil {
		return err
	}
	lifecycle.plan, lifecycle.target, lifecycle.applied = plan, service.Target, true
	return nil
}

func (lifecycle *runtimeRestoreAgents) Verify(ctx context.Context, selected []app.Agent) error {
	panelPlan, err := lifecycle.runtime.PlanPanelRuntime(ctx)
	if err != nil {
		return err
	}
	if err := lifecycle.runtime.ApplyPanelRuntime(ctx, panelPlan.PlanID); err != nil {
		return err
	}
	lifecycle.panelStarted = true
	deadline := time.Now().Add(60 * time.Second)
	for {
		report, err := lifecycle.runtime.Doctor(ctx, selected)
		if err == nil && report.ExitCode() != 1 {
			return nil
		}
		if time.Now().After(deadline) {
			return errors.New("restored Agent integration verification failed")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

func (lifecycle *runtimeRestoreAgents) Rollback(ctx context.Context) error {
	if lifecycle == nil {
		return nil
	}
	var rollbackErrors []error
	if lifecycle.panelStarted {
		if _, err := (install.LocalTarget{}).Run(ctx, []string{"docker", "rm", "-f", panel.ContainerName}, nil); err != nil {
			rollbackErrors = append(rollbackErrors, err)
		}
		lifecycle.panelStarted = false
	}
	if !lifecycle.applied {
		return errors.Join(rollbackErrors...)
	}
	store, ledger, err := lifecycle.runtime.openLedger(ctx)
	if err != nil {
		return errors.Join(append(rollbackErrors, err)...)
	}
	defer store.Close()
	target := lifecycle.target
	if target == nil {
		target = install.LocalTarget{}
	}
	err = install.NewTransaction(target, ledger).Rollback(ctx, lifecycle.plan)
	lifecycle.applied = false
	return errors.Join(append(rollbackErrors, err)...)
}

type restoreMetadataClient struct{ restorer *localWorkspaceRestorer }

func (client restoreMetadataClient) metadata(ctx context.Context) (tencentdb.MetadataClient, error) {
	request, err := client.restorer.ProviderRequest(ctx, client.restorer.manifest)
	if err != nil {
		return nil, err
	}
	defer request.Wipe()
	provider, err := tencentdb.NewClient(tencentdb.Config{BaseURL: request.Endpoint, Token: string(request.GatewayToken), ServiceID: client.restorer.manifest.Provider.InstanceID})
	if err != nil {
		return nil, err
	}
	return tencentdb.NewMetadataClient(provider), nil
}

func (client restoreMetadataClient) InitAdmin(context.Context, tencentdb.InitAdminRequest) (tencentdb.UserCredential, error) {
	return tencentdb.UserCredential{}, errors.New("restore never initializes admin identity")
}
func (client restoreMetadataClient) CreateUser(context.Context, []byte, tencentdb.CreateUserRequest) (tencentdb.UserCredential, error) {
	return tencentdb.UserCredential{}, errors.New("restore never creates users")
}
func (client restoreMetadataClient) CreateTeam(context.Context, []byte, tencentdb.CreateTeamRequest) (tencentdb.Team, error) {
	return tencentdb.Team{}, errors.New("restore never creates teams")
}
func (client restoreMetadataClient) CreateAgent(context.Context, []byte, tencentdb.CreateAgentRequest) (tencentdb.Agent, error) {
	return tencentdb.Agent{}, errors.New("restore never creates agents")
}
func (client restoreMetadataClient) VerifyUser(ctx context.Context, key []byte) (tencentdb.User, error) {
	metadata, err := client.metadata(ctx)
	if err != nil {
		return tencentdb.User{}, err
	}
	return metadata.VerifyUser(ctx, key)
}
func (client restoreMetadataClient) ListTeams(ctx context.Context, key []byte, request tencentdb.ListTeamsRequest) ([]tencentdb.Team, error) {
	metadata, err := client.metadata(ctx)
	if err != nil {
		return nil, err
	}
	return metadata.ListTeams(ctx, key, request)
}
func (client restoreMetadataClient) ListAgents(ctx context.Context, key []byte, request tencentdb.ListAgentsRequest) ([]tencentdb.Agent, error) {
	metadata, err := client.metadata(ctx)
	if err != nil {
		return nil, err
	}
	return metadata.ListAgents(ctx, key, request)
}
func (client restoreMetadataClient) GetAsset(ctx context.Context, key []byte, assetID string) (tencentdb.Asset, error) {
	metadata, err := client.metadata(ctx)
	if err != nil {
		return tencentdb.Asset{}, err
	}
	return metadata.GetAsset(ctx, key, assetID)
}
func (client restoreMetadataClient) InstanceQuota(ctx context.Context, key []byte) (tencentdb.InstanceQuota, error) {
	metadata, err := client.metadata(ctx)
	if err != nil {
		return tencentdb.InstanceQuota{}, err
	}
	return metadata.InstanceQuota(ctx, key)
}

func (restorer *localWorkspaceRestorer) PlanRestore(ctx context.Context, request app.WorkspaceRestoreRequest, manifest workspacebackup.Manifest) (install.ChangeSet, error) {
	if restorer == nil || restorer.secrets == nil || !filepath.IsAbs(request.BundlePath) || len(request.Passphrase) < 12 {
		return install.ChangeSet{}, errors.New("local workspace restore dependencies are required")
	}
	if restorer.statePath != "" {
		if _, err := os.Lstat(restorer.statePath); err == nil {
			return install.ChangeSet{}, errors.New("an interrupted restore operation requires resolution before retry")
		} else if !errors.Is(err, fs.ErrNotExist) {
			return install.ChangeSet{}, errors.New("inspect restore operation sidecar")
		}
	}
	for _, path := range []string{restorer.paths.Config, restorer.paths.Journal, restorer.paths.PanelRegistry} {
		if _, err := os.Lstat(path); err == nil {
			return install.ChangeSet{}, fmt.Errorf("restore target %q already exists", filepath.Base(path))
		} else if !errors.Is(err, fs.ErrNotExist) {
			return install.ChangeSet{}, errors.New("inspect local restore target")
		}
	}
	for _, account := range []string{
		controlplane.OwnerUserKeyAccount, controlplane.AdminUserKeyAccount, "provider/tencentdb/llm-api-key", "identity/hmac-key", "adapter/hermes/token",
	} {
		value, err := restorer.secrets.Get(ctx, account)
		wipeRuntimeSecret(value)
		if err == nil {
			return install.ChangeSet{}, errors.New("restore target contains an existing MLink credential")
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return install.ChangeSet{}, errors.New("inspect local restore credential")
		}
	}
	if restorer.bundle != nil {
		accounts, err := restorer.inspectBundleAccounts(ctx, request)
		if err != nil {
			return install.ChangeSet{}, err
		}
		for _, account := range accounts {
			value, err := restorer.secrets.Get(ctx, account)
			wipeRuntimeSecret(value)
			if err == nil {
				return install.ChangeSet{}, errors.New("restore target contains an existing MLink credential")
			}
			if !errors.Is(err, fs.ErrNotExist) {
				return install.ChangeSet{}, errors.New("inspect local restore credential")
			}
		}
	}
	restorer.manifest = manifest
	restorer.selectedAgents = append([]app.Agent(nil), request.SelectedAgents...)
	if len(restorer.selectedAgents) == 0 {
		for _, agent := range restorer.manifest.Agents {
			restorer.selectedAgents = append(restorer.selectedAgents, app.Agent(agent))
		}
	}
	target := install.Target(install.LocalTarget{})
	return install.BuildChangeSet(target, []install.DesiredResource{{
		OwnerID: "dev.mlink.workspace-restore", Target: "artifact:restore-local-state", Action: install.ActionUnchanged,
		SemanticDiff: []install.SemanticDiff{{Path: "restore.local", Before: "absent", After: "MLink state, Keychain and selected Agent integrations"}},
	}})
}

func (restorer *localWorkspaceRestorer) inspectBundleAccounts(ctx context.Context, request app.WorkspaceRestoreRequest) ([]string, error) {
	accounts := map[string]bool{}
	_, err := restorer.bundle.Open(ctx, request.BundlePath, request.Passphrase, func(section workspacebackup.Section, reader io.Reader) error {
		switch section {
		case workspacebackup.SectionMLink:
			archive := tar.NewReader(reader)
			for {
				header, err := archive.Next()
				if errors.Is(err, io.EOF) {
					return errors.New("restored MLink config is missing")
				}
				if err != nil || header.Typeflag != tar.TypeReg || header.Size < 0 || header.Size > 4<<20 {
					return errors.New("restored MLink archive is invalid")
				}
				if header.Name != "config.yaml" {
					continue
				}
				content, err := io.ReadAll(io.LimitReader(archive, header.Size+1))
				if err != nil || int64(len(content)) != header.Size {
					return errors.New("read restored MLink config")
				}
				configuration, err := config.Decode(content)
				if err != nil {
					return err
				}
				connection := configuration.Connections[configuration.ActiveConnectionID]
				account, err := runtimeCredentialAccount(connection.SecretRefs["token"])
				if err != nil {
					return err
				}
				accounts[account] = true
				return nil
			}
		case workspacebackup.SectionIdentity:
			content, err := io.ReadAll(io.LimitReader(reader, (8<<20)+1))
			if err != nil || len(content) == 0 || len(content) > 8<<20 {
				wipeRuntimeSecret(content)
				return errors.New("identity restore section is invalid")
			}
			defer wipeRuntimeSecret(content)
			bundle, err := identity.DecryptBundle(content, request.Passphrase)
			if err != nil {
				return err
			}
			defer bundle.Wipe()
			for _, binding := range bundle.Bindings {
				account, err := identity.BindingAccount(binding.Ref.ID)
				if err != nil {
					return err
				}
				accounts[account] = true
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	result := make([]string, 0, len(accounts))
	for account := range accounts {
		result = append(result, account)
	}
	sort.Strings(result)
	return result, nil
}

func (restorer *localWorkspaceRestorer) StageSection(_ context.Context, section workspacebackup.Section, reader io.Reader) error {
	if restorer == nil || reader == nil {
		return errors.New("restore staging section is required")
	}
	if section == workspacebackup.SectionMLink {
		return restorer.stageCoreConfiguration(reader)
	}
	if section != workspacebackup.SectionSecrets {
		return errors.New("unsupported restore staging section")
	}
	material, err := decodeRestoreSecrets(reader)
	if err != nil {
		return err
	}
	restorer.staged.wipe()
	restorer.staged = material
	return nil
}

func (restorer *localWorkspaceRestorer) stageCoreConfiguration(reader io.Reader) error {
	archive := tar.NewReader(reader)
	for {
		header, err := archive.Next()
		if errors.Is(err, io.EOF) {
			return errors.New("MemoryCore configuration is missing from workspace backup")
		}
		if err != nil || header.Typeflag != tar.TypeReg || header.Size < 0 || header.Size > 1<<20 {
			return errors.New("workspace configuration archive is invalid")
		}
		if header.Name != "memorycore/tdai-gateway.yaml" {
			continue
		}
		path := filepath.Join(restorer.paths.Home, "memorycore", "tdai-gateway.yaml")
		if err := writeRestoreFile(path, archive, header.Size); err != nil {
			return err
		}
		restorer.createdFiles = append(restorer.createdFiles, path)
		return nil
	}
}

func decodeRestoreSecrets(reader io.Reader) (restoreSecretMaterial, error) {
	content, err := io.ReadAll(io.LimitReader(reader, maxRestoreSecretSection+1))
	if err != nil || len(content) == 0 || len(content) > maxRestoreSecretSection {
		wipeRuntimeSecret(content)
		return restoreSecretMaterial{}, errors.New("restore secret section is invalid")
	}
	defer wipeRuntimeSecret(content)
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	var material restoreSecretMaterial
	if err := decoder.Decode(&material); err != nil {
		material.wipe()
		return restoreSecretMaterial{}, errors.New("decode restore secret section")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) || len(material.GatewayToken) == 0 || len(material.LLMAPIKey) == 0 || len(material.AdminUserKey) == 0 || len(material.OwnerUserKey) == 0 {
		material.wipe()
		return restoreSecretMaterial{}, errors.New("restore secret section fields are invalid")
	}
	return material, nil
}

func (restorer *localWorkspaceRestorer) ProviderRequest(_ context.Context, manifest workspacebackup.Manifest) (lifecycle.RestoreRequest, error) {
	if restorer == nil {
		return lifecycle.RestoreRequest{}, errors.New("local restore provider request is unavailable")
	}
	request := lifecycle.RestoreRequest{
		ProviderID: manifest.Provider.ProviderID, CoreContainer: tencentdb.MemoryCoreContainerName,
		CoreVolume: tencentdb.MemoryCoreVolumeName, KnowledgeVolume: panel.VolumeName, CoreNetwork: tencentdb.MemoryCoreNetworkName,
		CoreConfigPath: filepath.Join(restorer.paths.Home, "memorycore", "tdai-gateway.yaml"), Endpoint: "http://127.0.0.1:8420",
		GatewayToken: append([]byte(nil), restorer.staged.GatewayToken...), LLMAPIKey: append([]byte(nil), restorer.staged.LLMAPIKey...),
		OwnerUserKey: append([]byte(nil), restorer.staged.OwnerUserKey...),
	}
	if restorer.provider.CoreContainer != "" {
		request.CoreContainer = restorer.provider.CoreContainer
	}
	if restorer.provider.CoreVolume != "" {
		request.CoreVolume = restorer.provider.CoreVolume
	}
	if restorer.provider.KnowledgeVolume != "" {
		request.KnowledgeVolume = restorer.provider.KnowledgeVolume
	}
	if restorer.provider.CoreNetwork != "" {
		request.CoreNetwork = restorer.provider.CoreNetwork
	}
	if restorer.provider.CoreConfigPath != "" {
		request.CoreConfigPath = restorer.provider.CoreConfigPath
	}
	if restorer.provider.Endpoint != "" {
		request.Endpoint = restorer.provider.Endpoint
	}
	return request, nil
}

func (restorer *localWorkspaceRestorer) ApplySection(ctx context.Context, section workspacebackup.Section, reader io.Reader) error {
	if restorer == nil || reader == nil {
		return errors.New("local restore section is required")
	}
	switch section {
	case workspacebackup.SectionMLink:
		return restorer.extractMLink(reader)
	case workspacebackup.SectionIdentity:
		return restorer.restoreIdentity(ctx, reader)
	case workspacebackup.SectionSecrets:
		return restorer.restoreSecrets(ctx, reader)
	case workspacebackup.SectionAgents:
		return restorer.restoreAgents(ctx, reader)
	default:
		return errors.New("unsupported local restore section")
	}
}

func (restorer *localWorkspaceRestorer) restoreIdentity(ctx context.Context, reader io.Reader) error {
	content, err := io.ReadAll(io.LimitReader(reader, (8<<20)+1))
	if err != nil || len(content) == 0 || len(content) > 8<<20 {
		wipeRuntimeSecret(content)
		return errors.New("identity restore section is invalid")
	}
	defer wipeRuntimeSecret(content)
	bundle, err := identity.DecryptBundle(content, restorer.passphrase)
	if err != nil {
		return errors.New("decrypt identity restore section")
	}
	defer bundle.Wipe()
	control := bundle.ControlPlane
	expected := restorer.manifest.ControlPlane
	if bundle.SchemaVersion != 2 || control == nil || control.InstallationID != expected.InstallationID || control.InstanceID != expected.InstanceID ||
		control.OwnerUserID != expected.OwnerUserID || control.OwnerTeamID != expected.OwnerTeamID || control.OwnerAgentID != expected.OwnerAgentID || control.OwnerAssetID != expected.OwnerAssetID {
		return errors.New("restored identity differs from encrypted manifest")
	}
	if len(restorer.staged.AdminUserKey) != 0 && (!bytes.Equal(bundle.AdminUserKey, restorer.staged.AdminUserKey) || !bytes.Equal(bundle.OwnerUserKey, restorer.staged.OwnerUserKey)) {
		return errors.New("restored identity credentials differ from secret section")
	}
	values := map[string][]byte{
		"identity/hmac-key": bundle.IdentityKey, controlplane.AdminUserKeyAccount: bundle.AdminUserKey, controlplane.OwnerUserKeyAccount: bundle.OwnerUserKey,
	}
	for _, binding := range bundle.Bindings {
		account, err := identity.BindingAccount(binding.Ref.ID)
		if err != nil {
			return err
		}
		values[account] = binding.Value
	}
	for account, value := range values {
		if err := restorer.putSecret(ctx, account, value); err != nil {
			return err
		}
	}
	return nil
}

func (restorer *localWorkspaceRestorer) restoreSecrets(ctx context.Context, reader io.Reader) error {
	material, err := decodeRestoreSecrets(reader)
	if err != nil {
		return err
	}
	defer material.wipe()
	if !sameRestoreSecrets(material, restorer.staged) {
		return errors.New("restore secret section changed after verification")
	}
	configuration, err := (config.Store{Path: restorer.paths.Config}).Load()
	if err != nil {
		return errors.New("load restored MLink configuration")
	}
	connection := configuration.Connections[configuration.ActiveConnectionID]
	gatewayAccount, err := runtimeCredentialAccount(connection.SecretRefs["token"])
	if err != nil {
		return err
	}
	values := map[string][]byte{
		gatewayAccount: material.GatewayToken, "provider/tencentdb/llm-api-key": material.LLMAPIKey,
		controlplane.AdminUserKeyAccount: material.AdminUserKey, controlplane.OwnerUserKeyAccount: material.OwnerUserKey,
	}
	if len(material.HermesGrant) != 0 {
		values["adapter/hermes/token"] = material.HermesGrant
	}
	for account, value := range values {
		if err := restorer.putSecret(ctx, account, value); err != nil {
			return err
		}
	}
	return nil
}

func (restorer *localWorkspaceRestorer) restoreAgents(ctx context.Context, reader io.Reader) error {
	content, err := io.ReadAll(io.LimitReader(reader, (64<<10)+1))
	if err != nil || len(content) == 0 || len(content) > 64<<10 {
		return errors.New("Agent restore section is invalid")
	}
	var document struct {
		Agents []string `json:"agents"`
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil {
		return errors.New("decode Agent restore section")
	}
	recorded := make(map[app.Agent]bool, len(document.Agents))
	for _, value := range document.Agents {
		recorded[app.Agent(value)] = true
	}
	for _, selected := range restorer.selectedAgents {
		if !recorded[selected] {
			return errors.New("selected Agent is absent from backup")
		}
	}
	if restorer.agents == nil {
		return errors.New("Agent restore lifecycle is unavailable")
	}
	return restorer.agents.Apply(ctx, restorer.selectedAgents)
}

func (restorer *localWorkspaceRestorer) putSecret(ctx context.Context, account string, value []byte) error {
	current, err := restorer.secrets.Get(ctx, account)
	if err == nil {
		defer wipeRuntimeSecret(current)
		for _, created := range restorer.createdSecrets {
			if created == account && bytes.Equal(current, value) {
				return nil
			}
		}
		return errors.New("restore credential collision")
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return errors.New("inspect restore credential")
	}
	if err := restorer.secrets.Put(ctx, account, value); err != nil {
		return errors.New("store restored credential")
	}
	restorer.createdSecrets = append(restorer.createdSecrets, account)
	return nil
}

func runtimeCredentialAccount(reference string) (string, error) {
	const prefix = "keychain://dev.mlink/"
	if !strings.HasPrefix(reference, prefix) || len(reference) == len(prefix) {
		return "", errors.New("restored credential reference is invalid")
	}
	return strings.TrimPrefix(reference, prefix), nil
}

func sameRestoreSecrets(left, right restoreSecretMaterial) bool {
	return bytes.Equal(left.GatewayToken, right.GatewayToken) && bytes.Equal(left.LLMAPIKey, right.LLMAPIKey) && bytes.Equal(left.HermesGrant, right.HermesGrant) &&
		bytes.Equal(left.AdminUserKey, right.AdminUserKey) && bytes.Equal(left.OwnerUserKey, right.OwnerUserKey)
}

func (restorer *localWorkspaceRestorer) extractMLink(reader io.Reader) error {
	archive := tar.NewReader(reader)
	seen := map[string]bool{}
	for {
		header, err := archive.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil || header.Typeflag != tar.TypeReg || header.Size < 0 || header.Size > 1<<30 || seen[header.Name] {
			return errors.New("MLink restore archive is invalid")
		}
		seen[header.Name] = true
		destination, err := restorer.restoreArchivePath(header.Name)
		if err != nil {
			return err
		}
		if err := writeRestoreFile(destination, archive, header.Size); err != nil {
			if header.Name != "memorycore/tdai-gateway.yaml" || !restorer.createdFile(destination) {
				return err
			}
			content, readErr := io.ReadAll(io.LimitReader(archive, header.Size+1))
			existing, fileErr := os.ReadFile(destination)
			if readErr != nil || fileErr != nil || int64(len(content)) != header.Size || !bytes.Equal(content, existing) {
				return errors.New("staged MemoryCore configuration differs from archive")
			}
			continue
		}
		restorer.createdFiles = append(restorer.createdFiles, destination)
	}
	if !seen["config.yaml"] || !seen["journal.db"] || !seen["memorycore/tdai-gateway.yaml"] {
		return errors.New("MLink restore archive is incomplete")
	}
	return nil
}

func (restorer *localWorkspaceRestorer) createdFile(path string) bool {
	for _, created := range restorer.createdFiles {
		if created == path {
			return true
		}
	}
	return false
}

func (restorer *localWorkspaceRestorer) restoreArchivePath(name string) (string, error) {
	clean := filepath.ToSlash(filepath.Clean(name))
	if clean != name || strings.HasPrefix(clean, "../") || filepath.IsAbs(name) {
		return "", errors.New("MLink restore archive path is unsafe")
	}
	switch clean {
	case "config.yaml":
		return restorer.paths.Config, nil
	case "journal.db":
		return restorer.paths.Journal, nil
	case "memorycore/tdai-gateway.yaml":
		return filepath.Join(restorer.paths.Home, "memorycore", "tdai-gateway.yaml"), nil
	case "panel/metadata-instances.json":
		return restorer.paths.PanelRegistry, nil
	default:
		if strings.HasPrefix(clean, "backups/") && len(clean) > len("backups/") {
			return filepath.Join(restorer.paths.Backups, filepath.FromSlash(strings.TrimPrefix(clean, "backups/"))), nil
		}
		return "", errors.New("MLink restore archive contains an unknown path")
	}
}

func writeRestoreFile(path string, reader io.Reader, size int64) error {
	if err := ensureRestoreDirectory(filepath.Dir(path)); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return errors.New("create restored MLink file")
	}
	written, copyErr := io.CopyN(file, reader, size)
	syncErr := file.Sync()
	closeErr := file.Close()
	if copyErr != nil || written != size || syncErr != nil || closeErr != nil {
		_ = os.Remove(path)
		return errors.New("write restored MLink file")
	}
	return nil
}

func ensureRestoreDirectory(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return errors.New("create restore directory")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&fs.ModeSymlink != 0 {
		return errors.New("restore directory is unsafe")
	}
	return os.Chmod(path, 0o700)
}

func (restorer *localWorkspaceRestorer) VerifyRestore(ctx context.Context, manifest workspacebackup.Manifest) error {
	configuration, err := (config.Store{Path: restorer.paths.Config}).Load()
	if err != nil || configuration.ControlPlane == nil {
		return errors.New("restored MLink configuration is invalid")
	}
	control := configuration.ControlPlane
	expected := manifest.ControlPlane
	if control.InstanceID != expected.InstanceID || control.OwnerUserID != expected.OwnerUserID || control.OwnerTeamID != expected.OwnerTeamID || control.OwnerAgentID != expected.OwnerAgentID || control.OwnerAssetID != expected.OwnerAssetID {
		return errors.New("restored MLink control plane differs from manifest")
	}
	if restorer.agents != nil {
		if err := restorer.agents.Verify(ctx, restorer.selectedAgents); err != nil {
			return err
		}
	}
	restorer.staged.wipe()
	wipeRuntimeSecret(restorer.passphrase)
	restorer.passphrase = nil
	return nil
}

func (restorer *localWorkspaceRestorer) RollbackRestore(ctx context.Context) error {
	var rollbackErrors []error
	if restorer.agents != nil {
		if err := restorer.agents.Rollback(ctx); err != nil {
			rollbackErrors = append(rollbackErrors, err)
		}
	}
	for index := len(restorer.createdFiles) - 1; index >= 0; index-- {
		if err := os.Remove(restorer.createdFiles[index]); err != nil && !errors.Is(err, fs.ErrNotExist) {
			rollbackErrors = append(rollbackErrors, err)
		}
	}
	for index := len(restorer.createdSecrets) - 1; index >= 0; index-- {
		if err := restorer.secrets.Delete(ctx, restorer.createdSecrets[index]); err != nil && !errors.Is(err, fs.ErrNotExist) {
			rollbackErrors = append(rollbackErrors, err)
		}
	}
	restorer.createdFiles = nil
	restorer.createdSecrets = nil
	restorer.staged.wipe()
	wipeRuntimeSecret(restorer.passphrase)
	restorer.passphrase = nil
	return errors.Join(rollbackErrors...)
}

func (restorer *localWorkspaceRestorer) wipeTransient() {
	if restorer == nil {
		return
	}
	restorer.staged.wipe()
	wipeRuntimeSecret(restorer.passphrase)
	restorer.passphrase = nil
}

func (runtime *runtimeApplication) PlanWorkspaceBackup(ctx context.Context, request app.WorkspaceBackupRequest) (install.ChangeSet, error) {
	service, closeService, err := runtime.workspaceBackupService(ctx, false)
	if err != nil {
		return install.ChangeSet{}, err
	}
	defer closeService()
	return service.PlanWorkspaceBackup(ctx, request)
}

func (runtime *runtimeApplication) ApplyWorkspaceBackup(ctx context.Context, planID string, request app.WorkspaceBackupRequest) error {
	if err := ensureRestoreDirectory(filepath.Join(runtime.paths.Home, "tmp")); err != nil {
		return err
	}
	service, closeService, err := runtime.workspaceBackupService(ctx, true)
	if err != nil {
		return err
	}
	defer closeService()
	return service.ApplyWorkspaceBackup(ctx, planID, request)
}

func (runtime *runtimeApplication) InspectWorkspaceBackup(ctx context.Context, path string, passphrase []byte) (workspacebackup.Manifest, error) {
	service := app.Service{WorkspacePacker: workspacebackup.Packer{StagingParent: filepath.Join(runtime.paths.Home, "tmp")}}
	return service.InspectWorkspaceBackup(ctx, path, passphrase)
}

func (runtime *runtimeApplication) PlanWorkspaceRestore(ctx context.Context, request app.WorkspaceRestoreRequest) (install.ChangeSet, error) {
	service := runtime.workspaceRestoreService(request)
	defer service.WorkspaceRestorer.(*localWorkspaceRestorer).wipeTransient()
	return service.PlanWorkspaceRestore(ctx, request)
}

func (runtime *runtimeApplication) ApplyWorkspaceRestore(ctx context.Context, planID string, request app.WorkspaceRestoreRequest) error {
	service := runtime.workspaceRestoreService(request)
	defer service.WorkspaceRestorer.(*localWorkspaceRestorer).wipeTransient()
	return service.ApplyWorkspaceRestore(ctx, planID, request)
}

func (runtime *runtimeApplication) workspaceBackupService(ctx context.Context, writable bool) (*app.Service, func(), error) {
	configuration, err := (config.Store{Path: runtime.paths.Config}).Load()
	if err != nil || configuration.ControlPlane == nil {
		return nil, func() {}, errors.New("active control-plane configuration is required")
	}
	var store *journal.Store
	var ledger install.Ledger = previewLedger{}
	if writable {
		var installationLedger *journal.InstallationLedger
		store, installationLedger, err = runtime.openLedger(ctx)
		ledger = installationLedger
	} else {
		store, err = journal.OpenReadOnly(ctx, runtime.paths.Journal)
	}
	if err != nil {
		return nil, func() {}, err
	}
	localTarget := install.LocalTarget{}
	driver := &tencentdb.SnapshotDriver{
		Runner: localTarget, Stream: install.LocalStreamRunner{}, Target: localTarget, InstanceID: configuration.ControlPlane.InstanceID,
	}
	service := runtime.baseService
	service.Paths, service.UID, service.Target, service.Ledger = runtime.paths, runtime.uid, localTarget, ledger
	service.Secrets = runtime.secretStore()
	service.ControlPlaneStates, service.PrincipalAgentStates = store, store
	service.WorkspaceEvidence = store
	service.SnapshotDriver = driver
	service.WorkspaceResumer = runtimeWorkspaceResumer{
		target: localTarget, driver: driver, uid: runtime.uid,
		plist: filepath.Join(filepath.Dir(runtime.paths.Home), "Library", "LaunchAgents", "dev.mlink.broker.plist"),
	}
	service.WorkspacePacker = workspacebackup.Packer{StagingParent: filepath.Join(runtime.paths.Home, "tmp")}
	service.WorkspaceArchiver = app.LocalWorkspaceArchiver{}
	return &service, func() { _ = store.Close() }, nil
}

func (runtime *runtimeApplication) workspaceRestoreService(request app.WorkspaceRestoreRequest) *app.Service {
	localTarget := install.LocalTarget{}
	local := &localWorkspaceRestorer{
		paths: runtime.paths, secrets: runtime.secretStore(), passphrase: append([]byte(nil), request.Passphrase...),
		selectedAgents: append([]app.Agent(nil), request.SelectedAgents...),
		bundle:         workspacebackup.Packer{}, statePath: filepath.Join(runtime.paths.Run, "restore-operation.json"),
	}
	local.agents = &runtimeRestoreAgents{runtime: runtime}
	driver := &tencentdb.SnapshotDriver{
		Runner: localTarget, Stream: install.LocalStreamRunner{}, Target: localTarget, Environment: install.LocalEnvironmentRunner{},
		Metadata: restoreMetadataClient{restorer: local}, InstanceID: "default",
	}
	return &app.Service{
		Paths: runtime.paths, UID: runtime.uid, Target: localTarget, Ledger: &restoreMemoryLedger{}, Secrets: runtime.secretStore(),
		SnapshotDriver: driver, WorkspacePacker: workspacebackup.Packer{StagingParent: filepath.Join(runtime.paths.Home, "tmp")},
		WorkspaceFingerprinter: workspacebackup.Packer{}, WorkspaceStager: workspacebackup.Packer{}, WorkspaceRestorer: local,
		RestoreOperations: &deferredRestoreOperationStore{path: runtime.paths.Journal, statePath: filepath.Join(runtime.paths.Run, "restore-operation.json")},
	}
}
