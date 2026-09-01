package tencentdb

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"text/template"
	"time"

	"mlink/internal/config"
	"mlink/internal/install"
	"mlink/internal/provider/lifecycle"
	"mlink/internal/secret"
)

const (
	MemoryCoreImageReference = "agentmemory/memory-core@sha256:9798254a8cc06276b7c5b3c19df49f136fae25d579564e1f01f9c4b9b8cd2d11"
	MemoryCoreContainerName  = "tdai-memory-core"
	MemoryCoreVolumeName     = "tdai-memory-core-data"
	MemoryCoreNetworkName    = "tdai-memory-stack"
)

type Deployment struct {
	Runner        install.CommandRunner
	Target        install.Target
	Ledger        install.Ledger
	Secrets       secret.Store
	HTTPClient    *http.Client
	ConfigPath    string
	EnvPath       string
	HealthTimeout time.Duration
	PollInterval  time.Duration
}

//go:embed templates/tdai-gateway.yaml.tmpl
var gatewayConfigTemplate string

func (deployment Deployment) Detect(ctx context.Context, connection config.Connection) (lifecycle.BackendStatus, error) {
	if connection.ProviderID != providerID || connection.ProviderVersion != providerVersion {
		return lifecycle.BackendStatus{}, errors.New("TencentDB connection identity is incompatible")
	}
	rawEndpoint, ok := connection.ProviderConfig["base_url"].(string)
	if !ok {
		return lifecycle.BackendStatus{}, errors.New("valid MemoryCore endpoint is required")
	}
	endpoint, local, err := deploymentEndpoint(rawEndpoint)
	if err != nil {
		return lifecycle.BackendStatus{}, err
	}
	status := lifecycle.BackendStatus{ProviderID: providerID, Endpoint: strings.TrimRight(endpoint.String(), "/"), Local: local}
	version, compatible, healthErr := deployment.health(ctx, status.Endpoint)
	if healthErr == nil {
		if !compatible {
			status.State = lifecycle.BackendIncompatible
			return status, nil
		}
		status.State, status.Version = lifecycle.BackendReachable, version
		if local {
			if inspected, inspectErr := deployment.inspect(ctx); inspectErr == nil && inspected.compatible() {
				status.Installed = true
			}
		}
		return status, nil
	}
	if !local {
		status.State = lifecycle.BackendRemoteUnreachable
		return status, nil
	}
	inspected, inspectErr := deployment.inspect(ctx)
	if inspectErr != nil {
		status.State = lifecycle.BackendAbsent
		return status, nil
	}
	status.Installed = true
	if !inspected.compatible() {
		status.State = lifecycle.BackendIncompatible
		return status, nil
	}
	if inspected.State.Status == "exited" || inspected.State.Status == "created" || inspected.State.Status == "paused" {
		status.State = lifecycle.BackendStopped
		return status, nil
	}
	status.State = lifecycle.BackendIncompatible
	return status, nil
}

func deploymentEndpoint(raw string) (*url.URL, bool, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return nil, false, errors.New("valid MemoryCore endpoint is required")
	}
	hostname := strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
	address := net.ParseIP(hostname)
	local := hostname == "localhost" || address != nil && address.IsLoopback()
	if local && parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, false, errors.New("local MemoryCore endpoint must use HTTP or HTTPS")
	}
	if !local && parsed.Scheme != "https" {
		return nil, false, errors.New("remote MemoryCore endpoint must use HTTPS")
	}
	return parsed, local, nil
}

func (deployment Deployment) health(ctx context.Context, endpoint string) (string, bool, error) {
	client := deployment.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 1500 * time.Millisecond}
	}
	requestCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, strings.TrimRight(endpoint, "/")+"/health", nil)
	if err != nil {
		return "", false, err
	}
	response, err := client.Do(request)
	if err != nil {
		return "", false, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 64<<10))
	if err != nil || response.StatusCode != http.StatusOK {
		return "", false, errors.New("MemoryCore health unavailable")
	}
	var value struct {
		Status   string                     `json:"status"`
		Version  string                     `json:"version"`
		Services map[string]json.RawMessage `json:"services"`
	}
	if json.Unmarshal(body, &value) != nil {
		return "", false, nil
	}
	compatible := value.Status == "ok" && strings.TrimSpace(value.Version) != "" && value.Services["pipelineWorker"] != nil
	return value.Version, compatible, nil
}

type coreInspect struct {
	Config struct {
		Image  string            `json:"Image"`
		Labels map[string]string `json:"Labels"`
	} `json:"Config"`
	State struct {
		Status string `json:"Status"`
	} `json:"State"`
	Mounts []struct {
		Name        string `json:"Name"`
		Destination string `json:"Destination"`
	} `json:"Mounts"`
	HostConfig struct {
		PortBindings map[string][]struct {
			HostIP   string `json:"HostIp"`
			HostPort string `json:"HostPort"`
		} `json:"PortBindings"`
	} `json:"HostConfig"`
}

func (deployment Deployment) inspect(ctx context.Context) (coreInspect, error) {
	runner := deployment.Runner
	if runner == nil {
		runner = install.LocalTarget{}
	}
	output, err := runner.Run(ctx, []string{"docker", "inspect", MemoryCoreContainerName}, nil)
	if err != nil {
		return coreInspect{}, err
	}
	var values []coreInspect
	if json.Unmarshal(output, &values) != nil || len(values) != 1 {
		return coreInspect{}, errors.New("invalid MemoryCore container inspection")
	}
	return values[0], nil
}

func (value coreInspect) compatible() bool {
	if value.Config.Image != MemoryCoreImageReference || value.Config.Labels["dev.mlink.component"] != "memory-core" {
		return false
	}
	volume := false
	for _, mount := range value.Mounts {
		volume = volume || mount.Name == MemoryCoreVolumeName && mount.Destination == "/data/tdai-memory"
	}
	bindings := value.HostConfig.PortBindings["8420/tcp"]
	port := false
	for _, binding := range bindings {
		port = port || binding.HostIP == "127.0.0.1" && binding.HostPort == "8420"
	}
	return volume && port
}

func (deployment Deployment) PlanInstall(ctx context.Context, request lifecycle.BackendInstallRequest) (install.ChangeSet, error) {
	if err := deployment.validateInstallRequest(request); err != nil {
		return install.ChangeSet{}, err
	}
	if _, err := deployment.inspect(ctx); err == nil {
		return install.ChangeSet{}, errors.New("MemoryCore container already exists")
	}
	configData, err := renderGatewayConfig(request)
	if err != nil {
		return install.ChangeSet{}, err
	}
	resources := []install.DesiredResource{{
		OwnerID: "dev.mlink.memorycore.config", Target: deployment.ConfigPath, Content: configData, Mode: 0o600,
		SemanticDiff: []install.SemanticDiff{{Path: "memorycore.config", Before: "absent or owned", After: "official standalone config; secret values supplied at runtime"}},
	}}
	resources = append(resources, install.DesiredResource{
		OwnerID: "dev.mlink.memorycore.image", Target: "service:docker-pull:" + MemoryCoreImageReference, Action: install.ActionService,
		Command:      []string{"docker", "pull", MemoryCoreImageReference},
		SemanticDiff: []install.SemanticDiff{{Path: "memorycore.image", Before: "absent or cached", After: MemoryCoreImageReference}},
	})
	if !deployment.dockerObjectExists(ctx, "network", MemoryCoreNetworkName) {
		resources = append(resources, install.DesiredResource{
			OwnerID: "dev.mlink.memorycore.network", Target: "service:docker-network:" + MemoryCoreNetworkName, Action: install.ActionService,
			Command:         []string{"docker", "network", "create", "--label", "dev.mlink.component=memory-core", MemoryCoreNetworkName},
			RollbackCommand: []string{"docker", "network", "rm", MemoryCoreNetworkName},
			SemanticDiff:    []install.SemanticDiff{{Path: "memorycore.network", Before: "absent", After: MemoryCoreNetworkName}},
		})
	}
	if !deployment.dockerObjectExists(ctx, "volume", MemoryCoreVolumeName) {
		resources = append(resources, install.DesiredResource{
			OwnerID: "dev.mlink.memorycore.volume", Target: "service:docker-volume:" + MemoryCoreVolumeName, Action: install.ActionService,
			Command:      []string{"docker", "volume", "create", "--label", "dev.mlink.component=memory-core", MemoryCoreVolumeName},
			SemanticDiff: []install.SemanticDiff{{Path: "memorycore.data-volume", Before: "absent", After: MemoryCoreVolumeName + "; retained on rollback and ordinary uninstall"}},
		})
	}
	run := []string{
		"docker", "run", "-d", "--name", MemoryCoreContainerName, "--restart", "unless-stopped",
		"--label", "dev.mlink.component=memory-core", "--network", MemoryCoreNetworkName,
		"-p", "127.0.0.1:8420:8420", "-v", MemoryCoreVolumeName + ":/data/tdai-memory",
		"-v", deployment.ConfigPath + ":/data/config/tdai-gateway.yaml:ro", "--env-file", deployment.EnvPath,
		"-e", "TDAI_GATEWAY_PORT=8420", "-e", "TDAI_GATEWAY_HOST=0.0.0.0", "-e", "TDAI_DATA_DIR=/data/tdai-memory",
		MemoryCoreImageReference,
	}
	resources = append(resources, install.DesiredResource{
		OwnerID: "dev.mlink.memorycore.container", Target: "service:docker-run:" + MemoryCoreContainerName, Action: install.ActionService,
		Command: run, RollbackCommand: []string{"docker", "rm", "-f", MemoryCoreContainerName},
		SemanticDiff: []install.SemanticDiff{
			{Path: "memorycore.container", Before: "absent", After: MemoryCoreContainerName},
			{Path: "memorycore.port", Before: "available", After: "127.0.0.1:8420:8420"},
			{Path: "memorycore.secret-transport", Before: "Keychain inputs", After: "private temporary env file removed after startup"},
		},
	})
	plan, err := install.BuildChangeSet(deployment.Target, resources)
	if err != nil {
		return install.ChangeSet{}, err
	}
	plan.SelectedConnection = "local"
	return plan, nil
}

func (deployment Deployment) ApplyInstall(ctx context.Context, planID string, request lifecycle.BackendInstallRequest) error {
	plan, err := deployment.PlanInstall(ctx, request)
	if err != nil {
		return err
	}
	if plan.PlanID != planID {
		return fmt.Errorf("%w: MemoryCore install changed after preview", install.ErrPlanStale)
	}
	previous, err := deployment.putInstallSecrets(ctx, request)
	if err != nil {
		return err
	}
	defer wipeSecretSnapshots(previous)
	envData := []byte("TDAI_GATEWAY_API_KEY=" + string(request.GatewayToken) + "\nTDAI_LLM_BASE_URL=" + request.LLMBaseURL + "\nTDAI_LLM_MODEL=" + request.LLMModel + "\nTDAI_LLM_API_KEY=" + string(request.LLMAPIKey) + "\n")
	defer wipeBytes(envData)
	if err := deployment.Target.WriteAtomic(ctx, deployment.EnvPath, envData, 0o600); err != nil {
		_ = deployment.restoreInstallSecrets(ctx, previous)
		return err
	}
	defer deployment.Target.Remove(context.Background(), deployment.EnvPath)
	transaction := install.NewTransaction(deployment.Target, deployment.Ledger)
	applied, err := transaction.ApplyDeferredOwnership(ctx, plan)
	if err != nil {
		_ = deployment.restoreInstallSecrets(ctx, previous)
		return err
	}
	if err := deployment.waitHealthy(ctx, request.Endpoint); err != nil {
		rollbackErr := transaction.Rollback(ctx, plan)
		secretErr := deployment.restoreInstallSecrets(ctx, previous)
		return errors.Join(err, rollbackErr, secretErr)
	}
	if err := transaction.RecordOwnership(ctx, applied); err != nil {
		rollbackErr := transaction.Rollback(ctx, plan)
		secretErr := deployment.restoreInstallSecrets(ctx, previous)
		return errors.Join(err, rollbackErr, secretErr)
	}
	return nil
}

// PlanUninstall removes only the MLink-owned runtime and configuration. The
// persistent MemoryCore volume is deliberately outside the ordinary uninstall
// ChangeSet so memory data survives reinstall and upgrades.
func (deployment Deployment) PlanUninstall(ctx context.Context) (install.ChangeSet, error) {
	inspected, inspectErr := deployment.inspect(ctx)
	if inspectErr == nil && !inspected.compatible() {
		return install.ChangeSet{}, errors.New("MLink refuses to remove an unowned or drifted MemoryCore container")
	}
	if deployment.Target == nil || !filepath.IsAbs(deployment.ConfigPath) {
		return install.ChangeSet{}, errors.New("MemoryCore uninstall target and absolute configuration path are required")
	}
	resources := []install.DesiredResource{{
		OwnerID: "dev.mlink.memorycore.config", Target: deployment.ConfigPath, Action: install.ActionRemoveOwned,
		SemanticDiff: []install.SemanticDiff{{Path: "memorycore.config", Before: "MLink-owned", After: "removed"}},
	}}
	if inspectErr == nil {
		resources = append(resources, install.DesiredResource{
			OwnerID: "dev.mlink.memorycore.container", Target: "service:remove:" + MemoryCoreContainerName, Action: install.ActionService,
			Command:      []string{"docker", "rm", "-f", MemoryCoreContainerName},
			SemanticDiff: []install.SemanticDiff{{Path: "memorycore.container", Before: "MLink-owned", After: "removed; data volume retained"}},
		})
	}
	return install.BuildChangeSet(deployment.Target, resources)
}

func (deployment Deployment) validateInstallRequest(request lifecycle.BackendInstallRequest) error {
	if deployment.Target == nil || deployment.Ledger == nil || deployment.Secrets == nil || !filepath.IsAbs(deployment.ConfigPath) || !filepath.IsAbs(deployment.EnvPath) {
		return errors.New("MemoryCore install target, ledger, Keychain, and absolute paths are required")
	}
	endpoint, local, err := deploymentEndpoint(request.Endpoint)
	if err != nil || !local || strings.TrimRight(endpoint.String(), "/") != "http://127.0.0.1:8420" {
		return errors.New("official local MemoryCore install requires http://127.0.0.1:8420")
	}
	if err := validateLLMEndpoint(request.LLMBaseURL); err != nil {
		return errors.New("valid HTTPS or loopback memory LLM endpoint is required")
	}
	if request.ProviderID != providerID || strings.TrimSpace(request.LLMModel) == "" || len(request.GatewayToken) < 16 || len(request.LLMAPIKey) == 0 || len(request.GatewayToken) > 16<<10 || len(request.LLMAPIKey) > 16<<10 || bytes.ContainsAny(request.GatewayToken, "\r\n\x00") || bytes.ContainsAny(request.LLMAPIKey, "\r\n\x00") {
		return errors.New("valid protected MemoryCore and memory LLM inputs are required")
	}
	return nil
}

func validateLLMEndpoint(raw string) error {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
		return errors.New("invalid memory LLM endpoint")
	}
	hostname := strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
	address := net.ParseIP(hostname)
	local := hostname == "localhost" || address != nil && address.IsLoopback()
	if parsed.Scheme == "https" || local && parsed.Scheme == "http" {
		return nil
	}
	return errors.New("invalid memory LLM endpoint")
}

func renderGatewayConfig(request lifecycle.BackendInstallRequest) ([]byte, error) {
	tmpl, err := template.New("gateway").Funcs(template.FuncMap{"yaml": func(value string) string {
		encoded, _ := json.Marshal(value)
		return string(encoded)
	}}).Parse(gatewayConfigTemplate)
	if err != nil {
		return nil, err
	}
	var output bytes.Buffer
	if err := tmpl.Execute(&output, request); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func (deployment Deployment) dockerObjectExists(ctx context.Context, kind, name string) bool {
	runner := deployment.Runner
	if runner == nil {
		runner = install.LocalTarget{}
	}
	_, err := runner.Run(ctx, []string{"docker", kind, "inspect", name}, nil)
	return err == nil
}

type secretSnapshot struct {
	account string
	value   []byte
	existed bool
}

func (deployment Deployment) putInstallSecrets(ctx context.Context, request lifecycle.BackendInstallRequest) ([]secretSnapshot, error) {
	values := []struct {
		account string
		value   []byte
	}{{"connection/local/token", request.GatewayToken}, {"provider/tencentdb/llm-api-key", request.LLMAPIKey}}
	result := make([]secretSnapshot, 0, len(values))
	for _, item := range values {
		previous, err := deployment.Secrets.Get(ctx, item.account)
		snapshot := secretSnapshot{account: item.account}
		if err == nil {
			snapshot.value, snapshot.existed = previous, true
		} else if !errors.Is(err, fs.ErrNotExist) {
			wipeSecretSnapshots(result)
			return nil, err
		}
		result = append(result, snapshot)
		if err := deployment.Secrets.Put(ctx, item.account, item.value); err != nil {
			_ = deployment.restoreInstallSecrets(ctx, result)
			wipeSecretSnapshots(result)
			return nil, err
		}
	}
	return result, nil
}

func (deployment Deployment) restoreInstallSecrets(ctx context.Context, snapshots []secretSnapshot) error {
	var restoreErrors []error
	for index := len(snapshots) - 1; index >= 0; index-- {
		if snapshots[index].existed {
			restoreErrors = append(restoreErrors, deployment.Secrets.Put(ctx, snapshots[index].account, snapshots[index].value))
		} else {
			restoreErrors = append(restoreErrors, deployment.Secrets.Delete(ctx, snapshots[index].account))
		}
	}
	return errors.Join(restoreErrors...)
}

func wipeSecretSnapshots(values []secretSnapshot) {
	for index := range values {
		wipeBytes(values[index].value)
	}
}

func wipeBytes(value []byte) {
	for index := range value {
		value[index] = 0
	}
}

func (deployment Deployment) waitHealthy(ctx context.Context, endpoint string) error {
	timeout := deployment.HealthTimeout
	if timeout <= 0 {
		timeout = 90 * time.Second
	}
	interval := deployment.PollInterval
	if interval <= 0 {
		interval = 250 * time.Millisecond
	}
	deadline := time.Now().Add(timeout)
	for {
		_, compatible, err := deployment.health(ctx, endpoint)
		if err == nil && compatible {
			return nil
		}
		if time.Now().After(deadline) {
			return errors.New("MemoryCore did not become healthy before timeout")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(interval):
		}
	}
}
