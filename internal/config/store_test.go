package config

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestStoreRoundTripDoesNotContainSecrets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	store := Store{Path: path}
	want := Config{
		SchemaVersion:      2,
		NamespaceID:        "personal",
		ActiveConnectionID: "local",
		Connections: map[string]Connection{
			"local": {
				ID:              "local",
				ProviderID:      "dev.mlink.tencentdb",
				ProviderVersion: "0.1.0",
				ConfigRevision:  "rev-1",
				ProviderConfig:  map[string]any{"endpoint": "http://127.0.0.1:8096"},
				SecretRefs: map[string]string{
					"token": "keychain://dev.mlink/connection/local/token",
				},
			},
		},
		Principals: map[string]Principal{
			"owner": {ID: "owner", CanonicalUserID: "usr_owner_keliang", Kind: PrincipalPerson},
		},
		Spaces: map[string]MemorySpace{
			"personal-owner": {
				ID: "personal-owner", ConnectionID: "local", TenantID: "personal", AgentID: "keliang-personal",
				IncludeAgentShared: true, PrincipalPolicy: PolicyFixed, PrincipalID: "owner",
			},
			"hermes-private": {
				ID: "hermes-private", ConnectionID: "local", TenantID: "personal", AgentID: "hermes-private",
				PrincipalPolicy: PolicyExternalHMAC,
			},
			"hermes-groups": {
				ID: "hermes-groups", ConnectionID: "local", TenantID: "personal", AgentID: "hermes-groups",
				PrincipalPolicy: PolicyGroupHMAC,
			},
		},
		Adapters: map[string]Adapter{
			"codex": {ID: "codex", Enabled: true, SpaceID: "personal-owner"},
			"hermes": {
				ID: "hermes", Enabled: true,
				HermesRouting: &HermesRouting{OwnerSpaceID: "personal-owner", PrivateSpaceID: "hermes-private", GroupSpaceID: "hermes-groups"},
			},
		},
		Bindings: map[string]BindingRef{
			"owner-feishu-union-1": {
				ID: "owner-feishu-union-1", Source: "feishu", Kind: "union_id", PrincipalID: "owner",
				SecretRef: "keychain://dev.mlink/identity/binding/owner-feishu-union-1", Status: BindingActive,
			},
		},
	}
	if err := store.SaveAtomic(want); err != nil {
		t.Fatal(err)
	}
	got, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("round trip = %#v, want %#v", got, want)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte("actual-secret")) {
		t.Fatal("secret leaked")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("config mode = %o, want 600", got)
	}
}

func TestStoreRoundTripsV3ControlPlane(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	want := fixtureV3Config()
	store := Store{Path: path}
	if err := store.SaveAtomic(want); err != nil {
		t.Fatal(err)
	}
	got, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("round trip = %#v, want %#v", got, want)
	}
}

func TestValidateV3RoutingPolicies(t *testing.T) {
	tests := map[string]func(*Config){
		"missing owner id":    func(cfg *Config) { cfg.ControlPlane.OwnerUserID = "" },
		"missing Agent limit": func(cfg *Config) { cfg.ControlPlane.DynamicAgentLimit = 0 },
		"legacy spaces": func(cfg *Config) {
			cfg.Spaces = fixtureV2Config().Spaces
		},
		"non-loopback panel": func(cfg *Config) { cfg.ControlPlane.PanelURL = "http://0.0.0.0:8125" },
		"wrong provider":     func(cfg *Config) { cfg.ControlPlane.ProviderID = "mem0" },
		"private L2": func(cfg *Config) {
			policy := cfg.RoutingPolicies["hermes-private"]
			policy.Layers = []MemoryLayer{LayerL1, LayerL2}
			cfg.RoutingPolicies["hermes-private"] = policy
		},
		"group without topic sessions": func(cfg *Config) {
			policy := cfg.RoutingPolicies["hermes-groups"]
			policy.SessionPolicy = ""
			cfg.RoutingPolicies["hermes-groups"] = policy
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			cfg := fixtureV3Config()
			mutate(&cfg)
			if err := Validate(cfg); err == nil {
				t.Fatal("Validate() error = nil")
			}
		})
	}
}

func TestValidateV3AdaptersReferencePoliciesWithoutLegacySpaces(t *testing.T) {
	cfg := fixtureV3Config()
	cfg.Adapters = map[string]Adapter{
		"codex": {ID: "codex", Enabled: true, SpaceID: "owner"},
		"pi":    {ID: "pi", Enabled: true, SpaceID: "owner"},
		"hermes": {ID: "hermes", Enabled: true, HermesRouting: &HermesRouting{
			OwnerSpaceID: "owner", PrivateSpaceID: "hermes-private", GroupSpaceID: "hermes-groups",
		}},
	}
	if err := Validate(cfg); err != nil {
		t.Fatal(err)
	}
}

func TestLoadV2WithoutImplicitControlPlaneMigration(t *testing.T) {
	raw, err := yaml.Marshal(fixtureV2Config())
	if err != nil {
		t.Fatal(err)
	}
	got, err := Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got.SchemaVersion != 2 || got.ControlPlane != nil || len(got.RoutingPolicies) != 0 {
		t.Fatalf("v2 config changed during load: %#v", got)
	}
}

func TestV2RejectsBindingSlotThatLeaksExternalValue(t *testing.T) {
	cfg := fixtureV2Config()
	cfg.Bindings["owner-feishu-union-1"] = BindingRef{
		ID: "on_actual_union_id", Source: "feishu", Kind: "union_id", PrincipalID: "owner",
		SecretRef: "keychain://dev.mlink/identity/binding/owner-feishu-union-1", Status: BindingActive,
	}
	if err := Validate(cfg); err == nil {
		t.Fatal("Validate() error = nil")
	}
}

func fixtureV2Config() Config {
	return Config{
		SchemaVersion: 2, NamespaceID: "personal", ActiveConnectionID: "local",
		Connections: map[string]Connection{"local": {
			ID: "local", ProviderID: "dev.mlink.tencentdb", ProviderVersion: "0.1.0", ConfigRevision: "rev-2",
			ProviderConfig: map[string]any{"base_url": "http://127.0.0.1:8420", "service_id": "default", "timeout_ms": 5000},
			SecretRefs:     map[string]string{"token": "keychain://dev.mlink/connection/local/token"},
		}},
		Principals: map[string]Principal{"owner": {ID: "owner", CanonicalUserID: "usr_owner_keliang", Kind: PrincipalPerson}},
		Spaces: map[string]MemorySpace{
			"personal-owner": {ID: "personal-owner", ConnectionID: "local", TenantID: "personal", AgentID: "keliang-personal", IncludeAgentShared: true, PrincipalPolicy: PolicyFixed, PrincipalID: "owner"},
			"hermes-private": {ID: "hermes-private", ConnectionID: "local", TenantID: "personal", AgentID: "hermes-private", PrincipalPolicy: PolicyExternalHMAC},
			"hermes-groups":  {ID: "hermes-groups", ConnectionID: "local", TenantID: "personal", AgentID: "hermes-groups", PrincipalPolicy: PolicyGroupHMAC},
		},
		Adapters: map[string]Adapter{
			"codex":  {ID: "codex", Enabled: true, SpaceID: "personal-owner"},
			"hermes": {ID: "hermes", Enabled: true, HermesRouting: &HermesRouting{OwnerSpaceID: "personal-owner", PrivateSpaceID: "hermes-private", GroupSpaceID: "hermes-groups"}},
		},
		Bindings: map[string]BindingRef{"owner-feishu-union-1": {
			ID: "owner-feishu-union-1", Source: "feishu", Kind: "union_id", PrincipalID: "owner",
			SecretRef: "keychain://dev.mlink/identity/binding/owner-feishu-union-1", Status: BindingActive,
		}},
	}
}

func fixtureV3Config() Config {
	return Config{
		SchemaVersion:      3,
		NamespaceID:        "installation-1",
		ActiveConnectionID: "local",
		Connections: map[string]Connection{"local": {
			ID: "local", ProviderID: "dev.mlink.tencentdb", ProviderVersion: "0.1.0", ConfigRevision: "rev-3",
			ProviderConfig: map[string]any{"base_url": "http://127.0.0.1:8420", "service_id": "default", "timeout_ms": 5000},
			SecretRefs:     map[string]string{"token": "keychain://dev.mlink/connection/local/token"},
		}},
		Principals: map[string]Principal{
			"owner": {ID: "owner", CanonicalUserID: "usr-generated", Kind: PrincipalPerson},
		},
		Adapters: map[string]Adapter{},
		ControlPlane: &ControlPlane{
			ProviderID: "dev.mlink.tencentdb", InstanceID: "default", PanelURL: "http://127.0.0.1:8125",
			OwnerUserID: "usr-generated", OwnerTeamID: "team-generated", OwnerAgentID: "agt-owner",
			OwnerAssetID: "chat_memory-team-generated-agt-owner", DynamicAgentLimit: 500,
		},
		RoutingPolicies: map[string]RoutingPolicy{
			"owner":          {ID: "owner", Layers: []MemoryLayer{LayerL1, LayerL2, LayerL3}, AgentPolicy: AgentFixed},
			"hermes-private": {ID: "hermes-private", Layers: []MemoryLayer{LayerL1}, AgentPolicy: AgentDynamicPrincipal},
			"hermes-groups":  {ID: "hermes-groups", Layers: []MemoryLayer{LayerL1}, AgentPolicy: AgentDynamicGroup, SessionPolicy: SessionPerTopic},
		},
	}
}

func TestStoreLoadMissingConfig(t *testing.T) {
	_, err := (Store{Path: filepath.Join(t.TempDir(), "missing.yaml")}).Load()
	if !os.IsNotExist(err) {
		t.Fatalf("Load() error = %v, want os.ErrNotExist", err)
	}
}
