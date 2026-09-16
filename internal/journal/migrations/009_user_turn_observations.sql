CREATE TABLE user_turn_observations (
  observation_id TEXT PRIMARY KEY,
  adapter_id TEXT NOT NULL,
  connection_id TEXT NOT NULL,
  tenant_id TEXT NOT NULL,
  agent_id TEXT NOT NULL,
  user_id TEXT NOT NULL,
  session_id TEXT NOT NULL,
  turn_id TEXT NOT NULL,
  turn_json BLOB NOT NULL,
  route_json BLOB NOT NULL,
  completed_json BLOB,
  receipt_json BLOB,
  active INTEGER NOT NULL DEFAULT 1,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL
);
CREATE INDEX user_turn_observations_session ON user_turn_observations(adapter_id,connection_id,tenant_id,agent_id,user_id,session_id,updated_at);
