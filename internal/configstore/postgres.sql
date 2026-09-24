CREATE EXTENSION IF NOT EXISTS citext;

CREATE TABLE IF NOT EXISTS metadata (
  key TEXT PRIMARY KEY,
  value BYTEA NOT NULL
);
CREATE TABLE IF NOT EXISTS backends (
  id CITEXT PRIMARY KEY,
  enabled INTEGER NOT NULL,
  config_json BYTEA NOT NULL,
  secrets BYTEA NOT NULL,
  revision BIGINT NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  last_probe_at TEXT,
  last_probe_ok INTEGER,
  last_probe_json BYTEA
);
CREATE TABLE IF NOT EXISTS events (
  id BIGSERIAL PRIMARY KEY,
  backend_id CITEXT NOT NULL DEFAULT '',
  source_kind TEXT NOT NULL DEFAULT '',
  source_id CITEXT NOT NULL DEFAULT '',
  actor TEXT NOT NULL DEFAULT 'local',
  action TEXT NOT NULL,
  success INTEGER NOT NULL,
  message TEXT NOT NULL DEFAULT '',
  revision BIGINT NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS tool_groups (
  id CITEXT PRIMARY KEY,
  enabled INTEGER NOT NULL,
  config_json BYTEA NOT NULL,
  secrets BYTEA NOT NULL,
  revision BIGINT NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  last_probe_at TEXT,
  last_probe_ok INTEGER,
  last_probe_json BYTEA
);
CREATE TABLE IF NOT EXISTS http_tools (
  group_id CITEXT NOT NULL,
  name CITEXT NOT NULL,
  config_json BYTEA NOT NULL,
  revision BIGINT NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  PRIMARY KEY(group_id, name),
  FOREIGN KEY(group_id) REFERENCES tool_groups(id) ON DELETE CASCADE
);
CREATE TABLE IF NOT EXISTS openapi_imports (
  group_id CITEXT NOT NULL,
  id CITEXT NOT NULL,
  config_json BYTEA NOT NULL,
  spec_document BYTEA NOT NULL,
  spec_sha256 TEXT NOT NULL,
  revision BIGINT NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  last_refresh_at TEXT,
  last_refresh_ok INTEGER,
  last_refresh_message TEXT NOT NULL DEFAULT '',
  PRIMARY KEY(group_id, id),
  FOREIGN KEY(group_id) REFERENCES tool_groups(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS events_created_at ON events(created_at DESC);
