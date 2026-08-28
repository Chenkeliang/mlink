package hermes

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPlanProviderUsesOfficialLifecycleAndDelegatedIdentity(t *testing.T) {
	resources, err := PlanProvider("/home/test/.hermes", HTTPGrant{
		Endpoint: "http://192.168.139.1:8097",
		Token:    "test-hermes-token",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(resources) != 3 {
		t.Fatalf("resource count = %d", len(resources))
	}
	plugin := string(resources[1].Content)
	for _, method := range []string{"initialize", "is_available", "prefetch", "sync_turn", "get_tool_schemas", "shutdown"} {
		if !strings.Contains(plugin, "def "+method+"(") {
			t.Fatalf("missing official lifecycle method %s", method)
		}
	}
	if !strings.Contains(plugin, `subject = str(kwargs.get("user_id_alt") or kwargs.get("user_id") or "").strip()`) {
		t.Fatal("stable identity precedence missing")
	}
	if !strings.Contains(plugin, `if not subject:`) || !strings.Contains(plugin, `identity_missing`) {
		t.Fatal("missing identity rejection absent")
	}
	for _, forbidden := range []string{"ANTHROPIC_BASE_URL", "OPENAI_BASE_URL", "model.provider", "api_key"} {
		if strings.Contains(plugin, forbidden) {
			t.Fatalf("provider touches model configuration %q", forbidden)
		}
	}
	if !strings.Contains(plugin, `Queue(maxsize=128)`) || !strings.Contains(plugin, `timeout=0.8`) || !strings.Contains(plugin, `join(timeout=2.0)`) {
		t.Fatal("bounded queue or timeout contract missing")
	}
	if !strings.Contains(plugin, `return []`) {
		t.Fatal("provider exposes memory tools")
	}
	if strings.Contains(plugin, `"user_id":`) {
		t.Fatal("provider must not send a canonical user_id")
	}
}

func TestPlanProviderCreatesPrivateConfigAndIsDeterministic(t *testing.T) {
	grant := HTTPGrant{Endpoint: "http://192.168.139.1:8097", Token: "test-hermes-token"}
	first, err := PlanProvider("/home/test/.hermes", grant)
	if err != nil {
		t.Fatal(err)
	}
	second, err := PlanProvider("/home/test/.hermes", grant)
	if err != nil {
		t.Fatal(err)
	}
	for index := range first {
		if first[index].Target != second[index].Target || !bytes.Equal(first[index].Content, second[index].Content) {
			t.Fatal("provider plan is not deterministic")
		}
	}
	config := first[2]
	if config.Target != "/home/test/.hermes/mlink.json" || config.Mode.Perm() != 0o600 {
		t.Fatalf("config resource = %#v", config)
	}
	if !bytes.Contains(config.Content, []byte(`"token":"test-hermes-token"`)) {
		t.Fatal("Broker token missing from private adapter config")
	}
	if first[0].Target != "/home/test/.hermes/plugins/mlink/plugin.yaml" || first[1].Target != "/home/test/.hermes/plugins/mlink/__init__.py" {
		t.Fatalf("provider targets = %q, %q", first[0].Target, first[1].Target)
	}
	golden, err := os.ReadFile(filepath.Join("testdata", "plugin.golden.sha256"))
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(first[1].Content)
	if actual := fmt.Sprintf("%x\n", digest); actual != string(golden) {
		t.Fatalf("provider digest = %q, golden = %q", actual, golden)
	}
}

func TestPlanProviderRejectsUnsafeInputs(t *testing.T) {
	for name, testCase := range map[string]struct {
		home  string
		grant HTTPGrant
	}{
		"relative home":   {home: ".hermes", grant: HTTPGrant{Endpoint: "http://127.0.0.1:8097", Token: "x"}},
		"remote endpoint": {home: "/home/test/.hermes", grant: HTTPGrant{Endpoint: "https://memory.example.com", Token: "x"}},
		"missing token":   {home: "/home/test/.hermes", grant: HTTPGrant{Endpoint: "http://127.0.0.1:8097"}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := PlanProvider(testCase.home, testCase.grant); err == nil {
				t.Fatal("PlanProvider() error = nil")
			}
		})
	}
}
