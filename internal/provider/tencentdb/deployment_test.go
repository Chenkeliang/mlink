package tencentdb

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"mlink/internal/config"
	"mlink/internal/provider/lifecycle"
)

type deploymentRunner struct {
	inspect []byte
	err     error
	calls   [][]string
}

func (runner *deploymentRunner) Run(_ context.Context, args []string, _ io.Reader) ([]byte, error) {
	runner.calls = append(runner.calls, append([]string(nil), args...))
	return append([]byte(nil), runner.inspect...), runner.err
}

func deploymentConnection(baseURL string) config.Connection {
	return config.Connection{
		ID: "local", ProviderID: providerID, ProviderVersion: providerVersion, ConfigRevision: "rev-test",
		ProviderConfig: map[string]any{"base_url": baseURL, "service_id": "default", "timeout_ms": 5000},
	}
}

func healthyCore(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != "/health" {
			t.Fatalf("request = %s %s", request.Method, request.URL.Path)
		}
		response.Header().Set("Content-Type", "application/json")
		_, _ = response.Write([]byte(`{"status":"ok","version":"0.1.0","services":{"pipelineWorker":{"status":"ok"}}}`))
	}))
}

func ownedCoreInspect(state, image string) []byte {
	return []byte(`[{"Config":{"Image":"` + image + `","Labels":{"dev.mlink.component":"memory-core"}},"State":{"Status":"` + state + `"},"Mounts":[{"Name":"tdai-memory-core-data","Destination":"/data/tdai-memory"}],"HostConfig":{"PortBindings":{"8420/tcp":[{"HostIp":"127.0.0.1","HostPort":"8420"}]}}}]`)
}

func TestDeploymentDetectReachableCompatibleCore(t *testing.T) {
	server := healthyCore(t)
	defer server.Close()
	runner := &deploymentRunner{err: errors.New("container absent")}
	status, err := (Deployment{Runner: runner, HTTPClient: server.Client()}).Detect(context.Background(), deploymentConnection(server.URL))
	if err != nil {
		t.Fatal(err)
	}
	if status.State != lifecycle.BackendReachable || !status.Local || status.Installed || status.Version != "0.1.0" {
		t.Fatalf("status = %#v", status)
	}
}

func TestDeploymentDetectAbsentAndStoppedOwnedCore(t *testing.T) {
	for name, testCase := range map[string]struct {
		runner *deploymentRunner
		want   lifecycle.BackendState
	}{
		"absent":  {&deploymentRunner{err: errors.New("not found")}, lifecycle.BackendAbsent},
		"stopped": {&deploymentRunner{inspect: ownedCoreInspect("exited", MemoryCoreImageReference)}, lifecycle.BackendStopped},
	} {
		t.Run(name, func(t *testing.T) {
			status, err := (Deployment{Runner: testCase.runner, HTTPClient: &http.Client{Transport: failingTransport{}}}).Detect(context.Background(), deploymentConnection("http://127.0.0.1:8420"))
			if err != nil || status.State != testCase.want {
				t.Fatalf("status/error = %#v/%v", status, err)
			}
		})
	}
}

func TestDeploymentDetectRejectsIncompatibleListenerAndContainerDrift(t *testing.T) {
	badServer := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = response.Write([]byte(`{"status":"ready"}`))
	}))
	defer badServer.Close()
	status, err := (Deployment{Runner: &deploymentRunner{err: errors.New("absent")}, HTTPClient: badServer.Client()}).Detect(context.Background(), deploymentConnection(badServer.URL))
	if err != nil || status.State != lifecycle.BackendIncompatible {
		t.Fatalf("listener status/error = %#v/%v", status, err)
	}

	runner := &deploymentRunner{inspect: ownedCoreInspect("exited", "agentmemory/memory-core:unexpected")}
	status, err = (Deployment{Runner: runner, HTTPClient: &http.Client{Transport: failingTransport{}}}).Detect(context.Background(), deploymentConnection("http://127.0.0.1:8420"))
	if err != nil || status.State != lifecycle.BackendIncompatible {
		t.Fatalf("container status/error = %#v/%v", status, err)
	}
}

func TestDeploymentDetectClassifiesRemoteUnreachableWithoutDocker(t *testing.T) {
	runner := &deploymentRunner{}
	status, err := (Deployment{Runner: runner, HTTPClient: &http.Client{Transport: failingTransport{}}}).Detect(context.Background(), deploymentConnection("https://memory.example.test"))
	if err != nil || status.State != lifecycle.BackendRemoteUnreachable || status.Local || len(runner.calls) != 0 {
		t.Fatalf("status/error/calls = %#v/%v/%#v", status, err, runner.calls)
	}
}

type failingTransport struct{}

func (failingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("offline")
}

func TestDeploymentDetectRejectsUnsafeEndpointBeforeNetwork(t *testing.T) {
	for _, endpoint := range []string{"http://memory.example.test", "file:///tmp/core", "http://user:pass@127.0.0.1:8420"} {
		_, err := (Deployment{Runner: &deploymentRunner{}, HTTPClient: &http.Client{Transport: failingTransport{}}}).Detect(context.Background(), deploymentConnection(endpoint))
		if err == nil || !strings.Contains(err.Error(), "endpoint") {
			t.Fatalf("Detect(%q) error = %v", endpoint, err)
		}
	}
}
