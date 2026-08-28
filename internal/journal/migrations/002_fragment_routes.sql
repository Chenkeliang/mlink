ALTER TABLE turn_fragments ADD COLUMN provider_id TEXT NOT NULL DEFAULT '';
ALTER TABLE turn_fragments ADD COLUMN provider_version TEXT NOT NULL DEFAULT '';
ALTER TABLE turn_fragments ADD COLUMN config_revision TEXT NOT NULL DEFAULT '';
