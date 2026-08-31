CREATE TABLE IF NOT EXISTS control_plane_installations(
  installation_id TEXT PRIMARY KEY,
  instance_id TEXT NOT NULL,
  owner_user_id TEXT NOT NULL,
  owner_team_id TEXT NOT NULL,
  owner_agent_id TEXT NOT NULL,
  owner_asset_id TEXT NOT NULL,
  panel_container TEXT NOT NULL,
  panel_image TEXT NOT NULL,
  state TEXT NOT NULL CHECK(state IN ('provisioning', 'provisioned', 'active', 'inactive', 'failed')),
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS principal_agents(
  principal_fingerprint TEXT PRIMARY KEY,
  route_kind TEXT NOT NULL CHECK(route_kind IN ('hermes-private', 'hermes-group')),
  backend_user_id TEXT NOT NULL,
  backend_team_id TEXT NOT NULL,
  backend_agent_id TEXT NOT NULL UNIQUE,
  backend_asset_id TEXT NOT NULL UNIQUE,
  display_label TEXT NOT NULL,
  state TEXT NOT NULL CHECK(state IN ('provisioning', 'active', 'inactive', 'failed')),
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_principal_agents_backend_agent
  ON principal_agents(backend_agent_id);
