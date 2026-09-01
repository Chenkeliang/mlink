package lifecycle

import (
	"context"
	"errors"
	"strings"
	"testing"

	"mlink/internal/config"
	"mlink/internal/install"
)

type fakeLifecycle struct{ id string }

func (value fakeLifecycle) Detect(context.Context, config.Connection) (BackendStatus, error) {
	return BackendStatus{ProviderID: value.id}, nil
}
func (fakeLifecycle) PlanInstall(context.Context, BackendInstallRequest) (install.ChangeSet, error) {
	return install.ChangeSet{PlanID: "plan_test"}, nil
}
func (fakeLifecycle) ApplyInstall(context.Context, string, BackendInstallRequest) error { return nil }

func TestRegistryRequiresUniqueExactProviderIDs(t *testing.T) {
	provider := fakeLifecycle{id: "dev.mlink.tencentdb"}
	registry, err := NewRegistry(Registration{ProviderID: provider.id, Lifecycle: provider})
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := registry.Get(provider.id)
	if err != nil || resolved == nil {
		t.Fatalf("resolved/error = %#v/%v", resolved, err)
	}
	if _, err := registry.Get("DEV.MLINK.TENCENTDB"); !errors.Is(err, ErrBackendUnavailable) {
		t.Fatalf("case-insensitive lookup error = %v", err)
	}
	if _, err := NewRegistry(
		Registration{ProviderID: provider.id, Lifecycle: provider},
		Registration{ProviderID: provider.id, Lifecycle: provider},
	); err == nil {
		t.Fatal("duplicate Provider registration accepted")
	}
}

func TestBackendInstallRequestNeverFormatsSecrets(t *testing.T) {
	request := BackendInstallRequest{
		ProviderID: "dev.mlink.tencentdb", Endpoint: "http://127.0.0.1:8420",
		GatewayToken: []byte("gateway-secret"), LLMBaseURL: "https://llm.example/v1", LLMModel: "model-a", LLMAPIKey: []byte("llm-secret"),
	}
	formatted := request.String() + " " + request.GoString()
	for _, secret := range []string{"gateway-secret", "llm-secret"} {
		if strings.Contains(formatted, secret) {
			t.Fatalf("formatted request leaked %q: %s", secret, formatted)
		}
	}
}

func TestBackendStatusValuesAreIndependent(t *testing.T) {
	first := BackendStatus{ProviderID: "provider", State: BackendReachable, Endpoint: "http://127.0.0.1:8420", Version: "0.1.0", Local: true, Installed: true}
	second := first
	second.State = BackendAbsent
	if first.State != BackendReachable {
		t.Fatalf("status aliasing = %#v", first)
	}
}
