package hermes

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"path/filepath"
	"strings"

	"mlink/internal/install"
)

//go:embed templates/plugin.yaml.tmpl
var pluginManifest []byte

//go:embed templates/__init__.py.tmpl
var providerModule []byte

type HTTPGrant struct {
	Endpoint string `json:"endpoint"`
	Token    string `json:"token"`
}

func PlanProvider(hermesHome string, grant HTTPGrant) ([]install.DesiredResource, error) {
	home := filepath.Clean(hermesHome)
	if !filepath.IsAbs(home) || home == string(filepath.Separator) {
		return nil, errors.New("Hermes home must be an absolute non-root path")
	}
	if err := validateBrokerEndpoint(grant.Endpoint); err != nil {
		return nil, err
	}
	if strings.TrimSpace(grant.Token) == "" {
		return nil, errors.New("Hermes Broker token is required")
	}
	config, err := json.Marshal(grant)
	if err != nil {
		return nil, fmt.Errorf("render Hermes Broker config: %w", err)
	}
	config = append(config, '\n')
	providerDirectory := filepath.Join(home, "plugins", "mlink")
	return []install.DesiredResource{
		{
			OwnerID: "dev.mlink.adapter.hermes",
			Target:  filepath.Join(providerDirectory, "plugin.yaml"),
			Content: append([]byte(nil), pluginManifest...),
			Mode:    0o600,
			SemanticDiff: []install.SemanticDiff{{
				Path: "memory-provider:mlink-manifest", Before: "absent or owned", After: "official Hermes user provider manifest",
			}},
		},
		{
			OwnerID: "dev.mlink.adapter.hermes",
			Target:  filepath.Join(providerDirectory, "__init__.py"),
			Content: append([]byte(nil), providerModule...),
			Mode:    0o600,
			SemanticDiff: []install.SemanticDiff{{
				Path: "memory-provider:mlink", Before: "absent or owned", After: "official Hermes MemoryProvider lifecycle bridge",
			}},
		},
		{
			OwnerID: "dev.mlink.adapter.hermes",
			Target:  filepath.Join(home, "mlink.json"),
			Content: config,
			Mode:    0o600,
			SemanticDiff: []install.SemanticDiff{{
				Path: "adapter:hermes-broker-grant", Before: "absent or owned", After: "private delegated HTTP grant",
			}},
		},
	}, nil
}

func RemoveOwnedProvider(hermesHome string) ([]install.DesiredResource, error) {
	home := filepath.Clean(hermesHome)
	if !filepath.IsAbs(home) || home == string(filepath.Separator) {
		return nil, errors.New("Hermes home must be an absolute non-root path")
	}
	providerDirectory := filepath.Join(home, "plugins", "mlink")
	return []install.DesiredResource{
		{OwnerID: "dev.mlink.adapter.hermes", Target: filepath.Join(providerDirectory, "plugin.yaml"), Action: install.ActionRemoveOwned},
		{OwnerID: "dev.mlink.adapter.hermes", Target: filepath.Join(providerDirectory, "__init__.py"), Action: install.ActionRemoveOwned},
		{OwnerID: "dev.mlink.adapter.hermes", Target: filepath.Join(home, "mlink.json"), Action: install.ActionRemoveOwned},
	}, nil
}

func validateBrokerEndpoint(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "http" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Path != "" {
		return errors.New("Hermes Broker endpoint must be a local HTTP origin")
	}
	hostname := strings.ToLower(parsed.Hostname())
	if hostname == "localhost" || hostname == "host.orb.internal" || hostname == "host.internal" {
		return nil
	}
	ip := net.ParseIP(hostname)
	if ip == nil || (!ip.IsLoopback() && !ip.IsPrivate()) {
		return errors.New("Hermes Broker endpoint must resolve to a loopback or private address")
	}
	return nil
}
