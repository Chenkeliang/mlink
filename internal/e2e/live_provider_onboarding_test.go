package e2e

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mlink/internal/app"
	"mlink/internal/config"
	"mlink/internal/controlplane"
	"mlink/internal/install"
	"mlink/internal/journal"
	"mlink/internal/layout"
	"mlink/internal/model"
	"mlink/internal/panel"
	"mlink/internal/provider/lifecycle"
	"mlink/internal/provider/tencentdb"
)

func TestLiveOfficialProviderOnboarding(t *testing.T) {
	if os.Getenv("MLINK_TEST_PROVIDER_ONBOARDING") != "1" {
		t.Skip("set MLINK_TEST_PROVIDER_ONBOARDING=1 for isolated official-image acceptance")
	}
	llmBaseURL := strings.TrimSpace(os.Getenv("MLINK_TEST_LLM_BASE_URL"))
	llmModel := strings.TrimSpace(os.Getenv("MLINK_TEST_LLM_MODEL"))
	llmAPIKey := strings.TrimSpace(os.Getenv("MLINK_TEST_LLM_API_KEY"))
	if llmBaseURL == "" || llmModel == "" || llmAPIKey == "" {
		t.Fatal("isolated acceptance requires protected memory LLM test inputs")
	}

	runID := fmt.Sprintf("mlink-e2e-%d", time.Now().UnixNano())
	hostPort := isolatedPort(t)
	layoutSpec := tencentdb.DeploymentLayout{
		ContainerName: runID + "-core", VolumeName: runID + "-data", NetworkName: runID + "-net",
		HostAddress: "127.0.0.1", HostPort: hostPort,
	}
	if layoutSpec.ContainerName == tencentdb.MemoryCoreContainerName || layoutSpec.VolumeName == tencentdb.MemoryCoreVolumeName ||
		layoutSpec.NetworkName == tencentdb.MemoryCoreNetworkName || hostPort == 8420 {
		t.Fatal("live acceptance refuses production MemoryCore resources")
	}
	cleanupIsolatedProvider(t, layoutSpec)
	defer cleanupIsolatedProvider(t, layoutSpec)

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	root := t.TempDir()
	testLedger := &ledger{backups: make(map[string]install.Backup)}
	keys := secrets{}
	gatewayToken := randomHex(t, 32)
	endpoint := fmt.Sprintf("http://127.0.0.1:%d", hostPort)
	deployment := tencentdb.Deployment{
		Runner: install.LocalTarget{}, Target: install.LocalTarget{}, Ledger: testLedger, Secrets: keys,
		ConfigPath: filepath.Join(root, "tdai-gateway.yaml"), EnvPath: filepath.Join(root, "memorycore.env"),
		HealthTimeout: 2 * time.Minute, PollInterval: 500 * time.Millisecond, Layout: layoutSpec,
	}
	request := lifecycle.BackendInstallRequest{
		ProviderID: "dev.mlink.tencentdb", Endpoint: endpoint, GatewayToken: gatewayToken,
		LLMBaseURL: llmBaseURL, LLMModel: llmModel, LLMAPIKey: []byte(llmAPIKey),
	}
	plan, err := deployment.PlanInstall(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if err := deployment.ApplyInstall(ctx, plan.PlanID, request); err != nil {
		t.Fatal(err)
	}
	if status, err := deployment.Detect(ctx, config.Connection{
		ID: "local", ProviderID: "dev.mlink.tencentdb", ProviderVersion: "0.1.0", ConfigRevision: "e2e",
		ProviderConfig: map[string]any{"base_url": endpoint, "service_id": runID, "timeout_ms": 5000},
	}); err != nil || status.State != lifecycle.BackendReachable || !status.Installed {
		t.Fatalf("official backend status/error = %#v/%v", status, err)
	}

	client, err := tencentdb.NewClient(tencentdb.Config{
		BaseURL: endpoint, Token: string(gatewayToken), ServiceID: runID, HTTPClient: &http.Client{Timeout: 20 * time.Second},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer destroyTestInstance(context.Background(), endpoint, string(gatewayToken), runID)
	store, err := journal.Open(ctx, filepath.Join(root, "journal.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	controlService := controlplane.Service{Metadata: tencentdb.NewMetadataClient(client), Secrets: keys, States: store}
	controlRequest := controlplane.ProvisionRequest{
		InstallationID: runID, InstanceID: runID, AdminUsername: runID + "-admin", OwnerUsername: runID + "-owner",
		TeamName: "MLink " + runID, OwnerAgentName: "MLink Owner " + runID, DynamicAgentLimit: 10,
	}
	controlPlan, err := controlService.PlanProvision(ctx, controlRequest)
	if err != nil {
		t.Fatal(err)
	}
	controlResult, err := controlService.ApplyProvision(ctx, controlPlan.PlanID, controlRequest)
	if err != nil {
		t.Fatal(err)
	}
	if controlResult.OwnerUserID == "" || controlResult.OwnerTeamID == "" || controlResult.OwnerAgentID == "" || controlResult.OwnerAssetID == "" {
		t.Fatal("MemoryCore did not return permanent control-plane IDs")
	}

	mlinkTarget := &target{files: map[string]file{}}
	mlinkPaths, err := layout.FromHome("/Users/mlink-e2e", "/tmp/mlink-e2e-candidate")
	if err != nil {
		t.Fatal(err)
	}
	mlinkTarget.files[mlinkPaths.SourceExecutable] = file{content: []byte("candidate"), mode: 0o700}
	mlinkTarget.files["/Users/mlink-e2e/.codex/hooks.json"] = file{content: []byte(`{"hooks":{}}`), mode: 0o600}
	installService := app.Service{
		Paths: mlinkPaths, UID: 501, Target: mlinkTarget, Ledger: testLedger, Secrets: keys,
		IdentityKey: bytes.Repeat([]byte{0x2a}, 32), ControlPlaneStates: store,
	}
	installRequest := app.InstallRequest{
		Agents: []app.Agent{app.Codex}, OwnerSlug: "owner", DynamicAgentLimit: 10,
		Connection: config.Connection{
			ID: "local", ProviderID: "dev.mlink.tencentdb", ProviderVersion: "0.1.0", ConfigRevision: "e2e",
			ProviderConfig: map[string]any{"base_url": endpoint, "service_id": runID, "timeout_ms": 5000},
			TenantID:       runID, AgentID: "custom-must-not-survive", UserID: "custom-must-not-survive",
		},
		SecretInputs: map[string][]byte{app.MemoryCoreTokenSecret: append([]byte(nil), gatewayToken...)},
	}
	installPlan, err := installService.PlanInstall(ctx, installRequest)
	if err != nil {
		t.Fatal(err)
	}
	if err := installService.ApplyInstall(ctx, installPlan.PlanID, installRequest); err != nil {
		t.Fatal(err)
	}
	installed, err := config.Decode(mlinkTarget.files[mlinkPaths.Config].content)
	if err != nil {
		t.Fatal(err)
	}
	if installed.ControlPlane == nil || installed.ControlPlane.OwnerAgentID != controlResult.OwnerAgentID ||
		installed.Connections["local"].AgentID != "" || installed.Connections["local"].UserID != "" {
		t.Fatalf("first writable configuration did not use Core IDs: %#v", installed)
	}

	provider := tencentdb.NewProvider(client)
	canary := "MLINK_PROVIDER_E2E_" + string(randomHex(t, 8))
	identityScope := model.IdentityScope{
		TenantID: controlResult.OwnerTeamID, AgentID: controlResult.OwnerAgentID, UserID: controlResult.OwnerUserID,
		SessionID: runID + "-session", TurnID: runID + "-turn",
	}
	messages := make([]model.Message, 0, 10)
	for range 5 {
		messages = append(messages,
			model.Message{Role: "user", Content: "我的长期测试代号是 " + canary + "。"},
			model.Message{Role: "assistant", Content: "已记录长期测试代号 " + canary + "。"},
		)
	}
	if _, err := provider.CaptureTurn(ctx, model.Turn{Identity: identityScope, Messages: messages}); err != nil {
		t.Fatal(err)
	}
	if !eventuallyRecallGeneratedID(ctx, provider, identityScope, canary) {
		t.Fatal("memory captured under Core-generated IDs was not recalled")
	}

	beforePanel, err := store.LoadControlPlane(ctx)
	if err != nil {
		t.Fatal(err)
	}
	panelTarget := &target{files: map[string]file{}}
	panelService := app.Service{
		ControlPlaneStates: store, PanelRuntime: &panel.Runtime{Runner: panelTarget, Target: panelTarget},
		PanelDesired: panel.Desired{
			RegistryPath: "/Users/mlink-e2e/.mlink/panel/metadata-instances.json", HostAddress: "127.0.0.1",
			PanelHostPort: 8125, KnowledgeHostPort: 8424, InstanceID: runID, InstanceName: "MLink E2E",
			GatewayEndpoint: "http://host.docker.internal:8420", KnowledgePublicBaseURL: "http://host.docker.internal:8424/v3",
			KnowledgeLLMProxyBaseURL: "http://host.docker.internal:8420",
		},
	}
	if _, err := panelService.PlanPanelRuntime(ctx); err != nil {
		t.Fatal(err)
	}
	afterPanel, err := store.LoadControlPlane(ctx)
	if err != nil || beforePanel.OwnerUserID != afterPanel.OwnerUserID || beforePanel.OwnerTeamID != afterPanel.OwnerTeamID ||
		beforePanel.OwnerAgentID != afterPanel.OwnerAgentID || beforePanel.OwnerAssetID != afterPanel.OwnerAssetID {
		t.Fatalf("Panel-later changed Core identity: before=%#v after=%#v err=%v", beforePanel, afterPanel, err)
	}
	t.Logf("provider_plan=%s control_plan=%s install_plan=%s owner_user=…%s owner_team=…%s owner_agent=…%s owner_asset=…%s",
		plan.PlanID, controlPlan.PlanID, installPlan.PlanID, idSuffix(controlResult.OwnerUserID), idSuffix(controlResult.OwnerTeamID),
		idSuffix(controlResult.OwnerAgentID), idSuffix(controlResult.OwnerAssetID))

	uninstallPlan, err := deployment.PlanUninstall(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := install.NewTransaction(install.LocalTarget{}, testLedger).Apply(ctx, uninstallPlan); err != nil {
		t.Fatal(err)
	}
	if exec.CommandContext(ctx, "docker", "inspect", layoutSpec.ContainerName).Run() == nil {
		t.Fatal("ordinary uninstall left the owned MemoryCore container")
	}
	if err := exec.CommandContext(ctx, "docker", "volume", "inspect", layoutSpec.VolumeName).Run(); err != nil {
		t.Fatal("ordinary uninstall removed the MemoryCore data volume")
	}
}

func isolatedPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	return listener.Addr().(*net.TCPAddr).Port
}

func randomHex(t *testing.T, size int) []byte {
	t.Helper()
	value := make([]byte, size)
	if _, err := rand.Read(value); err != nil {
		t.Fatal(err)
	}
	encoded := make([]byte, hex.EncodedLen(len(value)))
	hex.Encode(encoded, value)
	for index := range value {
		value[index] = 0
	}
	return encoded
}

func eventuallyRecallGeneratedID(ctx context.Context, provider *tencentdb.Provider, identityScope model.IdentityScope, canary string) bool {
	deadline := time.Now().Add(3 * time.Minute)
	identityScope.SessionID += "-recall"
	identityScope.TurnID = ""
	for time.Now().Before(deadline) {
		bundle, err := provider.Recall(ctx, model.RecallRequest{Identity: identityScope, Query: canary, MaxItems: 20, IncludeAgentShared: true})
		if err == nil {
			for _, item := range bundle.Items {
				if strings.Contains(item.Text, canary) {
					return true
				}
			}
		}
		time.Sleep(2 * time.Second)
	}
	return false
}

func cleanupIsolatedProvider(t *testing.T, layoutSpec tencentdb.DeploymentLayout) {
	t.Helper()
	for _, value := range []string{layoutSpec.ContainerName, layoutSpec.VolumeName, layoutSpec.NetworkName} {
		if !strings.HasPrefix(value, "mlink-e2e-") {
			t.Fatalf("refusing cleanup of non-test resource %q", value)
		}
	}
	_ = exec.Command("docker", "rm", "-f", layoutSpec.ContainerName).Run()
	_ = exec.Command("docker", "volume", "rm", layoutSpec.VolumeName).Run()
	_ = exec.Command("docker", "network", "rm", layoutSpec.NetworkName).Run()
}

func idSuffix(value string) string {
	if len(value) <= 8 {
		return value
	}
	return value[len(value)-8:]
}
