package tencentdb

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"mlink/internal/config"
	"mlink/internal/install"
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

type installRunner struct {
	commands [][]string
	started  bool
	failRun  bool
}

func (runner *installRunner) Run(_ context.Context, args []string, _ io.Reader) ([]byte, error) {
	runner.commands = append(runner.commands, append([]string(nil), args...))
	joined := strings.Join(args, " ")
	switch {
	case joined == "docker inspect "+MemoryCoreContainerName:
		return nil, errors.New("not found")
	case joined == "docker network inspect "+MemoryCoreNetworkName:
		return nil, errors.New("not found")
	case joined == "docker volume inspect "+MemoryCoreVolumeName:
		return nil, errors.New("not found")
	case strings.HasPrefix(joined, "docker run "):
		if runner.failRun {
			return nil, errors.New("run failed")
		}
		runner.started = true
		return []byte("container-id"), nil
	default:
		return []byte("ok"), nil
	}
}

type installHealthTransport struct{ runner *installRunner }

func (transport installHealthTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if !transport.runner.started {
		return nil, errors.New("offline")
	}
	body := io.NopCloser(strings.NewReader(`{"status":"ok","version":"0.1.0","services":{"pipelineWorker":{"status":"ok"}}}`))
	return &http.Response{StatusCode: http.StatusOK, Body: body, Header: make(http.Header), Request: request}, nil
}

type deploymentSecrets struct{ values map[string][]byte }

func (store *deploymentSecrets) Get(_ context.Context, account string) ([]byte, error) {
	value, ok := store.values[account]
	if !ok {
		return nil, fs.ErrNotExist
	}
	return append([]byte(nil), value...), nil
}
func (store *deploymentSecrets) Put(_ context.Context, account string, value []byte) error {
	store.values[account] = append([]byte(nil), value...)
	return nil
}
func (store *deploymentSecrets) Delete(_ context.Context, account string) error {
	delete(store.values, account)
	return nil
}

type deploymentLedger struct{ backups map[string]install.Backup }

func (ledger *deploymentLedger) SaveBackup(_ context.Context, backup install.Backup) error {
	if ledger.backups == nil {
		ledger.backups = make(map[string]install.Backup)
	}
	ledger.backups[backup.PlanID+"\x00"+backup.Target] = backup
	return nil
}
func (ledger *deploymentLedger) LoadBackup(_ context.Context, planID, target string) (install.Backup, error) {
	backup, ok := ledger.backups[planID+"\x00"+target]
	if !ok {
		return install.Backup{}, errors.New("missing backup")
	}
	return backup, nil
}
func (*deploymentLedger) RecordOwned(context.Context, install.OwnedResource) error { return nil }

func installRequest() lifecycle.BackendInstallRequest {
	return lifecycle.BackendInstallRequest{
		ProviderID: providerID, Endpoint: "http://127.0.0.1:8420", GatewayToken: []byte("gateway-secret-1234"),
		LLMBaseURL: "https://llm.example/v1", LLMModel: "model-a", LLMAPIKey: []byte("llm-secret"),
	}
}

func deploymentFixture(t *testing.T, runner *installRunner) Deployment {
	t.Helper()
	root := t.TempDir()
	return Deployment{
		Runner: runner, Target: install.LocalTarget{Runner: runner}, Ledger: &deploymentLedger{},
		Secrets:    &deploymentSecrets{values: map[string][]byte{}},
		ConfigPath: root + "/tdai-gateway.yaml", EnvPath: root + "/memorycore.env",
		HTTPClient:    &http.Client{Transport: installHealthTransport{runner: runner}},
		HealthTimeout: time.Second, PollInterval: time.Millisecond,
	}
}

func TestDeploymentInstallPlanIsPinnedSecretFreeAndZeroWrite(t *testing.T) {
	runner := &installRunner{}
	deployment := deploymentFixture(t, runner)
	request := installRequest()
	plan, err := deployment.PlanInstall(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	rendered, _ := install.RenderJSON(plan)
	for _, secret := range []string{"gateway-secret-1234", "llm-secret"} {
		if strings.Contains(string(rendered), secret) {
			t.Fatalf("Plan leaked %q: %s", secret, rendered)
		}
	}
	joined := string(rendered)
	for _, expected := range []string{MemoryCoreImageReference, "127.0.0.1:8420:8420", MemoryCoreVolumeName, MemoryCoreNetworkName} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("Plan missing %q: %s", expected, rendered)
		}
	}
	if _, _, err := deployment.Target.Read(context.Background(), deployment.ConfigPath); err == nil {
		t.Fatal("preview wrote gateway config")
	}
}

func TestDeploymentApplyInstallsAndDeletesTemporarySecretFile(t *testing.T) {
	runner := &installRunner{}
	deployment := deploymentFixture(t, runner)
	request := installRequest()
	plan, err := deployment.PlanInstall(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if err := deployment.ApplyInstall(context.Background(), plan.PlanID, request); err != nil {
		t.Fatal(err)
	}
	if !runner.started {
		t.Fatal("MemoryCore container did not start")
	}
	if _, _, err := deployment.Target.Read(context.Background(), deployment.EnvPath); err == nil {
		t.Fatal("temporary env file still exists")
	}
	configData, _, err := deployment.Target.Read(context.Background(), deployment.ConfigPath)
	if err != nil || strings.Contains(string(configData), "llm-secret") || !strings.Contains(string(configData), "model-a") {
		t.Fatalf("config/error = %s/%v", configData, err)
	}
	secrets := deployment.Secrets.(*deploymentSecrets)
	if string(secrets.values["connection/local/token"]) != "gateway-secret-1234" || string(secrets.values["provider/tencentdb/llm-api-key"]) != "llm-secret" {
		t.Fatal("Keychain values were not installed")
	}
	for _, command := range runner.commands {
		joined := strings.Join(command, " ")
		if strings.Contains(joined, "gateway-secret-1234") || strings.Contains(joined, "llm-secret") {
			t.Fatalf("secret leaked into argv: %q", joined)
		}
	}
}

func TestDeploymentApplyFailureRestoresSecretsAndConfiguration(t *testing.T) {
	runner := &installRunner{failRun: true}
	deployment := deploymentFixture(t, runner)
	secrets := deployment.Secrets.(*deploymentSecrets)
	secrets.values["connection/local/token"] = []byte("old-gateway")
	secrets.values["provider/tencentdb/llm-api-key"] = []byte("old-llm")
	request := installRequest()
	plan, _ := deployment.PlanInstall(context.Background(), request)
	if err := deployment.ApplyInstall(context.Background(), plan.PlanID, request); err == nil {
		t.Fatal("expected Docker run failure")
	}
	if string(secrets.values["connection/local/token"]) != "old-gateway" || string(secrets.values["provider/tencentdb/llm-api-key"]) != "old-llm" {
		t.Fatal("Keychain values were not restored")
	}
	if _, _, err := deployment.Target.Read(context.Background(), deployment.ConfigPath); err == nil {
		t.Fatal("failed install left gateway config")
	}
	if _, _, err := deployment.Target.Read(context.Background(), deployment.EnvPath); err == nil {
		t.Fatal("failed install left env file")
	}
}

func TestDeploymentDetectRejectsUnsafeEndpointBeforeNetwork(t *testing.T) {
	for _, endpoint := range []string{"http://memory.example.test", "file:///tmp/core", "http://user:pass@127.0.0.1:8420"} {
		_, err := (Deployment{Runner: &deploymentRunner{}, HTTPClient: &http.Client{Transport: failingTransport{}}}).Detect(context.Background(), deploymentConnection(endpoint))
		if err == nil || !strings.Contains(err.Error(), "endpoint") {
			t.Fatalf("Detect(%q) error = %v", endpoint, err)
		}
	}
}

func TestDeploymentUninstallRemovesOnlyOwnedContainerAndRetainsDataVolume(t *testing.T) {
	runner := &deploymentRunner{inspect: ownedCoreInspect("running", MemoryCoreImageReference)}
	root := t.TempDir()
	deployment := Deployment{
		Runner: runner, Target: install.LocalTarget{Runner: runner},
		ConfigPath: root + "/tdai-gateway.yaml",
	}
	if err := deployment.Target.WriteAtomic(context.Background(), deployment.ConfigPath, []byte("gateway: config\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	plan, err := deployment.PlanUninstall(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Operations) != 2 || plan.Operations[0].Target != deployment.ConfigPath ||
		strings.Join(plan.Operations[1].Command, " ") != "docker rm -f "+MemoryCoreContainerName {
		t.Fatalf("uninstall Plan = %#v", plan.Operations)
	}
	for _, operation := range plan.Operations {
		if strings.Contains(strings.Join(operation.Command, " "), "volume rm") || strings.Contains(operation.Target, "volume") {
			t.Fatalf("uninstall removes data volume: %#v", operation)
		}
	}
}

func TestDeploymentUninstallRefusesUnownedOrDriftedContainer(t *testing.T) {
	for name, inspect := range map[string][]byte{
		"wrong image": ownedCoreInspect("running", "agentmemory/memory-core:other"),
		"wrong label": bytes.ReplaceAll(ownedCoreInspect("running", MemoryCoreImageReference), []byte(`"memory-core"`), []byte(`"external"`)),
	} {
		t.Run(name, func(t *testing.T) {
			deployment := Deployment{Runner: &deploymentRunner{inspect: inspect}, Target: install.LocalTarget{}}
			if _, err := deployment.PlanUninstall(context.Background()); err == nil || !strings.Contains(err.Error(), "refuses") {
				t.Fatalf("PlanUninstall() error = %v", err)
			}
		})
	}
}
