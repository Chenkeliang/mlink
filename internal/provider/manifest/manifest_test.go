package manifest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadValidManifest(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.schema.json"), []byte(`{"type":"object"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	path := writeManifest(t, dir, validManifestYAML())

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got.APIVersion != "mlink.provider/v1" || got.Protocol != "stdio-jsonrpc" {
		t.Fatalf("manifest identity = %#v", got)
	}
	if got.ProviderID != "dev.mlink.tencentdb" || got.Version != "0.1.0" {
		t.Fatalf("provider = %s@%s", got.ProviderID, got.Version)
	}
	if got.DeclaredCapabilities["capture_turn"].MaxInFlight != 4 {
		t.Fatalf("capture max_in_flight = %d, want normalized default 4", got.DeclaredCapabilities["capture_turn"].MaxInFlight)
	}
	if !filepath.IsAbs(got.ConfigSchema) || filepath.Base(got.ConfigSchema) != "config.schema.json" {
		t.Fatalf("ConfigSchema = %q, want resolved absolute path", got.ConfigSchema)
	}
}

func TestLoadRejectsInvalidManifest(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(string) string
	}{
		{name: "unknown field", mutate: func(value string) string { return value + "unknown: true\n" }},
		{name: "duplicate protocol", mutate: func(value string) string {
			return strings.Replace(value, `protocol_versions: ["1.0"]`, `protocol_versions: ["1.0", "1.0"]`, 1)
		}},
		{name: "scalar entrypoint", mutate: func(value string) string {
			return strings.Replace(value, `entrypoint: ["mlink", "provider", "run", "tencentdb"]`, `entrypoint: "mlink provider run tencentdb"`, 1)
		}},
		{name: "empty entrypoint", mutate: func(value string) string {
			return strings.Replace(value, `entrypoint: ["mlink", "provider", "run", "tencentdb"]`, `entrypoint: []`, 1)
		}},
		{name: "anchor and alias", mutate: func(value string) string {
			return strings.Replace(value, "name: tencentdb\ndisplay_name: TencentDB MemoryCore", "name: &provider_name tencentdb\ndisplay_name: *provider_name", 1)
		}},
		{name: "custom tag", mutate: func(value string) string {
			return strings.Replace(value, "name: tencentdb", "name: !unsafe tencentdb", 1)
		}},
		{name: "missing capture roles", mutate: func(value string) string {
			return strings.Replace(value, "    roles: [user, assistant]\n", "", 1)
		}},
		{name: "invalid provider id", mutate: func(value string) string {
			return strings.Replace(value, "provider_id: dev.mlink.tencentdb", "provider_id: tencentdb", 1)
		}},
		{name: "invalid version", mutate: func(value string) string {
			return strings.Replace(value, "version: 0.1.0", "version: v0.1", 1)
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "config.schema.json"), []byte(`{}`), 0o600); err != nil {
				t.Fatal(err)
			}
			path := writeManifest(t, dir, tt.mutate(validManifestYAML()))
			if _, err := Load(path); err == nil {
				t.Fatal("Load() error = nil, want validation error")
			}
		})
	}
}

func TestLoadRejectsOversizedManifest(t *testing.T) {
	dir := t.TempDir()
	path := writeManifest(t, dir, strings.Repeat("x", maxManifestBytes+1))
	if _, err := Load(path); err == nil {
		t.Fatal("Load() error = nil, want size error")
	}
}

func TestLoadRejectsConfigSchemaSymlinkOutsidePackage(t *testing.T) {
	outside := t.TempDir()
	outsideSchema := filepath.Join(outside, "outside.json")
	if err := os.WriteFile(outsideSchema, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.Symlink(outsideSchema, filepath.Join(dir, "config.schema.json")); err != nil {
		t.Fatal(err)
	}
	path := writeManifest(t, dir, validManifestYAML())
	if _, err := Load(path); err == nil {
		t.Fatal("Load() error = nil, want symlink escape error")
	}
}

func TestLoadPreservesEntrypointArgumentsWithoutExpansion(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.schema.json"), []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	yaml := strings.Replace(
		validManifestYAML(),
		`entrypoint: ["mlink", "provider", "run", "tencentdb"]`,
		`entrypoint: ["provider", "$(touch /tmp/mlink-pwn)", "*.txt", ";"]`,
		1,
	)
	got, err := Load(writeManifest(t, dir, yaml))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	want := []string{"provider", "$(touch /tmp/mlink-pwn)", "*.txt", ";"}
	if strings.Join(got.Entrypoint, "|") != strings.Join(want, "|") {
		t.Fatalf("Entrypoint = %#v, want literal %#v", got.Entrypoint, want)
	}
}

func writeManifest(t *testing.T, dir, content string) string {
	t.Helper()
	path := filepath.Join(dir, "provider.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func validManifestYAML() string {
	return `api_version: mlink.provider/v1
provider_id: dev.mlink.tencentdb
name: tencentdb
display_name: TencentDB MemoryCore
version: 0.1.0
entrypoint: ["mlink", "provider", "run", "tencentdb"]
protocol: stdio-jsonrpc
protocol_versions: ["1.0"]
backend_compat:
  product: tencentdb-memorycore
  api_versions: ["v3"]
declared_capabilities:
  capture_turn:
    version: 1
    roles: [user, assistant]
    max_request_bytes: 1048576
    ordering: turn
  recall:
    version: 1
    scopes: [user, agent]
    max_request_bytes: 262144
    max_result_items: 20
    max_in_flight: 8
  health:
    version: 1
    max_in_flight: 2
config_schema: config.schema.json
executable_sha256: bundled
`
}
