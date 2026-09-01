ALTER TABLE journal_events ADD COLUMN resolution TEXT;
ALTER TABLE journal_events ADD COLUMN resolved_reason TEXT;
ALTER TABLE journal_events ADD COLUMN resolved_at TEXT;
ALTER TABLE journal_events ADD COLUMN resolved_by TEXT;
ALTER TABLE journal_events ADD COLUMN provider_ref TEXT;
CREATE INDEX IF NOT EXISTS idx_journal_events_unresolved ON journal_events(state, resolution, created_at);
