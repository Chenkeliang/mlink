package cli

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"mlink/internal/app"
	"mlink/internal/install"
	"mlink/internal/provider/lifecycle"
)

type providerCLIApplication struct {
	*fakeApplication
	status            lifecycle.BackendStatus
	plan              install.ChangeSet
	request           lifecycle.BackendInstallRequest
	applyCalls        int
	controlLimit      int
	controlApplyCalls int
	bootstrapRequest  app.ControlPlaneBootstrapRequest
}

func (application *providerCLIApplication) ProviderStatus(context.Context) (lifecycle.BackendStatus, error) {
	return application.status, nil
}
func (application *providerCLIApplication) PlanProviderInstall(_ context.Context, request lifecycle.BackendInstallRequest) (install.ChangeSet, error) {
	application.request = cloneBackendRequest(request)
	return application.plan, nil
}
func (application *providerCLIApplication) ApplyProviderInstall(_ context.Context, _ string, request lifecycle.BackendInstallRequest) error {
	application.request = cloneBackendRequest(request)
	application.applyCalls++
	return nil
}

func cloneBackendRequest(request lifecycle.BackendInstallRequest) lifecycle.BackendInstallRequest {
	request.GatewayToken = append([]byte(nil), request.GatewayToken...)
	request.LLMAPIKey = append([]byte(nil), request.LLMAPIKey...)
	return request
}
func (application *providerCLIApplication) PlanControlPlaneProvision(_ context.Context, limit int) (install.ChangeSet, error) {
	application.controlLimit = limit
	return application.plan, nil
}
func (application *providerCLIApplication) ApplyControlPlaneProvision(_ context.Context, _ string, limit int) error {
	application.controlLimit = limit
	application.controlApplyCalls++
	return nil
}
func (application *providerCLIApplication) PlanControlPlaneBootstrap(_ context.Context, request app.ControlPlaneBootstrapRequest) (install.ChangeSet, error) {
	application.bootstrapRequest = request
	application.bootstrapRequest.GatewayToken = append([]byte(nil), request.GatewayToken...)
	return application.plan, nil
}
func (application *providerCLIApplication) ApplyControlPlaneBootstrap(_ context.Context, _ string, request app.ControlPlaneBootstrapRequest) error {
	application.bootstrapRequest = request
	application.bootstrapRequest.GatewayToken = append([]byte(nil), request.GatewayToken...)
	application.controlApplyCalls++
	return nil
}

func TestProviderStatusRendersStructuredBackendState(t *testing.T) {
	application := &providerCLIApplication{fakeApplication: &fakeApplication{}, status: lifecycle.BackendStatus{ProviderID: "dev.mlink.tencentdb", State: lifecycle.BackendAbsent, Endpoint: "http://127.0.0.1:8420", Local: true}}
	stdout := &bytes.Buffer{}
	code := Run(context.Background(), []string{"provider", "status", "--json"}, Dependencies{App: application, Stdout: stdout, Stderr: io.Discard})
	if code != 0 || !strings.Contains(stdout.String(), `"state": "absent"`) {
		t.Fatalf("code/output = %d/%s", code, stdout)
	}
}

func TestProviderInstallRequiresProtectedInputsAndExactPlan(t *testing.T) {
	application := &providerCLIApplication{fakeApplication: &fakeApplication{}, plan: install.ChangeSet{PlanID: "plan_provider", Operations: []install.Operation{{Target: "service:docker-run:tdai-memory-core", Action: install.ActionService}}}}
	secretJSON := `{"gateway_token":"gateway-secret-1234","llm_api_key":"llm-secret"}` + "\n"
	args := []string{"provider", "install", "tencentdb", "--endpoint", "http://127.0.0.1:8420", "--llm-base-url", "https://llm.example/v1", "--llm-model", "model-a", "--secrets-stdin", "--apply-plan", "plan_provider", "--yes"}
	code := Run(context.Background(), args, Dependencies{App: application, Stdin: strings.NewReader(secretJSON), Stdout: io.Discard, Stderr: io.Discard})
	if code != 0 || application.applyCalls != 1 || string(application.request.GatewayToken) != "gateway-secret-1234" || string(application.request.LLMAPIKey) != "llm-secret" {
		t.Fatalf("code/application = %d/%#v", code, application)
	}
	if code := Run(context.Background(), []string{"provider", "install", "tencentdb", "--endpoint", "http://127.0.0.1:8420"}, Dependencies{App: application, Stdin: strings.NewReader(""), Stdout: io.Discard, Stderr: io.Discard}); code != 2 {
		t.Fatalf("missing protected inputs code = %d", code)
	}
}

func TestControlPlaneProvisionRequiresExplicitCapacityAndPlan(t *testing.T) {
	application := &providerCLIApplication{fakeApplication: &fakeApplication{}, plan: install.ChangeSet{PlanID: "plan_control", Operations: []install.Operation{{Target: "control-plane:owner", Action: install.ActionService}}}}
	args := []string{"control-plane", "provision", "--dynamic-agent-limit", "500", "--apply-plan", "plan_control", "--yes"}
	code := Run(context.Background(), args, Dependencies{App: application, Stdout: io.Discard, Stderr: io.Discard})
	if code != 0 || application.controlApplyCalls != 1 || application.controlLimit != 500 {
		t.Fatalf("code/application = %d/%#v", code, application)
	}
}

func TestControlPlaneProvisionCanBootstrapExistingBackendWithProtectedToken(t *testing.T) {
	application := &providerCLIApplication{fakeApplication: &fakeApplication{}, plan: install.ChangeSet{PlanID: "plan_control"}}
	args := []string{
		"control-plane", "provision", "--dynamic-agent-limit", "500",
		"--endpoint", "https://memory.example.test", "--service-id", "team-a", "--installation-id", "installation-a", "--owner", "keliang",
		"--secrets-stdin", "--apply-plan", "plan_control", "--yes",
	}
	code := Run(context.Background(), args, Dependencies{
		App: application, Stdin: strings.NewReader(`{"gateway_token":"existing-gateway"}` + "\n"), Stdout: io.Discard, Stderr: io.Discard,
	})
	if code != 0 || application.controlApplyCalls != 1 || application.bootstrapRequest.Connection.ProviderConfig["base_url"] != "https://memory.example.test" ||
		application.bootstrapRequest.Connection.ProviderConfig["service_id"] != "team-a" || application.bootstrapRequest.Connection.TenantID != "installation-a" ||
		application.bootstrapRequest.OwnerSlug != "keliang" || string(application.bootstrapRequest.GatewayToken) != "existing-gateway" {
		t.Fatalf("code/bootstrap = %d/%#v", code, application.bootstrapRequest)
	}
}
