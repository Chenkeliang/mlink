package panel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"mlink/internal/install"
)

const (
	ContainerName  = "tdai-memory-hub"
	ImageReference = "agentmemory/memory-hub@sha256:7be68305b9ab279407584ffe44300605a5833df57a1730ca5bd41bbbe4b3f104"
	VolumeName     = "tdai-panel-data"
)

type Desired struct {
	RegistryPath             string
	HostAddress              string
	PanelHostPort            int
	KnowledgeHostPort        int
	InstanceID               string
	InstanceName             string
	GatewayEndpoint          string
	KnowledgePublicBaseURL   string
	KnowledgeLLMProxyBaseURL string
}

type Runtime struct {
	Runner     install.CommandRunner
	Target     install.Target
	HTTPClient *http.Client
}

type Status struct {
	ContainerPresent bool `json:"container_present"`
	PanelHealthy     bool `json:"panel_healthy"`
	KnowledgeHealthy bool `json:"knowledge_healthy"`
	InstanceVisible  bool `json:"instance_visible"`
}

func (runtime Runtime) Plan(ctx context.Context, desired Desired) (install.ChangeSet, error) {
	if err := validateDesired(desired); err != nil {
		return install.ChangeSet{}, err
	}
	if runtime.Target == nil || runtime.Runner == nil {
		return install.ChangeSet{}, errors.New("Memory Hub target and command runner are required")
	}
	pull := []string{"docker", "pull", ImageReference}
	volume := []string{"docker", "volume", "create", "--label", "dev.mlink.component=memory-hub", VolumeName}
	run := []string{
		"docker", "run", "-d", "--name", ContainerName, "--restart", "unless-stopped",
		"--label", "dev.mlink.component=memory-hub", "--add-host", "host.docker.internal:host-gateway",
		"-p", desired.HostAddress + ":" + strconv.Itoa(desired.PanelHostPort) + ":8125",
		"-p", desired.HostAddress + ":" + strconv.Itoa(desired.KnowledgeHostPort) + ":8424",
		"-e", "KNOWLEDGE_PUBLIC_BASE_URL=" + desired.KnowledgePublicBaseURL,
		"-e", "KNOWLEDGE_LLM_PROXY_BASE_URL=" + desired.KnowledgeLLMProxyBaseURL,
		"-e", "LLM_MODE=proxy", "-e", "KNOWLEDGE_LLM_BINDING_SYNC=true",
		"-e", "LOG_LEVEL=info", "-e", "LOG_FORMAT=json",
		"-v", VolumeName + ":/data/knowledge",
		"-v", desired.RegistryPath + ":/app/panel/config/metadata-instances.json:ro", ImageReference,
	}
	return install.BuildChangeSet(runtime.Target, []install.DesiredResource{
		{
			OwnerID: "dev.mlink.panel.registry", Target: desired.RegistryPath, Action: install.ActionCreate,
			Content: []byte("protected Panel registry generated during Apply\n"), Mode: 0o600,
			SemanticDiff: []install.SemanticDiff{{Path: "panel:instance-registry", Before: "absent or owned", After: "0600 read-only mount; Gateway Bearer redacted"}},
		},
		{
			OwnerID: "dev.mlink.hub.image", Target: "service:docker-pull:" + ImageReference,
			Action: install.ActionService, Command: pull,
			SemanticDiff: []install.SemanticDiff{{Path: "hub:image", Before: "absent or pinned", After: ImageReference}},
		},
		{
			OwnerID: "dev.mlink.hub.volume", Target: "service:docker-volume:" + VolumeName,
			Action: install.ActionService, Command: volume,
			SemanticDiff: []install.SemanticDiff{{Path: "hub:knowledge-volume", Before: "absent or owned", After: "persistent; retained on ordinary uninstall"}},
		},
		{
			OwnerID: "dev.mlink.hub.container", Target: "service:docker-run:" + ContainerName,
			Action: install.ActionService, Command: run,
			SemanticDiff: []install.SemanticDiff{
				{Path: "hub:panel", Before: "absent or owned", After: "http://127.0.0.1:8125"},
				{Path: "hub:knowledge", Before: "absent or owned", After: "http://127.0.0.1:8424; no assets bound"},
			},
		},
	})
}

func (runtime Runtime) Apply(ctx context.Context, planID string, desired Desired, gatewayToken []byte) error {
	plan, err := runtime.Plan(ctx, desired)
	if err != nil {
		return err
	}
	if plan.PlanID != planID {
		return fmt.Errorf("%w: Memory Hub plan changed", install.ErrPlanStale)
	}
	registry, err := RenderRegistry(RegistryInput{
		InstanceID: desired.InstanceID, InstanceName: desired.InstanceName,
		GatewayEndpoint: desired.GatewayEndpoint, GatewayToken: gatewayToken,
	})
	if err != nil {
		return err
	}
	defer wipe(registry)
	before, beforeMode, readErr := runtime.Target.Read(ctx, desired.RegistryPath)
	existed := readErr == nil
	if readErr != nil && !errors.Is(readErr, fs.ErrNotExist) {
		return readErr
	}
	defer wipe(before)
	if err := runtime.Target.WriteAtomic(ctx, desired.RegistryPath, registry, 0o600); err != nil {
		return fmt.Errorf("write protected Hub registry: %w", err)
	}
	restore := func() {
		if existed {
			_ = runtime.Target.WriteAtomic(context.Background(), desired.RegistryPath, before, beforeMode)
		} else {
			_ = runtime.Target.Remove(context.Background(), desired.RegistryPath)
		}
	}
	for _, operation := range plan.Operations {
		if operation.Action != install.ActionService {
			continue
		}
		if _, err := runtime.Runner.Run(ctx, operation.Command, nil); err != nil {
			restore()
			return fmt.Errorf("apply Memory Hub operation %q: %w", operation.Target, err)
		}
		if operation.Target == "service:docker-pull:"+ImageReference {
			output, inspectErr := runtime.Runner.Run(ctx, []string{"docker", "image", "inspect", "--format", "{{json .RepoDigests}}", ImageReference}, nil)
			if inspectErr != nil || !containsExactRepoDigest(output) {
				restore()
				return errors.New("pulled Memory Hub image digest does not match the approved official digest")
			}
		}
	}
	return nil
}

func (runtime Runtime) Status(ctx context.Context, desired Desired) (Status, error) {
	if err := validateDesired(desired); err != nil {
		return Status{}, err
	}
	if runtime.Runner == nil {
		return Status{}, errors.New("Memory Hub command runner is required")
	}
	if _, err := runtime.Runner.Run(ctx, []string{"docker", "inspect", ContainerName}, nil); err != nil {
		return Status{}, nil
	}
	client := runtime.HTTPClient
	if client == nil {
		client = &http.Client{}
	}
	status := Status{ContainerPresent: true}
	status.PanelHealthy = getOK(ctx, client, runtime.OpenURL(desired)+"/health", nil)
	instances := struct {
		Instances []struct {
			InstanceID string `json:"instance_id"`
		} `json:"instances"`
	}{}
	if status.PanelHealthy && getOK(ctx, client, runtime.OpenURL(desired)+"/api/v1/meta/instances", &instances) {
		for _, instance := range instances.Instances {
			status.InstanceVisible = status.InstanceVisible || instance.InstanceID == desired.InstanceID
		}
	}
	status.KnowledgeHealthy = getOK(ctx, client, "http://"+desired.HostAddress+":"+strconv.Itoa(desired.KnowledgeHostPort)+"/health", nil)
	return status, nil
}

func getOK(ctx context.Context, client *http.Client, endpoint string, destination any) bool {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return false
	}
	response, err := client.Do(request)
	if err != nil {
		return false
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return false
	}
	if destination == nil {
		return true
	}
	return json.NewDecoder(response.Body).Decode(destination) == nil
}

func (Runtime) OpenURL(desired Desired) string {
	return "http://" + desired.HostAddress + ":" + strconv.Itoa(desired.PanelHostPort)
}

func validateDesired(desired Desired) error {
	registry := filepath.Clean(desired.RegistryPath)
	if !filepath.IsAbs(registry) || filepath.Base(registry) != "metadata-instances.json" ||
		desired.HostAddress != "127.0.0.1" || desired.PanelHostPort != 8125 || desired.KnowledgeHostPort != 8424 ||
		!registryIDPattern.MatchString(desired.InstanceID) || strings.TrimSpace(desired.InstanceName) == "" ||
		desired.GatewayEndpoint != "http://host.docker.internal:8420" ||
		desired.KnowledgePublicBaseURL != "http://host.docker.internal:8424/v3" ||
		desired.KnowledgeLLMProxyBaseURL != "http://host.docker.internal:8420" {
		return errors.New("safe pinned Memory Hub configuration is required")
	}
	return nil
}

func containsExactRepoDigest(output []byte) bool {
	var values []string
	if json.Unmarshal(output, &values) != nil {
		return false
	}
	for _, value := range values {
		if value == ImageReference {
			return true
		}
	}
	return false
}

func wipe(value []byte) {
	for index := range value {
		value[index] = 0
	}
}
