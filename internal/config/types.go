package config

// Config is MLink's versioned, non-secret user configuration.
type Config struct {
	SchemaVersion      int                      `yaml:"schema_version"`
	NamespaceID        string                   `yaml:"namespace_id"`
	ActiveConnectionID string                   `yaml:"active_connection_id"`
	Connections        map[string]Connection    `yaml:"connections"`
	Principals         map[string]Principal     `yaml:"principals,omitempty"`
	Spaces             map[string]MemorySpace   `yaml:"spaces,omitempty"`
	Adapters           map[string]Adapter       `yaml:"adapters"`
	Bindings           map[string]BindingRef    `yaml:"bindings,omitempty"`
	Broker             Broker                   `yaml:"broker,omitempty"`
	ControlPlane       *ControlPlane            `yaml:"control_plane,omitempty"`
	RoutingPolicies    map[string]RoutingPolicy `yaml:"routing_policies,omitempty"`
}

type MemoryLayer string

const (
	LayerL1 MemoryLayer = "L1"
	LayerL2 MemoryLayer = "L2"
	LayerL3 MemoryLayer = "L3"
)

type AgentPolicy string

const (
	AgentFixed            AgentPolicy = "fixed"
	AgentDynamicPrincipal AgentPolicy = "dynamic_per_principal"
	AgentDynamicGroup     AgentPolicy = "dynamic_per_group"
)

type SessionPolicy string

const SessionPerTopic SessionPolicy = "per_topic"

type ControlPlane struct {
	ProviderID        string `yaml:"provider_id"`
	InstanceID        string `yaml:"instance_id"`
	PanelURL          string `yaml:"panel_url"`
	OwnerUserID       string `yaml:"owner_user_id"`
	OwnerTeamID       string `yaml:"owner_team_id"`
	OwnerAgentID      string `yaml:"owner_agent_id"`
	OwnerAssetID      string `yaml:"owner_asset_id"`
	DynamicAgentLimit int    `yaml:"dynamic_agent_limit"`
}

type RoutingPolicy struct {
	ID            string        `yaml:"id"`
	Layers        []MemoryLayer `yaml:"layers"`
	AgentPolicy   AgentPolicy   `yaml:"agent_policy"`
	SessionPolicy SessionPolicy `yaml:"session_policy,omitempty"`
}

type Broker struct {
	HermesEndpoint string `yaml:"hermes_endpoint,omitempty"`
	ListenAddress  string `yaml:"listen_address,omitempty"`
}

// Connection selects and configures a memory provider. SecretRefs point to an
// external secret store; credentials are never persisted in this structure.
type Connection struct {
	ID                 string            `yaml:"id"`
	ProviderID         string            `yaml:"provider_id"`
	ProviderVersion    string            `yaml:"provider_version"`
	ConfigRevision     string            `yaml:"config_revision"`
	ProviderConfig     map[string]any    `yaml:"provider_config"`
	SecretRefs         map[string]string `yaml:"secret_refs"`
	TenantID           string            `yaml:"tenant_id,omitempty"`
	AgentID            string            `yaml:"agent_id,omitempty"`
	UserID             string            `yaml:"user_id,omitempty"`
	IncludeAgentShared bool              `yaml:"include_agent_shared,omitempty"`
}

// Adapter records the desired state of one Agent integration.
type Adapter struct {
	ID            string         `yaml:"id"`
	Enabled       bool           `yaml:"enabled"`
	SpaceID       string         `yaml:"space_id,omitempty"`
	HermesRouting *HermesRouting `yaml:"hermes_routing,omitempty"`
	ConnectionID  string         `yaml:"connection_id,omitempty"`
	Config        map[string]any `yaml:"config,omitempty"`
}

type PrincipalKind string

const PrincipalPerson PrincipalKind = "person"

type Principal struct {
	ID              string        `yaml:"id"`
	CanonicalUserID string        `yaml:"canonical_user_id"`
	Kind            PrincipalKind `yaml:"kind"`
}

type PrincipalPolicy string

const (
	PolicyFixed        PrincipalPolicy = "fixed"
	PolicyExternalHMAC PrincipalPolicy = "external_hmac"
	PolicyGroupHMAC    PrincipalPolicy = "group_hmac"
)

type MemorySpace struct {
	ID                 string          `yaml:"id"`
	ConnectionID       string          `yaml:"connection_id"`
	TenantID           string          `yaml:"tenant_id"`
	AgentID            string          `yaml:"agent_id"`
	IncludeAgentShared bool            `yaml:"include_agent_shared,omitempty"`
	PrincipalPolicy    PrincipalPolicy `yaml:"principal_policy"`
	PrincipalID        string          `yaml:"principal_id,omitempty"`
}

type BindingStatus string

const (
	BindingActive  BindingStatus = "active"
	BindingRevoked BindingStatus = "revoked"
)

type BindingRef struct {
	ID          string        `yaml:"id"`
	Source      string        `yaml:"source"`
	Kind        string        `yaml:"kind"`
	PrincipalID string        `yaml:"principal_id"`
	SecretRef   string        `yaml:"secret_ref"`
	Status      BindingStatus `yaml:"status"`
}

type HermesRouting struct {
	OwnerSpaceID   string `yaml:"owner_space_id"`
	PrivateSpaceID string `yaml:"private_space_id"`
	GroupSpaceID   string `yaml:"group_space_id"`
}
