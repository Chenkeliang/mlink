CREATE TABLE IF NOT EXISTS session_finalizations(
  id TEXT PRIMARY KEY,
  idempotency_key TEXT NOT NULL UNIQUE,
  adapter_id TEXT NOT NULL,
  connection_id TEXT NOT NULL,
  provider_id TEXT NOT NULL,
  provider_version TEXT NOT NULL,
  config_revision TEXT NOT NULL,
  tenant_id TEXT NOT NULL,
  agent_id TEXT NOT NULL,
  user_id TEXT NOT NULL,
  session_id TEXT NOT NULL,
  actor_digest TEXT NOT NULL DEFAULT '',
  state TEXT NOT NULL,
  attempt_count INTEGER NOT NULL DEFAULT 0,
  next_attempt_at TEXT,
  error_code TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  UNIQUE(adapter_id, connection_id, tenant_id, agent_id, user_id, session_id)
);

CREATE INDEX IF NOT EXISTS idx_session_finalizations_ready
  ON session_finalizations(state, next_attempt_at, created_at);
