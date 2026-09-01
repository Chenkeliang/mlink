package tencentdb

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"mlink/internal/config"
	"mlink/internal/install"
	"mlink/internal/provider/lifecycle"
)

const (
	MemoryCoreImageReference = "agentmemory/memory-core@sha256:9798254a8cc06276b7c5b3c19df49f136fae25d579564e1f01f9c4b9b8cd2d11"
	MemoryCoreContainerName  = "tdai-memory-core"
	MemoryCoreVolumeName     = "tdai-memory-core-data"
	MemoryCoreNetworkName    = "tdai-memory-stack"
)

type Deployment struct {
	Runner     install.CommandRunner
	HTTPClient *http.Client
}

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
