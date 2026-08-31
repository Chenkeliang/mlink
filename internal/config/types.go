package config

// Config is MLink's versioned, non-secret user configuration.
type Config struct {
	SchemaVersion      int                    `yaml:"schema_version"`
	NamespaceID        string                 `yaml:"namespace_id"`
	ActiveConnectionID string                 `yaml:"active_connection_id"`
	Connections        map[string]Connection  `yaml:"connections"`
	Principals         map[string]Principal   `yaml:"principals,omitempty"`
	Spaces             map[string]MemorySpace `yaml:"spaces,omitempty"`
	Adapters           map[string]Adapter     `yaml:"adapters"`
	Bindings           map[string]BindingRef  `yaml:"bindings,omitempty"`
	Broker             Broker                 `yaml:"broker,omitempty"`
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
