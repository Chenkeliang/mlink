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
	ContainerName = "mlink-memory-panel"
	ImageName     = "mlink-memory-panel:a5dcbe6"
)

type Desired struct {
	SourceRoot      string
	RegistryPath    string
	HostAddress     string
	HostPort        int
	ContainerPort   int
	InstanceID      string
	InstanceName    string
	GatewayEndpoint string
}

type Runtime struct {
	Runner     install.CommandRunner
	Target     install.Target
	HTTPClient *http.Client
}

type Status struct {
	ContainerPresent bool
	Healthy          bool
}

func (runtime Runtime) Plan(ctx context.Context, desired Desired) (install.ChangeSet, error) {
	if err := validateDesired(desired); err != nil {
		return install.ChangeSet{}, err
	}
	if runtime.Target == nil || runtime.Runner == nil {
		return install.ChangeSet{}, errors.New("Panel target and command runner are required")
	}
	build := []string{
		"docker", "build", "--build-arg", "PANEL_UI=web", "-t", ImageName,
		"-f", filepath.Join(desired.SourceRoot, "docker", "local", "Dockerfile.local"), desired.SourceRoot,
	}
	run := []string{
		"docker", "run", "-d", "--name", ContainerName, "--restart", "unless-stopped",
		"--label", "dev.mlink.component=memory-panel", "--add-host", "host.docker.internal:host-gateway",
		"-p", desired.HostAddress + ":" + strconv.Itoa(desired.HostPort) + ":" + strconv.Itoa(desired.ContainerPort),
		"-e", "UI_DIST_DIR=./web/dist",
		"-e", "METADATA_INSTANCES_CONFIG=/app/config/metadata-instances.json",
		"-e", "KNOWLEDGE_LLM_BINDING_SYNC=false",
		"-e", "LOG_LEVEL=info", "-e", "LOG_FORMAT=json",
		"-v", desired.RegistryPath + ":/app/config/metadata-instances.json:ro", ImageName,
	}
	return install.BuildChangeSet(runtime.Target, []install.DesiredResource{
		{
			OwnerID: "dev.mlink.panel.registry", Target: desired.RegistryPath, Action: install.ActionCreate,
			Content: []byte("protected Panel registry generated during Apply\n"), Mode: 0o600,
			SemanticDiff: []install.SemanticDiff{{Path: "panel:instance-registry", Before: "absent or owned", After: "0600 read-only mount; Gateway Bearer redacted"}},
		},
		{
			OwnerID: "dev.mlink.panel.image", Target: "service:docker-build:" + ImageName,
			Action: install.ActionService, Command: build,
			SemanticDiff: []install.SemanticDiff{{Path: "panel:image", Before: "absent or pinned", After: ImageName}},
		},
		{
			OwnerID: "dev.mlink.panel.container", Target: "service:docker-run:" + ContainerName,
			Action: install.ActionService, Command: run,
			SemanticDiff: []install.SemanticDiff{{Path: "panel:listen", Before: "absent or owned", After: "http://127.0.0.1:8125"}},
		},
	})
}

func (runtime Runtime) Apply(ctx context.Context, planID string, desired Desired, gatewayToken []byte) error {
	plan, err := runtime.Plan(ctx, desired)
	if err != nil {
		return err
	}
	if plan.PlanID != planID {
		return fmt.Errorf("%w: Panel plan changed", install.ErrPlanStale)
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
		return fmt.Errorf("write protected Panel registry: %w", err)
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
			return fmt.Errorf("apply Panel operation %q: %w", operation.Target, err)
		}
	}
	return nil
}

func (runtime Runtime) Status(ctx context.Context, desired Desired) (Status, error) {
	if err := validateDesired(desired); err != nil {
		return Status{}, err
	}
	if runtime.Runner == nil {
		return Status{}, errors.New("Panel command runner is required")
	}
	if _, err := runtime.Runner.Run(ctx, []string{"docker", "inspect", ContainerName}, nil); err != nil {
		return Status{}, nil
	}
	client := runtime.HTTPClient
	if client == nil {
		client = &http.Client{}
	}
	if !getOK(ctx, client, runtime.OpenURL(desired)+"/health", nil) {
		return Status{ContainerPresent: true}, nil
	}
	instances := struct {
		Instances []struct {
			InstanceID string `json:"instance_id"`
		} `json:"instances"`
	}{}
	if !getOK(ctx, client, runtime.OpenURL(desired)+"/api/v1/meta/instances", &instances) {
		return Status{ContainerPresent: true}, nil
	}
	for _, instance := range instances.Instances {
		if instance.InstanceID == desired.InstanceID {
			return Status{ContainerPresent: true, Healthy: true}, nil
		}
	}
	return Status{ContainerPresent: true}, nil
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
	return "http://" + desired.HostAddress + ":" + strconv.Itoa(desired.HostPort)
}

func validateDesired(desired Desired) error {
	root := filepath.Clean(desired.SourceRoot)
	registry := filepath.Clean(desired.RegistryPath)
	if !filepath.IsAbs(root) || filepath.Base(root) != "MemoryPanel" || !filepath.IsAbs(registry) || filepath.Base(registry) != "metadata-instances.json" ||
		desired.HostAddress != "127.0.0.1" || desired.HostPort != 8125 || desired.ContainerPort != 8123 ||
		!registryIDPattern.MatchString(desired.InstanceID) || strings.TrimSpace(desired.InstanceName) == "" ||
		desired.GatewayEndpoint != "http://host.docker.internal:8420" {
		return errors.New("safe pinned Panel configuration is required")
	}
	return nil
}

func wipe(value []byte) {
	for index := range value {
		value[index] = 0
	}
}
