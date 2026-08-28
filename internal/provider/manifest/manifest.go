package manifest

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"mlink/internal/connection"
)

const maxManifestBytes = 1 << 20

var allowedYAMLTags = map[string]struct{}{
	"!!bool": {},
	"!!int":  {},
	"!!map":  {},
	"!!null": {},
	"!!seq":  {},
	"!!str":  {},
}

type Manifest struct {
	APIVersion           string                          `yaml:"api_version"`
	ProviderID           string                          `yaml:"provider_id"`
	Name                 string                          `yaml:"name"`
	DisplayName          string                          `yaml:"display_name"`
	Version              string                          `yaml:"version"`
	Entrypoint           []string                        `yaml:"entrypoint"`
	Protocol             string                          `yaml:"protocol"`
	ProtocolVersions     []string                        `yaml:"protocol_versions"`
	BackendCompat        BackendCompatibility            `yaml:"backend_compat"`
	DeclaredCapabilities map[string]CapabilityDescriptor `yaml:"declared_capabilities"`
	ConfigSchema         string                          `yaml:"config_schema"`
	ExecutableSHA256     string                          `yaml:"executable_sha256"`
}

type BackendCompatibility struct {
	Product     string   `yaml:"product"`
	APIVersions []string `yaml:"api_versions"`
}

func Load(path string) (Manifest, error) {
	raw, err := readBounded(path, maxManifestBytes)
	if err != nil {
		return Manifest{}, err
	}
	var document yaml.Node
	if err := yaml.Unmarshal(raw, &document); err != nil {
		return Manifest{}, fmt.Errorf("parse provider manifest: %w", err)
	}
	if err := validateYAMLNode(&document); err != nil {
		return Manifest{}, err
	}

	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	var result Manifest
	if err := decoder.Decode(&result); err != nil {
		return Manifest{}, fmt.Errorf("decode provider manifest: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return Manifest{}, errors.New("provider manifest contains multiple YAML documents")
		}
		return Manifest{}, fmt.Errorf("decode provider manifest: %w", err)
	}

	if err := result.validate(); err != nil {
		return Manifest{}, err
	}
	realManifest, err := filepath.EvalSymlinks(path)
	if err != nil {
		return Manifest{}, fmt.Errorf("resolve provider manifest: %w", err)
	}
	if result.ConfigSchema != "" {
		result.ConfigSchema, err = resolvePackageFile(filepath.Dir(realManifest), result.ConfigSchema)
		if err != nil {
			return Manifest{}, fmt.Errorf("resolve config_schema: %w", err)
		}
	}
	return result, nil
}

func (m *Manifest) validate() error {
	if m.APIVersion != "mlink.provider/v1" {
		return errors.New("unsupported provider manifest api_version")
	}
	if err := connection.ValidateProviderRef(connection.ProviderRef{ID: m.ProviderID, Version: m.Version}); err != nil {
		return err
	}
	if strings.TrimSpace(m.Name) == "" || strings.TrimSpace(m.DisplayName) == "" {
		return errors.New("provider manifest requires name and display_name")
	}
	if m.Protocol != "stdio-jsonrpc" {
		return errors.New("unsupported provider protocol")
	}
	if len(m.ProtocolVersions) == 0 {
		return errors.New("provider manifest requires protocol_versions")
	}
	seenVersions := make(map[string]struct{}, len(m.ProtocolVersions))
	for _, version := range m.ProtocolVersions {
		if !validProtocolVersion(version) {
			return fmt.Errorf("invalid protocol version %q", version)
		}
		if _, exists := seenVersions[version]; exists {
			return fmt.Errorf("duplicate protocol version %q", version)
		}
		seenVersions[version] = struct{}{}
	}
	if len(m.Entrypoint) == 0 {
		return errors.New("provider manifest requires entrypoint argv")
	}
	for _, argument := range m.Entrypoint {
		if argument == "" {
			return errors.New("provider manifest entrypoint contains an empty argument")
		}
	}
	normalized, err := normalizeCapabilities(m.DeclaredCapabilities)
	if err != nil {
		return err
	}
	m.DeclaredCapabilities = normalized
	return nil
}

func readBounded(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open provider manifest: %w", err)
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read provider manifest: %w", err)
	}
	if int64(len(raw)) > limit {
		return nil, errors.New("provider manifest exceeds 1 MiB limit")
	}
	return raw, nil
}

func validateYAMLNode(node *yaml.Node) error {
	if node.Anchor != "" || node.Kind == yaml.AliasNode || node.Alias != nil {
		return errors.New("provider manifest YAML anchors and aliases are not allowed")
	}
	if node.Tag != "" {
		shortTag := node.ShortTag()
		if _, allowed := allowedYAMLTags[shortTag]; !allowed {
			return fmt.Errorf("provider manifest custom YAML tag %q is not allowed", node.Tag)
		}
	}
	for _, child := range node.Content {
		if err := validateYAMLNode(child); err != nil {
			return err
		}
	}
	return nil
}

func resolvePackageFile(packageDir, relativePath string) (string, error) {
	if filepath.IsAbs(relativePath) || strings.HasPrefix(relativePath, "~") {
		return "", errors.New("resource path must be relative to the provider package")
	}
	realPackageDir, err := filepath.EvalSymlinks(packageDir)
	if err != nil {
		return "", err
	}
	realResource, err := filepath.EvalSymlinks(filepath.Join(realPackageDir, filepath.Clean(relativePath)))
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(realPackageDir, realResource)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", errors.New("resource path escapes the provider package")
	}
	info, err := os.Stat(realResource)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("resource path is not a regular file")
	}
	return realResource, nil
}

func validProtocolVersion(value string) bool {
	parts := strings.Split(value, ".")
	if len(parts) != 2 {
		return false
	}
	for _, part := range parts {
		if part == "" || (len(part) > 1 && part[0] == '0') {
			return false
		}
		for i := range len(part) {
			if part[i] < '0' || part[i] > '9' {
				return false
			}
		}
	}
	return true
}
