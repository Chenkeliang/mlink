package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"debug/buildinfo"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	adapterclient "mlink/internal/adapter/client"
	"mlink/internal/adapter/codex"
	cursoradapter "mlink/internal/adapter/cursor"
	"mlink/internal/adapter/hermes"
	"mlink/internal/app"
	"mlink/internal/backend"
	"mlink/internal/broker"
	"mlink/internal/cli"
	"mlink/internal/config"
	"mlink/internal/controlplane"
	"mlink/internal/identity"
	"mlink/internal/install"
	"mlink/internal/journal"
	"mlink/internal/layout"
	"mlink/internal/mcpserver"
	"mlink/internal/model"
	"mlink/internal/panel"
	"mlink/internal/provider/host"
	"mlink/internal/provider/lifecycle"
	"mlink/internal/provider/tencentdb"
	"mlink/internal/secret"
	"mlink/internal/tui"
	"mlink/internal/version"
)

type runtimeApplication struct {
	paths       layout.Paths
	uid         int
	baseService app.Service
}

type previewLedger struct{}

type missingControlPlaneStore struct{}

func (missingControlPlaneStore) SaveControlPlane(context.Context, journal.ControlPlaneState) error {
	return errors.New("preview control-plane state is read-only")
}
func (missingControlPlaneStore) LoadControlPlane(context.Context) (journal.ControlPlaneState, error) {
	return journal.ControlPlaneState{}, fs.ErrNotExist
}
func (missingControlPlaneStore) MarkControlPlaneState(context.Context, string) error {
	return errors.New("preview control-plane state is read-only")
}

type maintenanceRestarter struct {
	target install.Target
	uid    int
}

func (restarter maintenanceRestarter) RestartBroker(ctx context.Context) error {
	_, err := restarter.target.Run(ctx, []string{"launchctl", "kickstart", "-k", fmt.Sprintf("gui/%d/dev.mlink.broker", restarter.uid)}, nil)
	return err
}

func (restarter maintenanceRestarter) RestartHermes(ctx context.Context) error {
	_, err := restarter.target.Run(ctx, []string{"hermes", "gateway", "restart"}, nil)
	return err
}

type httpHermesGrantVerifier struct {
	Client *http.Client
}

type localUpgradeLoader struct {
	RunVersion func(context.Context, string) ([]byte, error)
}

type strictMCPBackend struct{ client adapterclient.Client }

type runtimeIdentityStateStore interface {
	controlplane.PrincipalAgentStore
	LoadControlPlane(context.Context) (journal.ControlPlaneState, error)
}

func (backend strictMCPBackend) Recall(ctx context.Context, input broker.RecallInput) (model.ContextBundle, error) {
	return backend.client.RecallStrict(ctx, input)
}

func (backend strictMCPBackend) Status(ctx context.Context) error { return backend.client.Status(ctx) }

func (loader localUpgradeLoader) LoadUpgradeCandidate(ctx context.Context, path string, uid int) (app.UpgradeCandidate, error) {
	if !filepath.IsAbs(path) {
		return app.UpgradeCandidate{}, errors.New("candidate path must be absolute")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return app.UpgradeCandidate{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm()&0o100 == 0 || info.Mode().Perm()&0o022 != 0 || info.Size() <= 0 || info.Size() > 256<<20 {
		return app.UpgradeCandidate{}, errors.New("candidate must be a safe executable regular file")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != uid {
		return app.UpgradeCandidate{}, errors.New("candidate must be owned by the current user")
	}
	if _, err := buildinfo.ReadFile(path); err != nil {
		return app.UpgradeCandidate{}, errors.New("candidate is not a Go executable")
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return app.UpgradeCandidate{}, err
	}
	runVersion := loader.RunVersion
	if runVersion == nil {
		runVersion = func(ctx context.Context, binary string) ([]byte, error) {
			command := exec.CommandContext(ctx, binary, "version", "--json")
			return command.Output()
		}
	}
	versionOutput, err := runVersion(ctx, path)
	if err != nil || len(versionOutput) == 0 || len(versionOutput) > 64<<10 {
		return app.UpgradeCandidate{}, errors.New("candidate version self-test failed")
	}
	var candidateInfo version.Info
	if err := json.Unmarshal(versionOutput, &candidateInfo); err != nil {
		return app.UpgradeCandidate{}, errors.New("candidate version metadata is invalid")
	}
	if candidateInfo.GOOS != runtime.GOOS || candidateInfo.GOARCH != runtime.GOARCH || candidateInfo.SchemaMin <= 0 || candidateInfo.SchemaMax < candidateInfo.SchemaMin {
		return app.UpgradeCandidate{}, errors.New("candidate platform or schema metadata is incompatible")
	}
	return app.UpgradeCandidate{Path: path, Content: content, Mode: info.Mode().Perm(), Info: candidateInfo}, nil
}

type localInstalledVerifier struct {
	Loader localUpgradeLoader
}

func (verifier localInstalledVerifier) VerifyInstalledBinary(ctx context.Context, path, expectedHash string) error {
	content, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(content)
	if hex.EncodeToString(digest[:]) != expectedHash {
		return errors.New("installed binary hash differs from preview")
	}
	_, err = verifier.Loader.LoadUpgradeCandidate(ctx, path, os.Getuid())
	return err
}

func (verifier httpHermesGrantVerifier) VerifyHermesGrant(ctx context.Context, endpoint string, newGrant, oldGrant []byte) error {
	client := verifier.Client
	if client == nil {
		client = &http.Client{Timeout: 2 * time.Second}
	}
	check := func(grant []byte) (int, error) {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(endpoint, "/")+"/v1/health?adapter_id=hermes", nil)
		if err != nil {
			return 0, err
		}
		request.Header.Set("Authorization", "Bearer "+string(grant))
		response, err := client.Do(request)
		if err != nil {
			return 0, err
		}
		defer response.Body.Close()
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return response.StatusCode, nil
	}
	newStatus, err := check(newGrant)
	if err != nil {
		return err
	}
	oldStatus, err := check(oldGrant)
	if err != nil {
		return err
	}
	if newStatus != http.StatusOK || oldStatus != http.StatusUnauthorized {
		return fmt.Errorf("Hermes grant verification status new=%d old=%d", newStatus, oldStatus)
	}
	return nil
}

func (previewLedger) SaveBackup(context.Context, install.Backup) error {
	return errors.New("preview ledger cannot save backups")
}
func (previewLedger) LoadBackup(context.Context, string, string) (install.Backup, error) {
	return install.Backup{}, fs.ErrNotExist
}
func (previewLedger) RecordOwned(context.Context, install.OwnedResource) error {
	return errors.New("preview ledger cannot record ownership")
}

func defaultDependencies(stdin io.Reader, stdout, stderr io.Writer) (cli.Dependencies, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return cli.Dependencies{}, err
	}
	executable, err := os.Executable()
	if err != nil {
		return cli.Dependencies{}, err
	}
	paths, err := layout.FromHome(home, executable)
	if err != nil {
		return cli.Dependencies{}, err
	}
	identityKey := make([]byte, 32)
	if _, err := rand.Read(identityKey); err != nil {
		return cli.Dependencies{}, err
	}
	runtime := &runtimeApplication{
		paths: paths,
		uid:   os.Getuid(),
	}
	runtime.baseService = app.Service{
		Paths:               paths,
		UID:                 runtime.uid,
		Secrets:             secret.Keychain{},
		HermesEndpoint:      environmentDefault("MLINK_HERMES_BROKER_ENDPOINT", ""),
		HermesListenAddress: environmentDefault("MLINK_HERMES_BROKER_LISTEN", ""),
		IdentityKey:         identityKey,
	}
	dependencies := cli.Dependencies{
		Stdin:          stdin,
		Stdout:         stdout,
		Stderr:         stderr,
		App:            runtime,
		InstallRequest: defaultInstallRequest(),
	}
	dependencies.RunCodexHook = func(ctx context.Context, event string) error {
		return codex.Handle(ctx, event, stdin, stdout, adapterclient.Client{
			SocketPath: paths.Socket, AdapterID: "codex", RecallTimeout: 800 * time.Millisecond, CaptureTimeout: 2 * time.Second,
		})
	}
	dependencies.RunCursorHook = func(ctx context.Context, event string) error {
		return cursoradapter.Handle(ctx, event, stdin, stdout, adapterclient.Client{
			SocketPath: paths.Socket, AdapterID: "cursor", RecallTimeout: 800 * time.Millisecond, CaptureTimeout: 2 * time.Second,
		})
	}
	dependencies.ServeBroker = runtime.ServeBroker
	dependencies.ServeMCP = func(ctx context.Context) error {
		backend := strictMCPBackend{client: adapterclient.Client{SocketPath: paths.Socket, AdapterID: "cursor", RecallTimeout: 2 * time.Second, CaptureTimeout: 2 * time.Second}}
		return mcpserver.New(backend, version.Current().Version).Run(ctx, &mcp.StdioTransport{})
	}
	dependencies.RunTUI = func(context.Context) error {
		_, err := tea.NewProgram(tui.New(runtime, dependencies.InstallRequest), tea.WithAltScreen(), tea.WithInput(stdin), tea.WithOutput(stdout)).Run()
		return err
	}
	return dependencies, nil
}

func (runtime *runtimeApplication) ServeBroker(ctx context.Context) error {
	configuration, err := (config.Store{Path: runtime.paths.Config}).Load()
	if err != nil {
		return err
	}
	connectionConfig, exists := configuration.Connections[configuration.ActiveConnectionID]
	if !exists {
		return errors.New("active MLink connection is missing")
	}
	journalStore, err := journal.Open(ctx, runtime.paths.Journal)
	if err != nil {
		return err
	}
	defer journalStore.Close()
	keychain := runtime.secretStore()
	router, grants, hermesEnabled, err := runtimeRouter(ctx, configuration, keychain)
	if err != nil {
		return err
	}
	defer wipeRuntimeSecret(router.Key)
	defer router.Bindings.Wipe()
	authorizer, err := runtimeAuthorizer(ctx, configuration, keychain, journalStore, router, grants, hermesEnabled)
	if err != nil {
		return err
	}
	providerRuntime := backend.NewRuntime(backend.RuntimeConfig{
		Secrets:          keychain,
		Manifest:         tencentdb.BundledManifest(),
		PackageDirectory: runtime.paths.Home,
		RuntimeDirectory: filepath.Join(runtime.paths.Home, "providers", "runtime"),
	})
	if _, err := providerRuntime.Start(ctx, connectionConfig); err != nil {
		return err
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = providerRuntime.Shutdown(shutdownCtx)
	}()
	active, err := journalStore.ActiveObservations(ctx)
	if err != nil {
		return err
	}
	for _, observation := range active {
		if _, err := providerRuntime.ObserveUserTurn(ctx, observation.Route, host.CallMeta{IdempotencyKey: observation.Turn.Identity.TurnID}, observation.Turn); err != nil {
			return fmt.Errorf("restore active memory turn: %w", err)
		}
	}
	server := broker.Server{
		Service:    broker.Service{Journal: journalStore, Provider: providerRuntime},
		Authorizer: authorizer,
	}
	serveCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	errorsChannel := make(chan error, 2)
	go func() { errorsChannel <- server.ServeUnix(serveCtx, runtime.paths.Socket) }()
	servers := 1
	if hermesEnabled {
		listener, err := privateListener(configuration.Broker.ListenAddress)
		if err != nil {
			return err
		}
		servers++
		go func() { errorsChannel <- server.ServeHTTP(serveCtx, listener) }()
	}
	go runDeliveryWorker(serveCtx, broker.Worker{Journal: journalStore, Provider: providerRuntime})
	select {
	case <-ctx.Done():
		return nil
	case err := <-errorsChannel:
		cancel()
		if err != nil {
			return err
		}
		if servers > 1 {
			if second := <-errorsChannel; second != nil {
				return second
			}
		}
		return nil
	}
}

func runtimeRouter(ctx context.Context, configuration config.Config, secrets secret.Store) (*identity.Router, []broker.Grant, bool, error) {
	if err := config.Validate(configuration); err != nil {
		return nil, nil, false, err
	}
	identityKey, err := secrets.Get(ctx, "identity/hmac-key")
	if err != nil {
		return nil, nil, false, err
	}
	if len(identityKey) != 32 {
		wipeRuntimeSecret(identityKey)
		return nil, nil, false, errors.New("MLink identity key is invalid")
	}
	bindings, err := (identity.Repository{Secrets: secrets, IdentityKey: identityKey}).Load(ctx, configuration.Bindings)
	if err != nil {
		wipeRuntimeSecret(identityKey)
		return nil, nil, false, err
	}
	hermesRouting := config.HermesRouting{}
	if adapter := configuration.Adapters["hermes"]; adapter.HermesRouting != nil {
		hermesRouting = *adapter.HermesRouting
	}
	router := &identity.Router{
		NamespaceID: configuration.NamespaceID, Key: identityKey, Connections: configuration.Connections,
		Spaces: configuration.Spaces, Principals: configuration.Principals, Adapters: configuration.Adapters,
		Bindings: bindings, Hermes: hermesRouting,
	}
	var grants []broker.Grant
	for _, adapterID := range []string{"codex", "pi", "cursor"} {
		adapter, enabled := configuration.Adapters[adapterID]
		if !enabled || !adapter.Enabled {
			continue
		}
		spaceID := adapter.SpaceID
		if configuration.SchemaVersion == 3 {
			spaceID = "owner"
		}
		grants = append(grants, broker.Grant{AdapterID: adapterID, Mode: broker.IdentityFixed, FixedSpaceID: spaceID, AllowedSpaceIDs: map[string]bool{spaceID: true}})
	}
	hermesAdapter, hermesEnabled := configuration.Adapters["hermes"]
	if !hermesEnabled || !hermesAdapter.Enabled {
		return router, grants, false, nil
	}
	token, err := secrets.Get(ctx, "adapter/hermes/token")
	if err != nil {
		bindings.Wipe()
		wipeRuntimeSecret(identityKey)
		return nil, nil, false, err
	}
	defer wipeRuntimeSecret(token)
	digest := sha256.Sum256(token)
	allowedSpaces := map[string]bool{
		hermesRouting.OwnerSpaceID: true, hermesRouting.PrivateSpaceID: true, hermesRouting.GroupSpaceID: true,
	}
	if configuration.SchemaVersion == 3 {
		allowedSpaces = map[string]bool{"owner": true, "hermes-private": true, "hermes-groups": true}
	}
	grants = append(grants, broker.Grant{
		TokenDigest: digest[:], AdapterID: "hermes", Mode: broker.IdentityDelegated, Source: "feishu",
		AllowedSpaceIDs: allowedSpaces,
	})
	return router, grants, true, nil
}

func runtimeAuthorizer(ctx context.Context, configuration config.Config, secrets secret.Store, states runtimeIdentityStateStore, router *identity.Router, grants []broker.Grant, hermesEnabled bool) (broker.Authorizer, error) {
	authorizer := broker.Authorizer{Router: router, Grants: grants}
	if configuration.SchemaVersion != 3 {
		return authorizer, nil
	}
	if configuration.ControlPlane == nil {
		return broker.Authorizer{}, errors.New("MLink control plane is missing")
	}
	state, err := states.LoadControlPlane(ctx)
	if err != nil {
		return broker.Authorizer{}, errors.New("load provisioned Core identity")
	}
	if err := validateRuntimeCoreIdentity(configuration, state); err != nil {
		return broker.Authorizer{}, errors.New("configured IDs do not match the provisioned Core identity")
	}
	authorizer.ConnectionID = configuration.ActiveConnectionID
	authorizer.ControlPlane = configuration.ControlPlane
	if !hermesEnabled {
		return authorizer, nil
	}
	connectionConfig := configuration.Connections[configuration.ActiveConnectionID]
	baseURL, baseOK := connectionConfig.ProviderConfig["base_url"].(string)
	serviceID, serviceOK := connectionConfig.ProviderConfig["service_id"].(string)
	timeoutMS, timeoutOK := connectionConfig.ProviderConfig["timeout_ms"].(int)
	if !baseOK || !serviceOK || !timeoutOK || timeoutMS < 100 || timeoutMS > 30_000 {
		return broker.Authorizer{}, errors.New("TencentDB control-plane connection configuration is invalid")
	}
	const keychainPrefix = "keychain://dev.mlink/"
	tokenRef := connectionConfig.SecretRefs["token"]
	if !strings.HasPrefix(tokenRef, keychainPrefix) || len(tokenRef) == len(keychainPrefix) {
		return broker.Authorizer{}, errors.New("TencentDB control-plane token reference is invalid")
	}
	token, err := secrets.Get(ctx, strings.TrimPrefix(tokenRef, keychainPrefix))
	if err != nil {
		return broker.Authorizer{}, errors.New("load TencentDB control-plane token")
	}
	defer wipeRuntimeSecret(token)
	timeout := time.Duration(timeoutMS) * time.Millisecond
	client, err := tencentdb.NewClient(tencentdb.Config{
		BaseURL: baseURL, Token: string(token), ServiceID: serviceID,
		HTTPClient: &http.Client{Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}},
	})
	if err != nil {
		return broker.Authorizer{}, err
	}
	authorizer.DynamicAgents = &controlplane.AgentProvisioner{
		Metadata: tencentdb.NewMetadataClient(client), Secrets: secrets, Store: states,
		MaxDynamicAgents: configuration.ControlPlane.DynamicAgentLimit, Timeout: timeout,
	}
	return authorizer, nil
}

func privateListener(address string) (net.Listener, error) {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return nil, errors.New("Hermes Broker listen address is invalid")
	}
	ip := net.ParseIP(host)
	if host == "" || ip != nil && ip.IsUnspecified() {
		return nil, errors.New("Hermes Broker refuses an unspecified listen address")
	}
	return net.Listen("tcp", address)
}

func runDeliveryWorker(ctx context.Context, worker broker.Worker) {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = worker.DrainOne(ctx)
		}
	}
}

func wipeRuntimeSecret(value []byte) {
	for index := range value {
		value[index] = 0
	}
}

func defaultInstallRequest() app.InstallRequest {
	username := "local-user"
	if current, err := user.Current(); err == nil && strings.TrimSpace(current.Username) != "" {
		username = sanitizeStableID(current.Username)
	}
	teamID := environmentDefault("MLINK_MEMORYCORE_TEAM_ID", "personal")
	return app.InstallRequest{
		OwnerSlug: username,
		Connection: config.Connection{
			ID:              environmentDefault("MLINK_CONNECTION_ID", "local"),
			ProviderID:      "dev.mlink.tencentdb",
			ProviderVersion: "0.1.0",
			ConfigRevision:  environmentDefault("MLINK_CONFIG_REVISION", "rev-local-1"),
			ProviderConfig: map[string]any{
				"base_url":   environmentDefault("MLINK_MEMORYCORE_BASE_URL", "http://127.0.0.1:8420"),
				"service_id": environmentDefault("MLINK_MEMORYCORE_SERVICE_ID", "default"),
				"timeout_ms": 5000,
			},
			TenantID:           teamID,
			AgentID:            environmentDefault("MLINK_MEMORY_AGENT_ID", "default"),
			UserID:             environmentDefault("MLINK_MEMORY_USER_ID", username),
			IncludeAgentShared: false,
		},
		HermesMachine:     environmentDefault("MLINK_HERMES_MACHINE", "hermes-agent-env"),
		DynamicAgentLimit: 500,
	}
}

func (runtime *runtimeApplication) ProviderStatus(ctx context.Context) (lifecycle.BackendStatus, error) {
	registry, err := runtime.providerRegistry(previewLedger{})
	if err != nil {
		return lifecycle.BackendStatus{}, err
	}
	backend, err := registry.Get("dev.mlink.tencentdb")
	if err != nil {
		return lifecycle.BackendStatus{}, err
	}
	return backend.Detect(ctx, runtime.providerConnection())
}

func (runtime *runtimeApplication) PlanProviderInstall(ctx context.Context, request lifecycle.BackendInstallRequest) (install.ChangeSet, error) {
	registry, err := runtime.providerRegistry(previewLedger{})
	if err != nil {
		return install.ChangeSet{}, err
	}
	backend, err := registry.Get(request.ProviderID)
	if err != nil {
		return install.ChangeSet{}, err
	}
	return backend.PlanInstall(ctx, request)
}

func (runtime *runtimeApplication) ApplyProviderInstall(ctx context.Context, planID string, request lifecycle.BackendInstallRequest) error {
	store, ledger, err := runtime.openLedger(ctx)
	if err != nil {
		return err
	}
	defer store.Close()
	registry, err := runtime.providerRegistry(ledger)
	if err != nil {
		return err
	}
	backend, err := registry.Get(request.ProviderID)
	if err != nil {
		return err
	}
	return backend.ApplyInstall(ctx, planID, request)
}

func (runtime *runtimeApplication) providerRegistry(ledger install.Ledger) (*lifecycle.Registry, error) {
	target := install.LocalTarget{}
	deployment := tencentdb.Deployment{
		Runner: target, Target: target, Ledger: ledger, Secrets: secret.Keychain{},
		ConfigPath: filepath.Join(runtime.paths.Home, "memorycore", "tdai-gateway.yaml"),
		EnvPath:    filepath.Join(runtime.paths.Run, "memorycore.env"),
	}
	return lifecycle.NewRegistry(lifecycle.Registration{ProviderID: "dev.mlink.tencentdb", Lifecycle: deployment})
}

func (runtime *runtimeApplication) providerConnection() config.Connection {
	if configuration, err := (config.Store{Path: runtime.paths.Config}).Load(); err == nil {
		if connection, exists := configuration.Connections[configuration.ActiveConnectionID]; exists {
			return connection
		}
	}
	return defaultInstallRequest().Connection
}

func (runtime *runtimeApplication) PlanControlPlaneProvision(ctx context.Context, limit int) (install.ChangeSet, error) {
	request := defaultInstallRequest()
	return runtime.PlanControlPlaneBootstrap(ctx, app.ControlPlaneBootstrapRequest{
		Connection: request.Connection, OwnerSlug: request.OwnerSlug, DynamicAgentLimit: limit,
	})
}

func (runtime *runtimeApplication) PlanControlPlaneBootstrap(ctx context.Context, request app.ControlPlaneBootstrapRequest) (install.ChangeSet, error) {
	service, closeService, err := runtime.headlessControlService(ctx, false, request)
	if err != nil {
		return install.ChangeSet{}, err
	}
	defer closeService()
	return service.PlanControlPlaneProvision(ctx)
}

func (runtime *runtimeApplication) ApplyControlPlaneProvision(ctx context.Context, planID string, limit int) error {
	request := defaultInstallRequest()
	account := "connection/" + request.Connection.ID + "/token"
	token, err := runtime.secretStore().Get(ctx, account)
	if err != nil {
		return err
	}
	defer wipeRuntimeSecret(token)
	return runtime.ApplyControlPlaneBootstrap(ctx, planID, app.ControlPlaneBootstrapRequest{
		Connection: request.Connection, OwnerSlug: request.OwnerSlug, GatewayToken: token, DynamicAgentLimit: limit,
	})
}

func (runtime *runtimeApplication) ApplyControlPlaneBootstrap(ctx context.Context, planID string, request app.ControlPlaneBootstrapRequest) error {
	if len(request.GatewayToken) == 0 || len(request.GatewayToken) > 16<<10 || bytes.ContainsAny(request.GatewayToken, "\r\n\x00") {
		return errors.New("protected MemoryCore gateway token is required")
	}
	preview, err := runtime.PlanControlPlaneBootstrap(ctx, request)
	if err != nil {
		return err
	}
	if preview.PlanID != planID {
		return fmt.Errorf("%w: control-plane bootstrap changed after preview", install.ErrPlanStale)
	}
	keychain := runtime.secretStore()
	account := "connection/" + request.Connection.ID + "/token"
	previous, previousErr := keychain.Get(ctx, account)
	if previousErr != nil && !errors.Is(previousErr, fs.ErrNotExist) {
		return previousErr
	}
	defer wipeRuntimeSecret(previous)
	if err := keychain.Put(ctx, account, request.GatewayToken); err != nil {
		return err
	}
	restore := func() error {
		if previousErr == nil {
			return keychain.Put(context.Background(), account, previous)
		}
		return keychain.Delete(context.Background(), account)
	}
	service, closeService, err := runtime.headlessControlService(ctx, true, request)
	if err != nil {
		return errors.Join(err, restore())
	}
	defer closeService()
	if err := service.ApplyControlPlaneProvision(ctx, planID); err != nil {
		return errors.Join(err, restore())
	}
	return nil
}

func (runtime *runtimeApplication) headlessControlService(ctx context.Context, writable bool, bootstrap app.ControlPlaneBootstrapRequest) (*app.Service, func(), error) {
	var provisionStates controlplane.StateStore
	var controlStates app.ControlPlaneStateStore
	closeService := func() {}
	if writable {
		store, _, err := runtime.openLedger(ctx)
		if err != nil {
			return nil, closeService, err
		}
		provisionStates, controlStates = store, store
		closeService = func() { _ = store.Close() }
	} else {
		store, err := journal.OpenReadOnly(ctx, runtime.paths.Journal)
		if errors.Is(err, fs.ErrNotExist) {
			missing := missingControlPlaneStore{}
			provisionStates, controlStates = missing, missing
		} else if err != nil {
			return nil, closeService, err
		} else {
			provisionStates, controlStates = store, store
			closeService = func() { _ = store.Close() }
		}
	}
	connection := bootstrap.Connection
	baseURL, baseOK := connection.ProviderConfig["base_url"].(string)
	instanceID, instanceOK := connection.ProviderConfig["service_id"].(string)
	if connection.ID == "" || connection.ProviderID != "dev.mlink.tencentdb" || !baseOK || strings.TrimSpace(baseURL) == "" || !instanceOK || strings.TrimSpace(instanceID) == "" || bootstrap.DynamicAgentLimit <= 0 || bootstrap.DynamicAgentLimit > 10_000 {
		closeService()
		return nil, func() {}, errors.New("complete MemoryCore control-plane bootstrap request is required")
	}
	if _, err := tencentdb.ValidateDeploymentEndpoint(baseURL); err != nil {
		closeService()
		return nil, func() {}, err
	}
	connection.SecretRefs = map[string]string{"token": "keychain://dev.mlink/connection/" + connection.ID + "/token"}
	configuration := config.Config{ActiveConnectionID: connection.ID, Connections: map[string]config.Connection{connection.ID: connection}}
	var metadata tencentdb.MetadataClient
	if writable {
		client, clientErr := runtimeTencentClient(ctx, configuration, runtime.secretStore())
		if clientErr != nil {
			closeService()
			return nil, func() {}, clientErr
		}
		metadata = tencentdb.NewMetadataClient(client)
	}
	controlService := &controlplane.Service{Metadata: metadata, Secrets: runtime.secretStore(), States: provisionStates}
	installationID := strings.TrimSpace(connection.TenantID)
	if installationID == "" {
		installationID = "personal"
	}
	return &app.Service{
		ControlProvisioner: controlService, ControlPlaneStates: controlStates, Secrets: runtime.secretStore(),
		ControlRequest: controlplane.ProvisionRequest{
			InstallationID: installationID, InstanceID: instanceID, ConnectionID: connection.ID, ProviderEndpoint: baseURL, AdminUsername: "mlink-admin",
			OwnerUsername: sanitizeStableID(bootstrap.OwnerSlug), TeamName: "MLink", OwnerAgentName: "MLink Owner", DynamicAgentLimit: bootstrap.DynamicAgentLimit,
		},
	}, closeService, nil
}

func (runtime *runtimeApplication) secretStore() secret.Store {
	if runtime.baseService.Secrets != nil {
		return runtime.baseService.Secrets
	}
	return secret.Keychain{}
}

func (runtime *runtimeApplication) PlanInstall(ctx context.Context, request app.InstallRequest) (install.ChangeSet, error) {
	service, request, err := runtime.prepare(ctx, request, previewLedger{})
	if err != nil {
		return install.ChangeSet{}, err
	}
	states, err := journal.OpenReadOnly(ctx, runtime.paths.Journal)
	if errors.Is(err, fs.ErrNotExist) {
		return install.ChangeSet{}, errors.New("fresh install requires provisioned Core identity")
	}
	if err != nil {
		return install.ChangeSet{}, err
	}
	defer states.Close()
	service.ControlPlaneStates = states
	return service.PlanInstall(ctx, request)
}

func (runtime *runtimeApplication) ApplyInstall(ctx context.Context, planID string, request app.InstallRequest) error {
	previewService, preparedRequest, err := runtime.prepare(ctx, request, previewLedger{})
	if err != nil {
		return err
	}
	previewStates, err := journal.OpenReadOnly(ctx, runtime.paths.Journal)
	if errors.Is(err, fs.ErrNotExist) {
		return errors.New("fresh install requires provisioned Core identity")
	}
	if err != nil {
		return err
	}
	previewService.ControlPlaneStates = previewStates
	preview, err := previewService.PlanInstall(ctx, preparedRequest)
	_ = previewStates.Close()
	if err != nil {
		return err
	}
	if preview.PlanID != planID {
		return fmt.Errorf("%w: supplied plan %q no longer matches %q", install.ErrPlanStale, planID, preview.PlanID)
	}
	store, ledger, err := runtime.openLedger(ctx)
	if err != nil {
		return err
	}
	defer store.Close()
	service, request, err := runtime.prepare(ctx, request, ledger)
	if err != nil {
		return err
	}
	service.BlockingEvents = store
	service.ControlPlaneStates = store
	return service.ApplyInstall(ctx, planID, request)
}

func (runtime *runtimeApplication) PlanRestore(ctx context.Context, request app.RestoreRequest) (install.ChangeSet, error) {
	store, ledger, err := runtime.openLedger(ctx)
	if err != nil {
		return install.ChangeSet{}, err
	}
	defer store.Close()
	installRequest := defaultInstallRequest()
	installRequest.Agents = []app.Agent{app.Codex, app.Pi, app.Hermes, app.Cursor}
	service, _, err := runtime.prepare(ctx, installRequest, ledger)
	if err != nil {
		return install.ChangeSet{}, err
	}
	return service.PlanRestore(ctx, request)
}

func (runtime *runtimeApplication) ApplyRestore(ctx context.Context, planID string, request app.RestoreRequest) error {
	store, ledger, err := runtime.openLedger(ctx)
	if err != nil {
		return err
	}
	defer store.Close()
	installRequest := defaultInstallRequest()
	installRequest.Agents = []app.Agent{app.Codex, app.Pi, app.Hermes, app.Cursor}
	service, _, err := runtime.prepare(ctx, installRequest, ledger)
	if err != nil {
		return err
	}
	return service.ApplyRestore(ctx, planID, request)
}

func (runtime *runtimeApplication) PlanUninstall(ctx context.Context, request app.UninstallRequest) (install.ChangeSet, error) {
	store, ledger, err := runtime.openLedger(ctx)
	if err != nil {
		return install.ChangeSet{}, err
	}
	defer store.Close()
	installRequest := defaultInstallRequest()
	installRequest.Agents = request.Agents
	service, _, err := runtime.prepare(ctx, installRequest, ledger)
	if err != nil {
		return install.ChangeSet{}, err
	}
	service.BlockingEvents = store
	service.ControlPlaneStates = store
	service.PrincipalAgentStates = store
	return service.PlanUninstall(ctx, request)
}

func (runtime *runtimeApplication) ApplyUninstall(ctx context.Context, planID string, request app.UninstallRequest) error {
	store, ledger, err := runtime.openLedger(ctx)
	if err != nil {
		return err
	}
	defer store.Close()
	installRequest := defaultInstallRequest()
	installRequest.Agents = request.Agents
	service, _, err := runtime.prepare(ctx, installRequest, ledger)
	if err != nil {
		return err
	}
	service.BlockingEvents = store
	service.ControlPlaneStates = store
	service.PrincipalAgentStates = store
	return service.ApplyUninstall(ctx, planID, request)
}

func (runtime *runtimeApplication) PlanPanelProvision(ctx context.Context) (install.ChangeSet, error) {
	service, closeService, err := runtime.controlPanelService(ctx, false)
	if err != nil {
		return install.ChangeSet{}, err
	}
	defer closeService()
	return service.PlanPanelProvision(ctx)
}

func (runtime *runtimeApplication) ApplyPanelProvision(ctx context.Context, planID string) error {
	service, closeService, err := runtime.controlPanelService(ctx, true)
	if err != nil {
		return err
	}
	defer closeService()
	return service.ApplyPanelProvision(ctx, planID)
}

func (runtime *runtimeApplication) PlanPanelRuntime(ctx context.Context) (install.ChangeSet, error) {
	service, closeService, err := runtime.controlPanelService(ctx, false)
	if err != nil {
		return install.ChangeSet{}, err
	}
	defer closeService()
	return service.PlanPanelRuntime(ctx)
}

func (runtime *runtimeApplication) ApplyPanelRuntime(ctx context.Context, planID string) error {
	service, closeService, err := runtime.controlPanelService(ctx, true)
	if err != nil {
		return err
	}
	defer closeService()
	return service.ApplyPanelRuntime(ctx, planID)
}

func (runtime *runtimeApplication) PlanPanelCutover(ctx context.Context, request app.ControlPlaneCutoverRequest) (install.ChangeSet, error) {
	service, closeService, err := runtime.controlPanelService(ctx, false)
	if err != nil {
		return install.ChangeSet{}, err
	}
	defer closeService()
	service.Target, err = runtime.controlPlaneCutoverTarget(ctx)
	if err != nil {
		return install.ChangeSet{}, err
	}
	return service.PlanControlPlaneCutover(ctx, request)
}

func (runtime *runtimeApplication) ApplyPanelCutover(ctx context.Context, planID string, request app.ControlPlaneCutoverRequest) error {
	service, closeService, err := runtime.controlPanelService(ctx, true)
	if err != nil {
		return err
	}
	defer closeService()
	service.Target, err = runtime.controlPlaneCutoverTarget(ctx)
	if err != nil {
		return err
	}
	return service.ApplyControlPlaneCutover(ctx, planID, request)
}

func (runtime *runtimeApplication) controlPlaneCutoverTarget(ctx context.Context) (install.Target, error) {
	configuration, err := (config.Store{Path: runtime.paths.Config}).Load()
	if err != nil {
		return nil, err
	}
	hermesAdapter, enabled := configuration.Adapters[string(app.Hermes)]
	if !enabled || !hermesAdapter.Enabled {
		return install.LocalTarget{}, nil
	}
	machine := environmentDefault("MLINK_HERMES_MACHINE", "hermes-agent-env")
	detection, err := hermes.Detect(ctx, install.LocalTarget{}, machine)
	if err != nil {
		return nil, err
	}
	orbTarget, err := hermes.NewOrbTarget(machine, detection.HermesHome, nil)
	if err != nil {
		return nil, err
	}
	return app.NewRoutingTarget(install.LocalTarget{}, orbTarget, detection.HermesHome)
}

func (runtime *runtimeApplication) PanelControlStatus(ctx context.Context) (app.PanelControlStatus, error) {
	service, closeService, err := runtime.controlPanelService(ctx, false)
	if err != nil {
		return app.PanelControlStatus{}, err
	}
	defer closeService()
	return service.PanelControlStatus(ctx)
}

func (runtime *runtimeApplication) OpenPanel(ctx context.Context) error {
	_, err := (install.LocalTarget{}).Run(ctx, []string{"open", "http://127.0.0.1:8125"}, nil)
	return err
}

func (runtime *runtimeApplication) CopyPanelOwnerKey(ctx context.Context) error {
	return runtime.CopyCredential(ctx, app.CredentialPanelOwner)
}

func (runtime *runtimeApplication) CredentialStatuses(ctx context.Context) ([]app.CredentialStatus, error) {
	service := runtime.baseService
	service.Target = install.LocalTarget{}
	return service.CredentialStatuses(ctx)
}

func (runtime *runtimeApplication) CopyCredential(ctx context.Context, role app.CredentialRole) error {
	service := runtime.baseService
	reader, writer := io.Pipe()
	copyResult := make(chan error, 1)
	go func() {
		err := service.CopyCredential(ctx, role, writer)
		_ = writer.CloseWithError(err)
		copyResult <- err
	}()
	_, runErr := (install.LocalTarget{}).Run(ctx, []string{"pbcopy"}, reader)
	copyErr := <-copyResult
	return errors.Join(copyErr, runErr)
}

func (runtime *runtimeApplication) controlPanelService(ctx context.Context, writable bool) (*app.Service, func(), error) {
	configuration, err := (config.Store{Path: runtime.paths.Config}).Load()
	if err != nil {
		return nil, func() {}, err
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
	closeService := func() { _ = store.Close() }
	keychain := secret.Keychain{}
	var metadata tencentdb.MetadataClient
	if writable {
		client, clientErr := runtimeTencentClient(ctx, configuration, keychain)
		if clientErr != nil {
			closeService()
			return nil, func() {}, clientErr
		}
		metadata = tencentdb.NewMetadataClient(client)
	}
	ownerName := "owner"
	if owner, ok := configuration.Principals["owner"]; ok {
		ownerName = strings.TrimPrefix(owner.CanonicalUserID, "usr_owner_")
	}
	localTarget := install.LocalTarget{}
	desired := panel.Desired{
		RegistryPath: runtime.paths.PanelRegistry, HostAddress: "127.0.0.1", PanelHostPort: 8125, KnowledgeHostPort: 8424,
		InstanceID: "default", InstanceName: "MLink Local", GatewayEndpoint: "http://host.docker.internal:8420",
		KnowledgePublicBaseURL: "http://host.docker.internal:8424/v3", KnowledgeLLMProxyBaseURL: "http://host.docker.internal:8420",
	}
	if configuration.ControlPlane != nil {
		desired.InstanceID = configuration.ControlPlane.InstanceID
	} else if connection := configuration.Connections[configuration.ActiveConnectionID]; connection.ProviderConfig != nil {
		if instanceID, ok := connection.ProviderConfig["service_id"].(string); ok && instanceID != "" {
			desired.InstanceID = instanceID
		}
	}
	dynamicAgentLimit := 500
	if configuration.ControlPlane != nil && configuration.ControlPlane.DynamicAgentLimit > 0 {
		dynamicAgentLimit = configuration.ControlPlane.DynamicAgentLimit
	}
	provisionRequest := controlplane.ProvisionRequest{
		InstallationID: configuration.NamespaceID, InstanceID: desired.InstanceID,
		AdminUsername: "mlink-admin", OwnerUsername: sanitizeStableID(ownerName), TeamName: "MLink", OwnerAgentName: "MLink Owner",
		DynamicAgentLimit: dynamicAgentLimit,
	}
	controlService := &controlplane.Service{Metadata: metadata, Secrets: keychain, States: store}
	panelRuntime := &panel.Runtime{Runner: localTarget, Target: localTarget}
	return &app.Service{
		Paths: runtime.paths, UID: runtime.uid, Target: localTarget, Ledger: ledger, Secrets: keychain,
		ControlPlaneStates: store, ControlProvisioner: controlService, ControlRequest: provisionRequest,
		PanelRuntime: panelRuntime, PanelDesired: desired, PanelConnectionID: configuration.ActiveConnectionID,
		PrincipalAgentStates: store,
	}, closeService, nil
}

func runtimeTencentClient(ctx context.Context, configuration config.Config, secrets secret.Store) (*tencentdb.Client, error) {
	connectionConfig := configuration.Connections[configuration.ActiveConnectionID]
	baseURL, baseOK := connectionConfig.ProviderConfig["base_url"].(string)
	serviceID, serviceOK := connectionConfig.ProviderConfig["service_id"].(string)
	if !baseOK || !serviceOK || strings.TrimSpace(baseURL) == "" || strings.TrimSpace(serviceID) == "" {
		return nil, errors.New("TencentDB connection configuration is invalid")
	}
	const prefix = "keychain://dev.mlink/"
	ref := connectionConfig.SecretRefs["token"]
	if !strings.HasPrefix(ref, prefix) {
		return nil, errors.New("TencentDB token reference is invalid")
	}
	token, err := secrets.Get(ctx, strings.TrimPrefix(ref, prefix))
	if err != nil {
		return nil, errors.New("load TencentDB token")
	}
	defer wipeRuntimeSecret(token)
	return tencentdb.NewClient(tencentdb.Config{BaseURL: baseURL, Token: string(token), ServiceID: serviceID})
}

func (runtime *runtimeApplication) PlanIdentityBind(ctx context.Context, request app.IdentityBindRequest) (install.ChangeSet, error) {
	return runtime.identityService(previewLedger{}).PlanIdentityBind(ctx, request)
}

func (runtime *runtimeApplication) ApplyIdentityBind(ctx context.Context, planID string, request app.IdentityBindRequest) error {
	store, ledger, err := runtime.openLedger(ctx)
	if err != nil {
		return err
	}
	defer store.Close()
	return runtime.identityService(ledger).ApplyIdentityBind(ctx, planID, request)
}

func (runtime *runtimeApplication) PlanIdentityRebind(ctx context.Context, request app.IdentityRebindRequest) (install.ChangeSet, error) {
	return runtime.identityService(previewLedger{}).PlanIdentityRebind(ctx, request)
}

func (runtime *runtimeApplication) ApplyIdentityRebind(ctx context.Context, planID string, request app.IdentityRebindRequest) error {
	store, ledger, err := runtime.openLedger(ctx)
	if err != nil {
		return err
	}
	defer store.Close()
	return runtime.identityService(ledger).ApplyIdentityRebind(ctx, planID, request)
}

func (runtime *runtimeApplication) PlanIdentityRevoke(ctx context.Context, request app.IdentityRevokeRequest) (install.ChangeSet, error) {
	return runtime.identityService(previewLedger{}).PlanIdentityRevoke(ctx, request)
}

func (runtime *runtimeApplication) ApplyIdentityRevoke(ctx context.Context, planID string, request app.IdentityRevokeRequest) error {
	store, ledger, err := runtime.openLedger(ctx)
	if err != nil {
		return err
	}
	defer store.Close()
	return runtime.identityService(ledger).ApplyIdentityRevoke(ctx, planID, request)
}

func (runtime *runtimeApplication) IdentityList(ctx context.Context) ([]app.IdentityDescriptor, error) {
	return runtime.identityService(previewLedger{}).IdentityList(ctx)
}

func (runtime *runtimeApplication) ListUnresolvedJournalEvents(ctx context.Context) ([]app.JournalEventDescriptor, error) {
	store, err := journal.OpenReadOnly(ctx, runtime.paths.Journal)
	if err != nil {
		return nil, err
	}
	defer store.Close()
	return runtime.journalMaintenanceService(store).ListUnresolvedJournalEvents(ctx)
}

func (runtime *runtimeApplication) PlanJournalResolution(ctx context.Context, request app.JournalResolutionRequest) (install.ChangeSet, error) {
	store, err := journal.OpenReadOnly(ctx, runtime.paths.Journal)
	if err != nil {
		return install.ChangeSet{}, err
	}
	defer store.Close()
	return runtime.journalMaintenanceService(store).PlanJournalResolution(ctx, request)
}

func (runtime *runtimeApplication) ApplyJournalResolution(ctx context.Context, planID string, request app.JournalResolutionRequest) error {
	store, err := journal.Open(ctx, runtime.paths.Journal)
	if err != nil {
		return err
	}
	defer store.Close()
	return runtime.journalMaintenanceService(store).ApplyJournalResolution(ctx, planID, request)
}

func (runtime *runtimeApplication) journalMaintenanceService(store *journal.Store) *app.Service {
	service := runtime.baseService
	service.Target = install.LocalTarget{}
	service.JournalMaintenance = store
	hostname, err := os.Hostname()
	if err != nil || strings.TrimSpace(hostname) == "" {
		hostname = "localhost"
	}
	service.OperatorID = fmt.Sprintf("uid:%d@%s", runtime.uid, sanitizeStableID(hostname))
	return &service
}

func (runtime *runtimeApplication) PlanHermesGrantRotation(ctx context.Context) (install.ChangeSet, error) {
	service, err := runtime.hermesCredentialService(ctx)
	if err != nil {
		return install.ChangeSet{}, err
	}
	return service.PlanHermesGrantRotation(ctx)
}

func (runtime *runtimeApplication) ApplyHermesGrantRotation(ctx context.Context, planID string) error {
	service, err := runtime.hermesCredentialService(ctx)
	if err != nil {
		return err
	}
	return service.ApplyHermesGrantRotation(ctx, planID)
}

func (runtime *runtimeApplication) hermesCredentialService(ctx context.Context) (*app.Service, error) {
	configuration, err := (config.Store{Path: runtime.paths.Config}).Load()
	if err != nil {
		return nil, err
	}
	adapter, enabled := configuration.Adapters[string(app.Hermes)]
	if !enabled || !adapter.Enabled {
		return nil, errors.New("Hermes adapter is not active")
	}
	machine := environmentDefault("MLINK_HERMES_MACHINE", "hermes-agent-env")
	detection, err := hermes.Detect(ctx, install.LocalTarget{}, machine)
	if err != nil {
		return nil, err
	}
	orbTarget, err := hermes.NewOrbTarget(machine, detection.HermesHome, nil)
	if err != nil {
		return nil, err
	}
	routingTarget, err := app.NewRoutingTarget(install.LocalTarget{}, orbTarget, detection.HermesHome)
	if err != nil {
		return nil, err
	}
	service := runtime.baseService
	service.Target = routingTarget
	service.Secrets = secret.Keychain{}
	service.HermesConfigPath = filepath.Join(detection.HermesHome, "mlink.json")
	service.Restarter = maintenanceRestarter{target: routingTarget, uid: runtime.uid}
	service.HermesGrantVerifier = httpHermesGrantVerifier{}
	return &service, nil
}

func (runtime *runtimeApplication) PlanUpgrade(ctx context.Context, request app.UpgradeRequest) (install.ChangeSet, error) {
	service, err := runtime.upgradeService(previewLedger{})
	if err != nil {
		return install.ChangeSet{}, err
	}
	return service.PlanUpgrade(ctx, request)
}

func (runtime *runtimeApplication) PlanCursorEnable(ctx context.Context) (install.ChangeSet, error) {
	return runtime.cursorLifecycleService(previewLedger{}).PlanCursorEnable(ctx)
}

func (runtime *runtimeApplication) ApplyCursorEnable(ctx context.Context, planID string) error {
	store, ledger, err := runtime.openLedger(ctx)
	if err != nil {
		return err
	}
	defer store.Close()
	return runtime.cursorLifecycleService(ledger).ApplyCursorEnable(ctx, planID)
}

func (runtime *runtimeApplication) cursorLifecycleService(ledger install.Ledger) *app.Service {
	service := runtime.baseService
	service.Paths = runtime.paths
	service.Target = install.LocalTarget{}
	service.Ledger = ledger
	return &service
}

func (runtime *runtimeApplication) ApplyUpgrade(ctx context.Context, planID string, request app.UpgradeRequest) error {
	store, ledger, err := runtime.openLedger(ctx)
	if err != nil {
		return err
	}
	defer store.Close()
	service, err := runtime.upgradeService(ledger)
	if err != nil {
		return err
	}
	return service.ApplyUpgrade(ctx, planID, request)
}

func (runtime *runtimeApplication) upgradeService(ledger install.Ledger) (*app.Service, error) {
	configuration, err := (config.Store{Path: runtime.paths.Config}).Load()
	if err != nil {
		return nil, err
	}
	target := install.LocalTarget{}
	loader := localUpgradeLoader{}
	service := runtime.baseService
	service.Paths = runtime.paths
	service.UID = runtime.uid
	service.Target = target
	service.Ledger = ledger
	service.ActiveSchema = configuration.SchemaVersion
	service.UpgradeCandidates = loader
	service.InstalledVerifier = localInstalledVerifier{Loader: loader}
	service.Restarter = maintenanceRestarter{target: target, uid: runtime.uid}
	return &service, nil
}

func (runtime *runtimeApplication) DetectIdentityCandidates(ctx context.Context) ([]identity.Candidate, error) {
	machine := environmentDefault("MLINK_HERMES_MACHINE", "hermes-agent-env")
	detection, err := hermes.Detect(ctx, install.LocalTarget{}, machine)
	if err != nil {
		return nil, err
	}
	return hermes.DetectIdentityCandidates(ctx, install.LocalTarget{}, machine, detection.HermesHome, 200)
}

func (runtime *runtimeApplication) ExportIdentity(ctx context.Context, passphrase []byte) ([]byte, error) {
	store, err := journal.OpenReadOnly(ctx, runtime.paths.Journal)
	if err != nil {
		return nil, err
	}
	defer store.Close()
	service := runtime.identityService(previewLedger{})
	service.ControlPlaneStates = store
	service.PrincipalAgentStates = store
	return service.ExportIdentity(ctx, passphrase, rand.Reader)
}

func (runtime *runtimeApplication) PlanIdentityImport(ctx context.Context, bundle identity.BundleV1) (install.ChangeSet, error) {
	store, err := journal.OpenReadOnly(ctx, runtime.paths.Journal)
	if err != nil {
		return install.ChangeSet{}, err
	}
	defer store.Close()
	service := runtime.identityService(previewLedger{})
	service.ControlPlaneStates = store
	service.PrincipalAgentStates = store
	return service.PlanIdentityImport(ctx, bundle)
}

func (runtime *runtimeApplication) ApplyIdentityImport(ctx context.Context, planID string, bundle identity.BundleV1) error {
	store, ledger, err := runtime.openLedger(ctx)
	if err != nil {
		return err
	}
	defer store.Close()
	service := runtime.identityService(ledger)
	service.ControlPlaneStates = store
	service.PrincipalAgentStates = store
	return service.ApplyIdentityImport(ctx, planID, bundle)
}

func (runtime *runtimeApplication) identityService(ledger install.Ledger) *app.Service {
	service := runtime.baseService
	service.Target = install.LocalTarget{}
	service.Ledger = ledger
	return &service
}

func (runtime *runtimeApplication) prepare(ctx context.Context, request app.InstallRequest, ledger install.Ledger) (*app.Service, app.InstallRequest, error) {
	machine := request.HermesMachine
	if machine == "" {
		machine = "hermes-agent-env"
	}
	hermesHome := request.HermesHome
	if containsAgent(request.Agents, app.Hermes) {
		if hermesHome == "" {
			detection, err := hermes.Detect(ctx, install.LocalTarget{}, machine)
			if err != nil {
				return nil, app.InstallRequest{}, err
			}
			hermesHome = detection.HermesHome
		}
	} else if hermesHome == "" {
		hermesHome = filepath.Join("/home", environmentDefault("USER", "user"), ".hermes")
	}
	orbTarget, err := hermes.NewOrbTarget(machine, hermesHome, nil)
	if err != nil {
		return nil, app.InstallRequest{}, err
	}
	router, err := app.NewRoutingTarget(install.LocalTarget{}, orbTarget, hermesHome)
	if err != nil {
		return nil, app.InstallRequest{}, err
	}
	service := runtime.baseService
	if containsAgent(request.Agents, app.Hermes) {
		if service.HermesEndpoint == "" || service.HermesListenAddress == "" {
			if service.HermesEndpoint != "" || service.HermesListenAddress != "" {
				return nil, app.InstallRequest{}, errors.New("both MLink Hermes Broker endpoint and listen address must be configured together")
			}
			hostAddresses, err := net.InterfaceAddrs()
			if err != nil {
				return nil, app.InstallRequest{}, fmt.Errorf("inspect host addresses for Hermes bridge: %w", err)
			}
			bridge, err := hermes.DetectBridge(ctx, install.LocalTarget{}, machine, hostAddresses, 8097)
			if err != nil {
				return nil, app.InstallRequest{}, err
			}
			service.HermesEndpoint = bridge.Endpoint
			service.HermesListenAddress = bridge.ListenAddress
		}
		service.HermesGrantToken = deriveHermesGrant(request.SecretInputs[app.MemoryCoreTokenSecret])
	}
	service.Target = router
	service.Ledger = ledger
	service.ProviderBackend = tencentdb.Deployment{
		Runner: install.LocalTarget{}, Target: router, Ledger: ledger, Secrets: secret.Keychain{},
		ConfigPath: filepath.Join(runtime.paths.Home, "memorycore", "tdai-gateway.yaml"),
		EnvPath:    filepath.Join(runtime.paths.Run, "memorycore.env"),
	}
	request.HermesMachine = machine
	request.HermesHome = hermesHome
	return &service, request, nil
}

func deriveHermesGrant(memoryCoreToken []byte) []byte {
	digest := hmac.New(sha256.New, memoryCoreToken)
	_, _ = digest.Write([]byte("dev.mlink/hermes-broker-grant/v1"))
	value := digest.Sum(nil)
	encoded := make([]byte, base64.RawURLEncoding.EncodedLen(len(value)))
	base64.RawURLEncoding.Encode(encoded, value)
	wipeRuntimeSecret(value)
	return encoded
}

func (runtime *runtimeApplication) openLedger(ctx context.Context) (*journal.Store, *journal.InstallationLedger, error) {
	store, err := journal.Open(ctx, runtime.paths.Journal)
	if err != nil {
		return nil, nil, err
	}
	ledger, err := journal.NewInstallationLedger(store, runtime.paths.Backups)
	if err != nil {
		_ = store.Close()
		return nil, nil, err
	}
	return store, ledger, nil
}

func containsAgent(agents []app.Agent, want app.Agent) bool {
	for _, agent := range agents {
		if agent == want {
			return true
		}
	}
	return false
}

func environmentDefault(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func sanitizeStableID(value string) string {
	var output strings.Builder
	for _, char := range value {
		if char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || strings.ContainsRune("._-", char) {
			output.WriteRune(char)
		} else {
			output.WriteByte('-')
		}
	}
	if output.Len() == 0 {
		return "local-user"
	}
	return output.String()
}
