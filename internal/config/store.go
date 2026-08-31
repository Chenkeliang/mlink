package config

import (
	"bytes"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Store persists the non-secret MLink configuration.
type Store struct {
	Path string
}

func (s Store) Load() (Config, error) {
	data, err := os.ReadFile(s.Path)
	if err != nil {
		return Config{}, err
	}
	return Decode(data)
}

func (s Store) SaveAtomic(cfg Config) error {
	if s.Path == "" {
		return errors.New("config path is required")
	}
	if err := Validate(cfg); err != nil {
		return err
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("encode MLink config: %w", err)
	}
	dir := filepath.Dir(s.Path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	temp, err := os.CreateTemp(dir, ".config-*.yaml")
	if err != nil {
		return fmt.Errorf("create temporary config: %w", err)
	}
	tempPath := temp.Name()
	committed := false
	defer func() {
		_ = temp.Close()
		if !committed {
			_ = os.Remove(tempPath)
		}
	}()
	if err := temp.Chmod(0o600); err != nil {
		return fmt.Errorf("protect temporary config: %w", err)
	}
	if _, err := temp.Write(data); err != nil {
		return fmt.Errorf("write temporary config: %w", err)
	}
	if err := temp.Sync(); err != nil {
		return fmt.Errorf("sync temporary config: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close temporary config: %w", err)
	}
	if err := os.Rename(tempPath, s.Path); err != nil {
		return fmt.Errorf("replace config: %w", err)
	}
	committed = true
	if err := os.Chmod(s.Path, 0o600); err != nil {
		return fmt.Errorf("protect config: %w", err)
	}
	dirFile, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("open config directory: %w", err)
	}
	defer dirFile.Close()
	if err := dirFile.Sync(); err != nil {
		return fmt.Errorf("sync config directory: %w", err)
	}
	return nil
}

var safeIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

func Decode(data []byte) (Config, error) {
	var header struct {
		SchemaVersion int `yaml:"schema_version"`
	}
	if err := yaml.Unmarshal(data, &header); err != nil {
		return Config{}, fmt.Errorf("decode MLink config header: %w", err)
	}
	if header.SchemaVersion == 1 {
		return Config{}, ErrMigrationRequired
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	var cfg Config
	if err := decoder.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("decode MLink config: %w", err)
	}
	if err := Validate(cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func Validate(cfg Config) error {
	if cfg.SchemaVersion != 2 && cfg.SchemaVersion != 3 {
		return fmt.Errorf("unsupported MLink config schema %d", cfg.SchemaVersion)
	}
	if !safeIDPattern.MatchString(cfg.NamespaceID) || !safeIDPattern.MatchString(cfg.ActiveConnectionID) {
		return errors.New("valid namespace and active connection are required")
	}
	if _, exists := cfg.Connections[cfg.ActiveConnectionID]; !exists {
		return errors.New("active connection is missing")
	}
	for id, connection := range cfg.Connections {
		if id != connection.ID || !safeIDPattern.MatchString(id) || connection.ProviderID == "" || connection.ProviderVersion == "" || connection.ConfigRevision == "" {
			return fmt.Errorf("invalid connection %q", id)
		}
		if connection.TenantID != "" || connection.AgentID != "" || connection.UserID != "" || connection.IncludeAgentShared {
			return fmt.Errorf("connection %q contains legacy identity fields", id)
		}
	}
	for id, principal := range cfg.Principals {
		if id != principal.ID || !safeIDPattern.MatchString(id) || !safeIDPattern.MatchString(principal.CanonicalUserID) || principal.Kind != PrincipalPerson {
			return fmt.Errorf("invalid principal %q", id)
		}
	}
	for id, space := range cfg.Spaces {
		if id != space.ID || !safeIDPattern.MatchString(id) || !safeIDPattern.MatchString(space.TenantID) || !safeIDPattern.MatchString(space.AgentID) {
			return fmt.Errorf("invalid memory space %q", id)
		}
		if _, exists := cfg.Connections[space.ConnectionID]; !exists {
			return fmt.Errorf("memory space %q references missing connection", id)
		}
		switch space.PrincipalPolicy {
		case PolicyFixed:
			if _, exists := cfg.Principals[space.PrincipalID]; !exists {
				return fmt.Errorf("memory space %q references missing principal", id)
			}
		case PolicyExternalHMAC, PolicyGroupHMAC:
			if space.PrincipalID != "" {
				return fmt.Errorf("dynamic memory space %q has a fixed principal", id)
			}
		default:
			return fmt.Errorf("memory space %q has invalid principal policy", id)
		}
		if space.IncludeAgentShared && id != "personal-owner" {
			return fmt.Errorf("memory space %q cannot enable Agent-shared memory", id)
		}
	}
	for id, adapter := range cfg.Adapters {
		if id != adapter.ID || !safeIDPattern.MatchString(id) {
			return fmt.Errorf("invalid adapter %q", id)
		}
		if adapter.ConnectionID != "" || len(adapter.Config) != 0 {
			return fmt.Errorf("adapter %q contains legacy routing fields", id)
		}
		if adapter.HermesRouting != nil {
			for _, spaceID := range []string{adapter.HermesRouting.OwnerSpaceID, adapter.HermesRouting.PrivateSpaceID, adapter.HermesRouting.GroupSpaceID} {
				if _, exists := cfg.Spaces[spaceID]; !exists {
					return fmt.Errorf("adapter %q references missing memory space", id)
				}
			}
			if adapter.SpaceID != "" {
				return fmt.Errorf("adapter %q mixes fixed and delegated routing", id)
			}
		} else if _, exists := cfg.Spaces[adapter.SpaceID]; !exists {
			return fmt.Errorf("adapter %q references missing memory space", id)
		}
	}
	secretRefs := make(map[string]string, len(cfg.Bindings))
	for id, binding := range cfg.Bindings {
		if id != binding.ID {
			return fmt.Errorf("invalid binding slot %q", id)
		}
		if err := ValidateBindingRef(binding); err != nil {
			return err
		}
		if _, exists := cfg.Principals[binding.PrincipalID]; !exists {
			return fmt.Errorf("binding %q references missing principal", id)
		}
		if previous, exists := secretRefs[binding.SecretRef]; exists {
			return fmt.Errorf("bindings %q and %q share one secret", previous, id)
		}
		secretRefs[binding.SecretRef] = id
	}
	if cfg.SchemaVersion == 2 {
		if cfg.ControlPlane != nil || len(cfg.RoutingPolicies) != 0 {
			return errors.New("schema v2 config contains schema v3 control-plane fields")
		}
		return nil
	}
	if err := validateControlPlane(cfg); err != nil {
		return err
	}
	return nil
}

func validateControlPlane(cfg Config) error {
	control := cfg.ControlPlane
	if control == nil || control.ProviderID != "dev.mlink.tencentdb" || !safeIDPattern.MatchString(control.InstanceID) ||
		!safeIDPattern.MatchString(control.OwnerUserID) || !safeIDPattern.MatchString(control.OwnerTeamID) ||
		!safeIDPattern.MatchString(control.OwnerAgentID) || !safeIDPattern.MatchString(control.OwnerAssetID) {
		return errors.New("valid TencentDB control plane is required")
	}
	connection := cfg.Connections[cfg.ActiveConnectionID]
	if connection.ProviderID != control.ProviderID {
		return errors.New("control plane and active connection providers differ")
	}
	serviceID, _ := connection.ProviderConfig["service_id"].(string)
	if serviceID != control.InstanceID {
		return errors.New("control plane and active connection instances differ")
	}
	panelURL, err := url.Parse(control.PanelURL)
	if err != nil || panelURL.Scheme != "http" || panelURL.Host != "127.0.0.1:8125" || panelURL.Path != "" || panelURL.RawQuery != "" || panelURL.Fragment != "" {
		return errors.New("control plane Panel URL must be http://127.0.0.1:8125")
	}
	owner, exists := cfg.Principals["owner"]
	if !exists || owner.CanonicalUserID != control.OwnerUserID {
		return errors.New("Owner principal must use the Core-generated user ID")
	}
	if len(cfg.RoutingPolicies) != 3 {
		return errors.New("exactly three routing policies are required")
	}
	ownerPolicy := cfg.RoutingPolicies["owner"]
	if ownerPolicy.ID != "owner" || ownerPolicy.AgentPolicy != AgentFixed || ownerPolicy.SessionPolicy != "" ||
		!sameLayers(ownerPolicy.Layers, LayerL1, LayerL2, LayerL3) {
		return errors.New("invalid Owner routing policy")
	}
	privatePolicy := cfg.RoutingPolicies["hermes-private"]
	if privatePolicy.ID != "hermes-private" || privatePolicy.AgentPolicy != AgentDynamicPrincipal || privatePolicy.SessionPolicy != "" ||
		!sameLayers(privatePolicy.Layers, LayerL1) {
		return errors.New("invalid Hermes private routing policy")
	}
	groupPolicy := cfg.RoutingPolicies["hermes-groups"]
	if groupPolicy.ID != "hermes-groups" || groupPolicy.AgentPolicy != AgentDynamicGroup || groupPolicy.SessionPolicy != SessionPerTopic ||
		!sameLayers(groupPolicy.Layers, LayerL1) {
		return errors.New("invalid Hermes group routing policy")
	}
	return nil
}

func sameLayers(got []MemoryLayer, want ...MemoryLayer) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range want {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}

func ValidateBindingRef(binding BindingRef) error {
	if !validBindingSlot(binding) {
		return fmt.Errorf("invalid binding slot %q", binding.ID)
	}
	wantRef := "keychain://dev.mlink/identity/binding/" + binding.ID
	if binding.SecretRef != wantRef || binding.Source != "feishu" || binding.Status != BindingActive && binding.Status != BindingRevoked {
		return fmt.Errorf("invalid binding %q", binding.ID)
	}
	return nil
}

func validBindingSlot(binding BindingRef) bool {
	token := map[string]string{"union_id": "union", "user_id": "user", "open_id": "open"}[binding.Kind]
	if token == "" || !safeIDPattern.MatchString(binding.ID) || !safeIDPattern.MatchString(binding.PrincipalID) {
		return false
	}
	prefix := binding.PrincipalID + "-feishu-" + token + "-"
	if !strings.HasPrefix(binding.ID, prefix) {
		return false
	}
	index, err := strconv.Atoi(strings.TrimPrefix(binding.ID, prefix))
	return err == nil && index > 0
}
