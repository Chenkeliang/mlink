ALTER TABLE turn_fragments ADD COLUMN actor_digest TEXT NOT NULL DEFAULT '';
ALTER TABLE journal_events ADD COLUMN actor_digest TEXT NOT NULL DEFAULT '';
