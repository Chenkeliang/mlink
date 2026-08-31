package main

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	adapterclient "mlink/internal/adapter/client"
	"mlink/internal/adapter/codex"
	"mlink/internal/adapter/hermes"
	"mlink/internal/app"
	"mlink/internal/backend"
	"mlink/internal/broker"
	"mlink/internal/cli"
	"mlink/internal/config"
	"mlink/internal/identity"
	"mlink/internal/install"
	"mlink/internal/journal"
	"mlink/internal/layout"
	"mlink/internal/provider/tencentdb"
	"mlink/internal/secret"
	"mlink/internal/tui"
)

type runtimeApplication struct {
	paths       layout.Paths
	uid         int
	baseService app.Service
}

type previewLedger struct{}

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
	dependencies.ServeBroker = runtime.ServeBroker
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
	keychain := secret.Keychain{}
	router, grants, hermesEnabled, err := runtimeRouter(ctx, configuration, keychain)
	if err != nil {
		return err
	}
	defer wipeRuntimeSecret(router.Key)
	defer router.Bindings.Wipe()
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
	server := broker.Server{
		Service:    broker.Service{Journal: journalStore, Provider: providerRuntime},
		Authorizer: broker.Authorizer{Router: router, Grants: grants},
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
	for _, adapterID := range []string{"codex", "pi"} {
		adapter, enabled := configuration.Adapters[adapterID]
		if !enabled || !adapter.Enabled {
			continue
		}
		grants = append(grants, broker.Grant{
			AdapterID: adapterID, Mode: broker.IdentityFixed, FixedSpaceID: adapter.SpaceID,
			AllowedSpaceIDs: map[string]bool{adapter.SpaceID: true},
		})
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
	grants = append(grants, broker.Grant{
		TokenDigest: digest[:], AdapterID: "hermes", Mode: broker.IdentityDelegated, Source: "feishu",
		AllowedSpaceIDs: map[string]bool{
			hermesRouting.OwnerSpaceID: true, hermesRouting.PrivateSpaceID: true, hermesRouting.GroupSpaceID: true,
		},
	})
	return router, grants, true, nil
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
				"base_url":   environmentDefault("MLINK_MEMORYCORE_BASE_URL", "http://127.0.0.1:8096"),
				"service_id": environmentDefault("MLINK_MEMORYCORE_SERVICE_ID", "default"),
				"timeout_ms": 5000,
			},
			TenantID:           teamID,
			AgentID:            environmentDefault("MLINK_MEMORY_AGENT_ID", "default"),
			UserID:             environmentDefault("MLINK_MEMORY_USER_ID", username),
			IncludeAgentShared: false,
		},
		HermesMachine: environmentDefault("MLINK_HERMES_MACHINE", "hermes-agent-env"),
	}
}

func (runtime *runtimeApplication) PlanInstall(ctx context.Context, request app.InstallRequest) (install.ChangeSet, error) {
	service, request, err := runtime.prepare(ctx, request, previewLedger{})
	if err != nil {
		return install.ChangeSet{}, err
	}
	return service.PlanInstall(ctx, request)
}

func (runtime *runtimeApplication) ApplyInstall(ctx context.Context, planID string, request app.InstallRequest) error {
	previewService, preparedRequest, err := runtime.prepare(ctx, request, previewLedger{})
	if err != nil {
		return err
	}
	preview, err := previewService.PlanInstall(ctx, preparedRequest)
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
	return service.ApplyInstall(ctx, planID, request)
}

func (runtime *runtimeApplication) PlanRestore(ctx context.Context, request app.RestoreRequest) (install.ChangeSet, error) {
	store, ledger, err := runtime.openLedger(ctx)
	if err != nil {
		return install.ChangeSet{}, err
	}
	defer store.Close()
	installRequest := defaultInstallRequest()
	installRequest.Agents = []app.Agent{app.Codex, app.Pi, app.Hermes}
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
	installRequest.Agents = []app.Agent{app.Codex, app.Pi, app.Hermes}
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
	return service.ApplyUninstall(ctx, planID, request)
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

func (runtime *runtimeApplication) DetectIdentityCandidates(ctx context.Context) ([]identity.Candidate, error) {
	machine := environmentDefault("MLINK_HERMES_MACHINE", "hermes-agent-env")
	detection, err := hermes.Detect(ctx, install.LocalTarget{}, machine)
	if err != nil {
		return nil, err
	}
	return hermes.DetectIdentityCandidates(ctx, install.LocalTarget{}, machine, detection.HermesHome, 200)
}

func (runtime *runtimeApplication) ExportIdentity(ctx context.Context, passphrase []byte) ([]byte, error) {
	return runtime.identityService(previewLedger{}).ExportIdentity(ctx, passphrase, rand.Reader)
}

func (runtime *runtimeApplication) PlanIdentityImport(ctx context.Context, bundle identity.BundleV1) (install.ChangeSet, error) {
	return runtime.identityService(previewLedger{}).PlanIdentityImport(ctx, bundle)
}

func (runtime *runtimeApplication) ApplyIdentityImport(ctx context.Context, planID string, bundle identity.BundleV1) error {
	store, ledger, err := runtime.openLedger(ctx)
	if err != nil {
		return err
	}
	defer store.Close()
	return runtime.identityService(ledger).ApplyIdentityImport(ctx, planID, bundle)
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
