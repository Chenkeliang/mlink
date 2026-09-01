package panel

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"strings"
	"testing"

	"mlink/internal/install"
)

type runnerCall struct {
	args  []string
	input []byte
}

type fakeRunner struct {
	calls         []runnerCall
	inspectOutput []byte
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func (runner *fakeRunner) Run(_ context.Context, args []string, input io.Reader) ([]byte, error) {
	var data []byte
	if input != nil {
		data, _ = io.ReadAll(input)
	}
	runner.calls = append(runner.calls, runnerCall{args: append([]string(nil), args...), input: data})
	if strings.Join(args, " ") == "docker image inspect --format {{json .RepoDigests}} "+ImageReference {
		return append([]byte(nil), runner.inspectOutput...), nil
	}
	return nil, nil
}

type memoryTarget struct {
	files map[string][]byte
	modes map[string]fs.FileMode
}

func (target *memoryTarget) Read(_ context.Context, path string) ([]byte, fs.FileMode, error) {
	data, exists := target.files[path]
	if !exists {
		return nil, 0, fs.ErrNotExist
	}
	return append([]byte(nil), data...), target.modes[path], nil
}
func (target *memoryTarget) WriteAtomic(_ context.Context, path string, data []byte, mode fs.FileMode) error {
	if target.files == nil {
		target.files = map[string][]byte{}
	}
	if target.modes == nil {
		target.modes = map[string]fs.FileMode{}
	}
	target.files[path] = append([]byte(nil), data...)
	target.modes[path] = mode
	return nil
}
func (target *memoryTarget) Remove(_ context.Context, path string) error {
	delete(target.files, path)
	return nil
}
func (*memoryTarget) Run(context.Context, []string, io.Reader) ([]byte, error) {
	return nil, errors.New("unused")
}

func TestPlanUsesDigestPinnedOfficialHubAndContainsNoSecrets(t *testing.T) {
	desired := fixtureDesired()
	runtime := Runtime{Runner: &fakeRunner{}, Target: &memoryTarget{files: map[string][]byte{}, modes: map[string]fs.FileMode{}}}
	plan, err := runtime.Plan(context.Background(), desired)
	if err != nil || len(plan.Operations) != 4 {
		t.Fatalf("plan = %#v, %v", plan, err)
	}
	rendered, _ := install.RenderJSON(plan)
	for _, forbidden := range []string{"gateway-secret", "8096", "LLM_API_KEY", "LLM_BASE_URL", "REMOTE_INSTANCE_PROXY_URL", "proxy_endpoint", "ANTHROPIC_BASE_URL", "OPENAI_BASE_URL"} {
		if bytes.Contains(rendered, []byte(forbidden)) {
			t.Fatalf("plan contains %q: %s", forbidden, rendered)
		}
	}
	commands := make([]string, 0, 3)
	for _, operation := range plan.Operations {
		if len(operation.Command) != 0 {
			commands = append(commands, strings.Join(operation.Command, " "))
		}
	}
	wantPull := "docker pull " + ImageReference
	wantVolume := "docker volume create --label dev.mlink.component=memory-hub " + VolumeName
	wantRun := "docker run -d --name tdai-memory-hub --restart unless-stopped --label dev.mlink.component=memory-hub --add-host host.docker.internal:host-gateway -p 127.0.0.1:8125:8125 -p 127.0.0.1:8424:8424 -e KNOWLEDGE_PUBLIC_BASE_URL=http://host.docker.internal:8424/v3 -e KNOWLEDGE_LLM_PROXY_BASE_URL=http://host.docker.internal:8420 -e LLM_MODE=proxy -e KNOWLEDGE_LLM_BINDING_SYNC=true -e LOG_LEVEL=info -e LOG_FORMAT=json -v tdai-panel-data:/data/knowledge -v /Users/test/.mlink/panel/metadata-instances.json:/app/panel/config/metadata-instances.json:ro " + ImageReference
	if len(commands) != 3 || commands[0] != wantPull || commands[1] != wantVolume || commands[2] != wantRun {
		t.Fatalf("commands = %#v", commands)
	}
}

func TestApplyWritesPrivateRegistryAndRunsExactPlan(t *testing.T) {
	desired := fixtureDesired()
	runner := &fakeRunner{inspectOutput: []byte(`["agentmemory/memory-hub@sha256:7be68305b9ab279407584ffe44300605a5833df57a1730ca5bd41bbbe4b3f104"]`)}
	target := &memoryTarget{files: map[string][]byte{}, modes: map[string]fs.FileMode{}}
	runtime := Runtime{Runner: runner, Target: target}
	plan, err := runtime.Plan(context.Background(), desired)
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.Apply(context.Background(), plan.PlanID, desired, []byte("gateway-secret")); err != nil {
		t.Fatal(err)
	}
	data := target.files[desired.RegistryPath]
	if !bytes.Contains(data, []byte("gateway-secret")) || target.modes[desired.RegistryPath].Perm() != 0o600 {
		t.Fatalf("registry mode/content = %o/%q", target.modes[desired.RegistryPath], data)
	}
	if len(runner.calls) != 4 {
		t.Fatalf("runner calls = %#v", runner.calls)
	}
}

func TestApplyRejectsUnexpectedOfficialImageDigestBeforeVolumeOrContainer(t *testing.T) {
	desired := fixtureDesired()
	runner := &fakeRunner{inspectOutput: []byte(`["agentmemory/memory-hub@sha256:unexpected"]`)}
	target := &memoryTarget{files: map[string][]byte{}, modes: map[string]fs.FileMode{}}
	runtime := Runtime{Runner: runner, Target: target}
	plan, err := runtime.Plan(context.Background(), desired)
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.Apply(context.Background(), plan.PlanID, desired, []byte("gateway-secret")); err == nil {
		t.Fatal("Apply() digest error = nil")
	}
	if _, exists := target.files[desired.RegistryPath]; exists || len(runner.calls) != 2 {
		t.Fatalf("registry/calls = %t/%#v", exists, runner.calls)
	}
}

func TestStatusRequiresPanelKnowledgeAndPublicInstanceListing(t *testing.T) {
	desired := fixtureDesired()
	runner := &fakeRunner{}
	paths := []string{}
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		paths = append(paths, request.URL.Port()+request.URL.Path)
		body := `{"status":"ok"}`
		if request.URL.Path == "/api/v1/meta/instances" {
			body = `{"instances":[{"instance_id":"default","name":"MLink Local","gateway_endpoint":"http://host.docker.internal:8420"}]}`
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}
	runtime := Runtime{Runner: runner, HTTPClient: client}

	status, err := runtime.Status(context.Background(), desired)
	if err != nil {
		t.Fatal(err)
	}
	if !status.ContainerPresent || !status.PanelHealthy || !status.KnowledgeHealthy || !status.InstanceVisible {
		t.Fatalf("status = %#v", status)
	}
	if strings.Join(paths, ",") != "8125/health,8125/api/v1/meta/instances,8424/health" {
		t.Fatalf("paths = %#v", paths)
	}
}

func TestStatusRejectsListingWithoutDesiredInstance(t *testing.T) {
	desired := fixtureDesired()
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		body := `{"status":"ok"}`
		if request.URL.Path == "/api/v1/meta/instances" {
			body = `{"instances":[{"instance_id":"other"}]}`
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}
	runtime := Runtime{Runner: &fakeRunner{}, HTTPClient: client}

	status, err := runtime.Status(context.Background(), desired)
	if err != nil {
		t.Fatal(err)
	}
	if status.InstanceVisible {
		t.Fatalf("status = %#v", status)
	}
}

func fixtureDesired() Desired {
	return Desired{
		RegistryPath: "/Users/test/.mlink/panel/metadata-instances.json",
		HostAddress:  "127.0.0.1", PanelHostPort: 8125, KnowledgeHostPort: 8424,
		InstanceID: "default", InstanceName: "MLink Local", GatewayEndpoint: "http://host.docker.internal:8420",
		KnowledgePublicBaseURL: "http://host.docker.internal:8424/v3", KnowledgeLLMProxyBaseURL: "http://host.docker.internal:8420",
	}
}
