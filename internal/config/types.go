package config

// Config is MLink's versioned, non-secret user configuration.
type Config struct {
	SchemaVersion      int                   `yaml:"schema_version"`
	NamespaceID        string                `yaml:"namespace_id"`
	ActiveConnectionID string                `yaml:"active_connection_id"`
	Connections        map[string]Connection `yaml:"connections"`
	Adapters           map[string]Adapter    `yaml:"adapters"`
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
	TenantID           string            `yaml:"tenant_id"`
	AgentID            string            `yaml:"agent_id"`
	UserID             string            `yaml:"user_id"`
	IncludeAgentShared bool              `yaml:"include_agent_shared"`
}

// Adapter records the desired state of one Agent integration.
type Adapter struct {
	ID           string         `yaml:"id"`
	Enabled      bool           `yaml:"enabled"`
	ConnectionID string         `yaml:"connection_id"`
	Config       map[string]any `yaml:"config,omitempty"`
}
