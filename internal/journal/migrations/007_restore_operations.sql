CREATE TABLE restore_operations (
  operation_id TEXT PRIMARY KEY,
  bundle_fingerprint TEXT NOT NULL,
  plan_id TEXT NOT NULL,
  phase TEXT NOT NULL,
  created_owner_ids_json BLOB NOT NULL,
  error_code TEXT NOT NULL DEFAULT '',
  started_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);

CREATE INDEX restore_operations_updated_at_idx
  ON restore_operations(updated_at DESC);

CREATE TABLE workspace_backup_evidence (
  bundle_fingerprint TEXT PRIMARY KEY,
  format TEXT NOT NULL,
  verified_at TEXT NOT NULL
);
