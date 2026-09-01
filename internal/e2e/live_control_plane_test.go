package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"mlink/internal/controlplane"
	"mlink/internal/journal"
	"mlink/internal/panel"
	"mlink/internal/provider/tencentdb"
)

func TestLiveControlPlaneAndOfficialPanel(t *testing.T) {
	if os.Getenv("MLINK_TEST_CONTROL_PLANE") != "1" {
		t.Skip("set MLINK_TEST_CONTROL_PLANE=1 for isolated live acceptance")
	}
	baseURL := os.Getenv("MLINK_TEST_MEMORYCORE_URL")
	token := os.Getenv("MLINK_TEST_MEMORYCORE_TOKEN")
	serviceID := os.Getenv("MLINK_TEST_SERVICE_ID")
	panelPort, err := strconv.Atoi(os.Getenv("MLINK_TEST_PANEL_PORT"))
	knowledgePort, knowledgeErr := strconv.Atoi(os.Getenv("MLINK_TEST_KNOWLEDGE_PORT"))
	if err != nil || knowledgeErr != nil || panelPort <= 1024 || panelPort == 8125 || knowledgePort <= 1024 || knowledgePort == 8424 || panelPort == knowledgePort || baseURL != "http://127.0.0.1:8420" || token == "" ||
		serviceID == "default" || !strings.HasPrefix(serviceID, "mlink-control-e2e-") {
		t.Fatal("safe unique live-test URL, token, Service ID, and non-production Panel port are required")
	}
	containerName := serviceID + "-panel"
	volumeName := serviceID + "-knowledge"
	if containerName == panel.ContainerName {
		t.Fatal("live test refuses the production Panel container name")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	defer destroyTestInstance(context.Background(), baseURL, token, serviceID)
	client, err := tencentdb.NewClient(tencentdb.Config{BaseURL: baseURL, Token: token, ServiceID: serviceID, HTTPClient: &http.Client{Timeout: 10 * time.Second}})
	if err != nil {
		t.Fatal(err)
	}
	metadata := tencentdb.NewMetadataClient(client)
	store, err := journal.Open(ctx, filepath.Join(t.TempDir(), "journal.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	keys := secrets{}
	service := controlplane.Service{Metadata: metadata, Secrets: keys, States: store}
	request := controlplane.ProvisionRequest{
		InstallationID: serviceID, InstanceID: serviceID, AdminUsername: "mlink-admin-" + serviceID,
		OwnerUsername: "mlink-owner-" + serviceID, TeamName: "MLink " + serviceID, OwnerAgentName: "MLink Owner " + serviceID,
	}
	plan, err := service.PlanProvision(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.ApplyProvision(ctx, plan.PlanID, request)
	if err != nil {
		t.Fatal(err)
	}
	if result.OwnerUserID == "" || result.OwnerAgentID == "" || bytes.Equal(keys[controlplane.AdminUserKeyAccount], keys[controlplane.OwnerUserKeyAccount]) {
		t.Fatal("Core did not create distinct admin and Owner identities")
	}
	provisioner := &controlplane.AgentProvisioner{Metadata: metadata, Secrets: keys, Store: store, MaxDynamicAgents: 10, Timeout: 10 * time.Second}
	mapping, err := provisioner.ResolveOrCreate(ctx, controlplane.PrincipalIntent{
		Fingerprint: "prn_aaaaaaaaaaaaaaaaaaaaaaaaaa", RouteKind: "hermes-private", DisplayLabel: "Live E2E DM",
	})
	if err != nil || mapping.BackendAgentID == "" || strings.Contains(mapping.DisplayLabel, "ou_") {
		t.Fatalf("dynamic mapping = %#v, %v", mapping, err)
	}

	registryPath := filepath.Join(t.TempDir(), "metadata-instances.json")
	registry, err := panel.RenderRegistry(panel.RegistryInput{
		InstanceID: serviceID, InstanceName: "MLink E2E", GatewayEndpoint: "http://host.docker.internal:8420", GatewayToken: []byte(token),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(registryPath, registry, 0o600); err != nil {
		t.Fatal(err)
	}
	runCommand(t, ctx, "docker", "pull", panel.ImageReference)
	runCommand(t, ctx, "docker", "volume", "create", "--label", "dev.mlink.component=memory-hub-e2e", volumeName)
	defer func() {
		_ = exec.Command("docker", "rm", "-f", containerName).Run()
		_ = exec.Command("docker", "volume", "rm", volumeName).Run()
	}()
	runCommand(t, ctx, "docker", "run", "-d", "--name", containerName, "--add-host", "host.docker.internal:host-gateway",
		"-p", fmt.Sprintf("127.0.0.1:%d:8125", panelPort), "-p", fmt.Sprintf("127.0.0.1:%d:8424", knowledgePort),
		"-e", fmt.Sprintf("KNOWLEDGE_PUBLIC_BASE_URL=http://host.docker.internal:%d/v3", knowledgePort),
		"-e", "KNOWLEDGE_LLM_PROXY_BASE_URL=http://host.docker.internal:8420", "-e", "LLM_MODE=proxy",
		"-e", "KNOWLEDGE_LLM_BINDING_SYNC=true", "-v", volumeName+":/data/knowledge",
		"-v", registryPath+":/app/panel/config/metadata-instances.json:ro", panel.ImageReference)
	waitPanel(t, ctx, panelPort, knowledgePort, serviceID, keys[controlplane.OwnerUserKeyAccount])
}

func runCommand(t *testing.T, ctx context.Context, name string, args ...string) {
	t.Helper()
	command := exec.CommandContext(ctx, name, args...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("%s failed: %v: %s", name, err, output)
	}
}

func waitPanel(t *testing.T, ctx context.Context, port, knowledgePort int, serviceID string, ownerKey []byte) {
	t.Helper()
	client := &http.Client{Timeout: 3 * time.Second}
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		response, err := client.Get(base + "/api/v1/meta/instances")
		if err == nil {
			body, _ := io.ReadAll(response.Body)
			response.Body.Close()
			if response.StatusCode == http.StatusOK && bytes.Contains(body, []byte(serviceID)) {
				payload, _ := json.Marshal(map[string]string{"user_key": string(ownerKey)})
				request, _ := http.NewRequestWithContext(ctx, http.MethodPost, base+"/api/v1/meta/auth/verify", bytes.NewReader(payload))
				request.Header.Set("Content-Type", "application/json")
				request.Header.Set("x-tdai-service-id", serviceID)
				verified, verifyErr := client.Do(request)
				if verifyErr == nil {
					verifyBody, _ := io.ReadAll(verified.Body)
					verified.Body.Close()
					if verified.StatusCode == http.StatusOK && bytes.Contains(verifyBody, []byte(`"valid":true`)) {
						knowledge, knowledgeErr := client.Get(fmt.Sprintf("http://127.0.0.1:%d/health", knowledgePort))
						if knowledgeErr == nil {
							knowledge.Body.Close()
							if knowledge.StatusCode == http.StatusOK {
								return
							}
						}
					}
				}
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatal("official Panel did not become healthy with a working Owner login")
}

func destroyTestInstance(ctx context.Context, baseURL, token, serviceID string) {
	if serviceID == "" || serviceID == "default" || !strings.HasPrefix(serviceID, "mlink-control-e2e-") {
		return
	}
	body, _ := json.Marshal(map[string]string{"instance_id": serviceID})
	request, _ := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/v3/instance/destroy", bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("x-tdai-service-id", serviceID)
	response, err := http.DefaultClient.Do(request)
	if err == nil {
		response.Body.Close()
	}
}
