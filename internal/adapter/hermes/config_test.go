package hermes

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestMergeConfigDisablesOnlyBuiltinMemoryAndSelectsMLink(t *testing.T) {
	before := readFixture(t, "config.yaml")
	after, ownership, err := MergeConfig(before)
	if err != nil {
		t.Fatal(err)
	}
	assertYAMLValue(t, after, "memory.memory_enabled", false)
	assertYAMLValue(t, after, "memory.user_profile_enabled", false)
	assertYAMLValue(t, after, "memory.provider", "mlink")
	assertYAMLValue(t, after, "group_sessions_per_user", false)
	assertYAMLValue(t, after, "thread_sessions_per_user", false)
	assertStringListContains(t, after, "agent.disabled_toolsets", "memory")

	wantPaths := []string{
		"group_sessions_per_user",
		"thread_sessions_per_user",
		"memory.memory_enabled",
		"memory.user_profile_enabled",
		"memory.provider",
		"agent.disabled_toolsets[memory]",
	}
	if !reflect.DeepEqual(ownership.Paths(), wantPaths) {
		t.Fatalf("ownership paths = %#v", ownership.Paths())
	}
	for _, path := range []string{"model", "agent.enabled_toolsets", "agent.reasoning_effort", "memory.memory_char_limit", "custom_section"} {
		if !reflect.DeepEqual(yamlValue(t, before, path), yamlValue(t, after, path)) {
			t.Fatalf("unowned path %q changed", path)
		}
	}
}

func TestMergeConfigMakesGroupsAndThreadsSharedAndRestoresExactly(t *testing.T) {
	before := []byte("group_sessions_per_user: true\nmemory:\n  provider: hy-memory\n")
	after, ownership, err := MergeConfig(before)
	if err != nil {
		t.Fatal(err)
	}
	assertYAMLValue(t, after, "group_sessions_per_user", false)
	assertYAMLValue(t, after, "thread_sessions_per_user", false)
	restored, err := RestoreConfig(after, ownership)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(restored, before) {
		t.Fatalf("restored = %q, want %q", restored, before)
	}
}

func TestMergeConfigIsIdempotentAndRestorePreservesLaterUnownedChanges(t *testing.T) {
	before := readFixture(t, "config.yaml")
	first, ownership, err := MergeConfig(before)
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := MergeConfig(first)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("second merge changed config")
	}

	var document yaml.Node
	if err := yaml.Unmarshal(first, &document); err != nil {
		t.Fatal(err)
	}
	setYAMLScalarForTest(t, &document, "custom_section.nested.keep", "changed-after-install")
	disabled := yamlNodeAt(t, &document, "agent.disabled_toolsets")
	disabled.Content = append(disabled.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "later-user-toolset"})
	changed, err := yaml.Marshal(&document)
	if err != nil {
		t.Fatal(err)
	}

	restored, err := RestoreConfig(changed, ownership)
	if err != nil {
		t.Fatal(err)
	}
	assertYAMLValue(t, restored, "memory.memory_enabled", true)
	assertYAMLValue(t, restored, "memory.user_profile_enabled", true)
	assertYAMLValue(t, restored, "memory.provider", "hy-memory")
	assertStringListMissing(t, restored, "agent.disabled_toolsets", "memory")
	assertStringListContains(t, restored, "agent.disabled_toolsets", "later-user-toolset")
	assertYAMLValue(t, restored, "custom_section.nested.keep", "changed-after-install")
}

func TestRestoreConfigRejectsManagedValueChangedAfterInstall(t *testing.T) {
	merged, ownership, err := MergeConfig(readFixture(t, "config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var document yaml.Node
	if err := yaml.Unmarshal(merged, &document); err != nil {
		t.Fatal(err)
	}
	setYAMLScalarForTest(t, &document, "memory.provider", "mem0")
	changed, err := yaml.Marshal(&document)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := RestoreConfig(changed, ownership); err == nil {
		t.Fatal("RestoreConfig() error = nil")
	}
}

func TestMergeAndRestoreHandlesMissingAndNullToolsets(t *testing.T) {
	for name, before := range map[string][]byte{
		"missing sections": []byte("custom: keep\n"),
		"null toolsets":    []byte("agent:\n  disabled_toolsets: null\ncustom: keep\n"),
	} {
		t.Run(name, func(t *testing.T) {
			merged, ownership, err := MergeConfig(before)
			if err != nil {
				t.Fatal(err)
			}
			assertStringListContains(t, merged, "agent.disabled_toolsets", "memory")
			restored, err := RestoreConfig(merged, ownership)
			if err != nil {
				t.Fatal(err)
			}
			var got, want map[string]any
			if err := yaml.Unmarshal(restored, &got); err != nil {
				t.Fatal(err)
			}
			if err := yaml.Unmarshal(before, &want); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("restored config = %#v, want %#v", got, want)
			}
		})
	}
}

func TestRestoreKeepsPreexistingDisabledMemoryToolset(t *testing.T) {
	before := []byte("agent:\n  disabled_toolsets: [memory, browser]\nmemory:\n  provider: hy-memory\n")
	merged, ownership, err := MergeConfig(before)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := RestoreConfig(merged, ownership)
	if err != nil {
		t.Fatal(err)
	}
	assertStringListContains(t, restored, "agent.disabled_toolsets", "memory")
}

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func assertYAMLValue(t *testing.T, data []byte, path string, want any) {
	t.Helper()
	if got := yamlValue(t, data, path); !reflect.DeepEqual(got, want) {
		t.Fatalf("%s = %#v, want %#v", path, got, want)
	}
}

func assertStringListContains(t *testing.T, data []byte, path, want string) {
	t.Helper()
	values, ok := yamlValue(t, data, path).([]any)
	if !ok {
		t.Fatalf("%s is not a list", path)
	}
	for _, value := range values {
		if value == want {
			return
		}
	}
	t.Fatalf("%s does not contain %q", path, want)
}

func assertStringListMissing(t *testing.T, data []byte, path, unwanted string) {
	t.Helper()
	values, ok := yamlValue(t, data, path).([]any)
	if !ok {
		t.Fatalf("%s is not a list", path)
	}
	for _, value := range values {
		if value == unwanted {
			t.Fatalf("%s still contains %q", path, unwanted)
		}
	}
}

func yamlValue(t *testing.T, data []byte, path string) any {
	t.Helper()
	var value map[string]any
	if err := yaml.Unmarshal(data, &value); err != nil {
		t.Fatal(err)
	}
	current := any(value)
	for _, part := range splitYAMLPath(path) {
		mapping, ok := current.(map[string]any)
		if !ok {
			t.Fatalf("%s does not resolve through a mapping", path)
		}
		current = mapping[part]
	}
	return current
}

func setYAMLScalarForTest(t *testing.T, document *yaml.Node, path, value string) {
	t.Helper()
	node := yamlNodeAt(t, document, path)
	node.Kind = yaml.ScalarNode
	node.Tag = "!!str"
	node.Value = value
}

func yamlNodeAt(t *testing.T, document *yaml.Node, path string) *yaml.Node {
	t.Helper()
	node := document.Content[0]
	for _, part := range splitYAMLPath(path) {
		found := false
		for index := 0; index+1 < len(node.Content); index += 2 {
			if node.Content[index].Value == part {
				node = node.Content[index+1]
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("missing YAML path %q", path)
		}
	}
	return node
}

func splitYAMLPath(path string) []string {
	var parts []string
	for len(path) > 0 {
		index := bytes.IndexByte([]byte(path), '.')
		if index < 0 {
			return append(parts, path)
		}
		parts = append(parts, path[:index])
		path = path[index+1:]
	}
	return parts
}
