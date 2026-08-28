CREATE TABLE IF NOT EXISTS schema_migrations(
  version INTEGER PRIMARY KEY,
  applied_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS turn_fragments(
  adapter_id TEXT NOT NULL,
  connection_id TEXT NOT NULL,
  tenant_id TEXT NOT NULL,
  agent_id TEXT NOT NULL,
  user_id TEXT NOT NULL,
  session_id TEXT NOT NULL,
  turn_id TEXT NOT NULL,
  role TEXT NOT NULL,
  content BLOB NOT NULL,
  content_hash TEXT NOT NULL,
  occurred_at TEXT NOT NULL,
  PRIMARY KEY(adapter_id, connection_id, tenant_id, agent_id, user_id, session_id, turn_id, role)
);

CREATE TABLE IF NOT EXISTS journal_events(
  id TEXT PRIMARY KEY,
  idempotency_key TEXT NOT NULL UNIQUE,
  adapter_id TEXT NOT NULL,
  session_id TEXT NOT NULL,
  turn_id TEXT NOT NULL,
  connection_id TEXT NOT NULL,
  provider_id TEXT NOT NULL,
  provider_version TEXT NOT NULL,
  config_revision TEXT NOT NULL,
  tenant_id TEXT NOT NULL,
  agent_id TEXT NOT NULL,
  user_id TEXT NOT NULL,
  content_hash TEXT NOT NULL,
  payload BLOB,
  state TEXT NOT NULL,
  attempt_count INTEGER NOT NULL DEFAULT 0,
  next_attempt_at TEXT,
  receipt_json BLOB,
  error_code TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  UNIQUE(adapter_id, connection_id, tenant_id, agent_id, user_id, session_id, turn_id)
);

CREATE TABLE IF NOT EXISTS delivery_attempts(
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  event_id TEXT NOT NULL,
  started_at TEXT NOT NULL,
  finished_at TEXT,
  delivery_state TEXT,
  error_code TEXT,
  FOREIGN KEY(event_id) REFERENCES journal_events(id)
);

CREATE TABLE IF NOT EXISTS adapter_installations(
  adapter_id TEXT PRIMARY KEY,
  agent_type TEXT NOT NULL,
  version TEXT NOT NULL,
  status TEXT NOT NULL,
  manifest_json BLOB NOT NULL,
  installed_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS owned_resources(
  owner_id TEXT NOT NULL,
  target TEXT NOT NULL,
  semantic_fingerprint TEXT NOT NULL,
  post_apply_hash TEXT NOT NULL,
  PRIMARY KEY(owner_id, target)
);

CREATE TABLE IF NOT EXISTS backup_artifacts(
  backup_id TEXT NOT NULL,
  target TEXT NOT NULL,
  backup_path TEXT NOT NULL,
  before_hash TEXT NOT NULL,
  after_hash TEXT NOT NULL,
  semantic_snapshot BLOB NOT NULL,
  created_at TEXT NOT NULL,
  PRIMARY KEY(backup_id, target)
);
