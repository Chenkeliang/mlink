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
	"os/exec"
	"path/filepath"
	"regexp"
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
	Layout        DeploymentLayout
}

type DeploymentLayout struct {
	ContainerName string
	VolumeName    string
	NetworkName   string
	HostAddress   string
	HostPort      int
}

var dockerObjectNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{2,127}$`)

func (deployment Deployment) resolvedLayout() DeploymentLayout {
	layout := deployment.Layout
	if layout == (DeploymentLayout{}) {
		return DeploymentLayout{
			ContainerName: MemoryCoreContainerName, VolumeName: MemoryCoreVolumeName,
			NetworkName: MemoryCoreNetworkName, HostAddress: "127.0.0.1", HostPort: 8420,
		}
	}
	return layout
}

func (layout DeploymentLayout) validate() error {
	if !dockerObjectNamePattern.MatchString(layout.ContainerName) || !dockerObjectNamePattern.MatchString(layout.VolumeName) ||
		!dockerObjectNamePattern.MatchString(layout.NetworkName) || layout.HostAddress != "127.0.0.1" ||
		layout.HostPort <= 1024 || layout.HostPort > 65535 {
		return errors.New("safe loopback MemoryCore deployment layout is required")
	}
	return nil
}

//go:embed templates/tdai-gateway.yaml.tmpl
var gatewayConfigTemplate string

func (deployment Deployment) Detect(ctx context.Context, connection config.Connection) (lifecycle.BackendStatus, error) {
	layout := deployment.resolvedLayout()
	if err := layout.validate(); err != nil {
		return lifecycle.BackendStatus{}, err
	}
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
			if inspected, inspectErr := deployment.inspect(ctx); inspectErr == nil && inspected.compatible(layout) {
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
	if !inspected.compatible(layout) {
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

func ValidateDeploymentEndpoint(raw string) (bool, error) {
	_, local, err := deploymentEndpoint(raw)
	return local, err
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
	output, err := runner.Run(ctx, []string{"docker", "inspect", deployment.resolvedLayout().ContainerName}, nil)
	if err != nil {
		return coreInspect{}, err
	}
	var values []coreInspect
	if json.Unmarshal(output, &values) != nil || len(values) != 1 {
		return coreInspect{}, errors.New("invalid MemoryCore container inspection")
	}
	return values[0], nil
}

func (value coreInspect) compatible(layout DeploymentLayout) bool {
	if value.Config.Image != MemoryCoreImageReference || value.Config.Labels["dev.mlink.component"] != "memory-core" {
		return false
	}
	volume := false
	for _, mount := range value.Mounts {
		volume = volume || mount.Name == layout.VolumeName && mount.Destination == "/data/tdai-memory"
	}
	bindings := value.HostConfig.PortBindings["8420/tcp"]
	port := false
	for _, binding := range bindings {
		port = port || binding.HostIP == layout.HostAddress && binding.HostPort == fmt.Sprint(layout.HostPort)
	}
	return volume && port
}

func (deployment Deployment) PlanInstall(ctx context.Context, request lifecycle.BackendInstallRequest) (install.ChangeSet, error) {
	if err := deployment.validateRuntimeRequest(request); err != nil {
		return install.ChangeSet{}, err
	}
	layout := deployment.resolvedLayout()
	if inspected, err := deployment.inspect(ctx); err == nil {
		if !inspected.compatible(layout) {
			return install.ChangeSet{}, errors.New("MLink refuses to replace an unowned or drifted MemoryCore container")
		}
		switch inspected.State.Status {
		case "exited", "created", "paused":
			return install.BuildChangeSet(deployment.Target, []install.DesiredResource{{
				OwnerID: "dev.mlink.memorycore.container", Target: "service:start:" + layout.ContainerName, Action: install.ActionService,
				Command: []string{"docker", "start", layout.ContainerName}, RollbackCommand: []string{"docker", "stop", layout.ContainerName},
				SemanticDiff: []install.SemanticDiff{{Path: "memorycore.container", Before: inspected.State.Status, After: "running; existing runtime credentials preserved"}},
			}})
		default:
			return install.ChangeSet{}, errors.New("MemoryCore container already exists")
		}
	}
	if err := deployment.validateInstallRequest(request); err != nil {
		return install.ChangeSet{}, err
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
	if !deployment.dockerObjectExists(ctx, "network", layout.NetworkName) {
		resources = append(resources, install.DesiredResource{
			OwnerID: "dev.mlink.memorycore.network", Target: "service:docker-network:" + layout.NetworkName, Action: install.ActionService,
			Command:         []string{"docker", "network", "create", "--label", "dev.mlink.component=memory-core", layout.NetworkName},
			RollbackCommand: []string{"docker", "network", "rm", layout.NetworkName},
			SemanticDiff:    []install.SemanticDiff{{Path: "memorycore.network", Before: "absent", After: layout.NetworkName}},
		})
	}
	if !deployment.dockerObjectExists(ctx, "volume", layout.VolumeName) {
		resources = append(resources, install.DesiredResource{
			OwnerID: "dev.mlink.memorycore.volume", Target: "service:docker-volume:" + layout.VolumeName, Action: install.ActionService,
			Command:      []string{"docker", "volume", "create", "--label", "dev.mlink.component=memory-core", layout.VolumeName},
			SemanticDiff: []install.SemanticDiff{{Path: "memorycore.data-volume", Before: "absent", After: layout.VolumeName + "; retained on rollback and ordinary uninstall"}},
		})
	}
	run := []string{
		"docker", "run", "-d", "--name", layout.ContainerName, "--restart", "unless-stopped",
		"--label", "dev.mlink.component=memory-core", "--network", layout.NetworkName,
		"-p", fmt.Sprintf("%s:%d:8420", layout.HostAddress, layout.HostPort), "-v", layout.VolumeName + ":/data/tdai-memory",
		"-v", deployment.ConfigPath + ":/data/config/tdai-gateway.yaml:ro", "--env-file", deployment.EnvPath,
		"-e", "TDAI_GATEWAY_PORT=8420", "-e", "TDAI_GATEWAY_HOST=0.0.0.0", "-e", "TDAI_DATA_DIR=/data/tdai-memory",
		MemoryCoreImageReference,
	}
	resources = append(resources, install.DesiredResource{
		OwnerID: "dev.mlink.memorycore.container", Target: "service:docker-run:" + layout.ContainerName, Action: install.ActionService,
		Command: run, RollbackCommand: []string{"docker", "rm", "-f", layout.ContainerName},
		SemanticDiff: []install.SemanticDiff{
			{Path: "memorycore.container", Before: "absent", After: layout.ContainerName},
			{Path: "memorycore.port", Before: "available", After: fmt.Sprintf("%s:%d:8420", layout.HostAddress, layout.HostPort)},
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
	if deployment.isRestartPlan(plan) {
		transaction := install.NewTransaction(deployment.Target, deployment.Ledger)
		applied, err := transaction.ApplyDeferredOwnership(ctx, plan)
		if err != nil {
			return err
		}
		if err := deployment.waitHealthy(ctx, request.Endpoint); err != nil {
			return errors.Join(err, transaction.Rollback(ctx, plan))
		}
		return transaction.RecordOwnership(ctx, applied)
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

func (deployment Deployment) isRestartPlan(plan install.ChangeSet) bool {
	return len(plan.Operations) == 1 && plan.Operations[0].Target == "service:start:"+deployment.resolvedLayout().ContainerName &&
		len(plan.Operations[0].Command) == 3 && plan.Operations[0].Command[0] == "docker" && plan.Operations[0].Command[1] == "start"
}

// PlanUninstall removes only the MLink-owned runtime and configuration. The
// persistent MemoryCore volume is deliberately outside the ordinary uninstall
// ChangeSet so memory data survives reinstall and upgrades.
func (deployment Deployment) PlanUninstall(ctx context.Context) (install.ChangeSet, error) {
	layout := deployment.resolvedLayout()
	if err := layout.validate(); err != nil {
		return install.ChangeSet{}, err
	}
	inspected, inspectErr := deployment.inspect(ctx)
	if inspectErr != nil && !dockerObjectNotFound(inspectErr) {
		return install.ChangeSet{}, errors.New("verify MemoryCore container ownership before uninstall")
	}
	if inspectErr == nil && !inspected.compatible(layout) {
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
			OwnerID: "dev.mlink.memorycore.container", Target: "service:remove:" + layout.ContainerName, Action: install.ActionService,
			Command:      []string{"docker", "rm", "-f", layout.ContainerName},
			SemanticDiff: []install.SemanticDiff{{Path: "memorycore.container", Before: "MLink-owned", After: "removed; data volume retained"}},
		})
	}
	return install.BuildChangeSet(deployment.Target, resources)
}

func dockerObjectNotFound(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		message += " " + strings.ToLower(string(exitErr.Stderr))
	}
	return strings.Contains(message, "not found") || strings.Contains(message, "no such object") || strings.Contains(message, "no such container")
}

func (deployment Deployment) validateInstallRequest(request lifecycle.BackendInstallRequest) error {
	if err := deployment.validateRuntimeRequest(request); err != nil {
		return err
	}
	if deployment.Secrets == nil || !filepath.IsAbs(deployment.ConfigPath) || !filepath.IsAbs(deployment.EnvPath) {
		return errors.New("MemoryCore install target, ledger, Keychain, and absolute paths are required")
	}
	if err := validateLLMEndpoint(request.LLMBaseURL); err != nil {
		return errors.New("valid HTTPS or loopback memory LLM endpoint is required")
	}
	if strings.TrimSpace(request.LLMModel) == "" || len(request.GatewayToken) < 16 || len(request.LLMAPIKey) == 0 || len(request.GatewayToken) > 16<<10 || len(request.LLMAPIKey) > 16<<10 || bytes.ContainsAny(request.GatewayToken, "\r\n\x00") || bytes.ContainsAny(request.LLMAPIKey, "\r\n\x00") {
		return errors.New("valid protected MemoryCore and memory LLM inputs are required")
	}
	return nil
}

func (deployment Deployment) validateRuntimeRequest(request lifecycle.BackendInstallRequest) error {
	layout := deployment.resolvedLayout()
	if err := layout.validate(); err != nil {
		return err
	}
	if deployment.Target == nil || deployment.Ledger == nil {
		return errors.New("MemoryCore install target and ledger are required")
	}
	endpoint, local, err := deploymentEndpoint(request.Endpoint)
	expectedEndpoint := fmt.Sprintf("http://%s:%d", layout.HostAddress, layout.HostPort)
	if err != nil || !local || strings.TrimRight(endpoint.String(), "/") != expectedEndpoint {
		return fmt.Errorf("official local MemoryCore install requires %s", expectedEndpoint)
	}
	if request.ProviderID != providerID {
		return errors.New("TencentDB MemoryCore provider identity is required")
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
