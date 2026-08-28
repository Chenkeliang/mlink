package pi

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPlanExtensionUsesOfficialSettledLifecycle(t *testing.T) {
	resource, err := PlanExtension("/Users/test/.local/bin/mlink", "/Users/test/.mlink/run/mlink.sock")
	if err != nil {
		t.Fatal(err)
	}
	if resource.Target != "/Users/test/.pi/agent/extensions/mlink.ts" {
		t.Fatalf("target = %q", resource.Target)
	}
	text := string(resource.Content)
	for _, event := range []string{"session_start", "before_agent_start", "agent_end", "agent_settled", "session_shutdown"} {
		if !strings.Contains(text, `pi.on("`+event+`"`) {
			t.Fatalf("missing %s", event)
		}
	}
	for _, forbidden := range []string{"modelProvider", "baseURL", "apiKey", "toolResult", "systemPrompt"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("extension touches forbidden input/config %q", forbidden)
		}
	}
	if !strings.Contains(text, `pi.on("agent_settled"`) || !strings.Contains(text, `path: "/v1/turns"`) {
		t.Fatal("final turn is not committed from agent_settled")
	}
	if strings.Index(text, `pi.on("agent_settled"`) > strings.Index(text, `path: "/v1/turns"`) {
		t.Fatal("turn submission appears outside agent_settled")
	}
}

func TestPlanExtensionIsDeterministicAndMatchesGolden(t *testing.T) {
	first, err := PlanExtension("/Users/test/.local/bin/mlink", "/Users/test/.mlink/run/mlink.sock")
	if err != nil {
		t.Fatal(err)
	}
	second, err := PlanExtension("/Users/test/.local/bin/mlink", "/Users/test/.mlink/run/mlink.sock")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first.Content, second.Content) {
		t.Fatal("render is not deterministic")
	}
	golden, err := os.ReadFile(filepath.Join("testdata", "mlink.ts.golden"))
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(first.Content)
	actual := []byte(fmt.Sprintf("%x\n", digest))
	if !bytes.Equal(actual, golden) {
		t.Fatalf("render digest = %s, golden = %s", actual, golden)
	}
}

func TestRemoveOwnedExtensionTargetsOnlyMLinkFile(t *testing.T) {
	resource, err := RemoveOwnedExtension("/Users/test/.local/bin/mlink")
	if err != nil {
		t.Fatal(err)
	}
	if resource.Target != "/Users/test/.pi/agent/extensions/mlink.ts" || resource.Action != "remove_owned" {
		t.Fatalf("resource = %#v", resource)
	}
}
