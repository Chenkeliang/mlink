ALTER TABLE control_plane_installations
  ADD COLUMN dynamic_agent_limit INTEGER NOT NULL DEFAULT 500;
