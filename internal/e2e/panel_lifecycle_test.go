package e2e

import (
	"bytes"
	"context"
	"io/fs"
	"testing"

	"mlink/internal/install"
	"mlink/internal/panel"
)

func TestPanelLifecyclePlanIsPinnedLoopbackAndProxyFree(t *testing.T) {
	runtimeTarget := &target{files: map[string]file{}}
	runtime := panel.Runtime{Runner: runtimeTarget, Target: runtimeTarget}
	plan, err := runtime.Plan(context.Background(), panel.Desired{
		SourceRoot: "/src/MemoryPanel", RegistryPath: "/Users/test/.mlink/panel/metadata-instances.json",
		HostAddress: "127.0.0.1", HostPort: 8125, ContainerPort: 8123, InstanceID: "default",
		InstanceName: "MLink Local", GatewayEndpoint: "http://host.docker.internal:8420",
	})
	if err != nil {
		t.Fatal(err)
	}
	rendered, _ := install.RenderJSON(plan)
	for _, forbidden := range [][]byte{[]byte("8096"), []byte("LLM_BASE_URL"), []byte("KNOWLEDGE_SERVICE_URL"), []byte("gateway-secret")} {
		if bytes.Contains(rendered, forbidden) {
			t.Fatalf("Panel plan contains %q: %s", forbidden, rendered)
		}
	}
	if len(plan.Operations) != 3 || plan.Operations[0].Mode.Perm() != fs.FileMode(0o600) {
		t.Fatalf("Panel plan = %#v", plan)
	}
}
