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
CREATE TABLE IF NOT EXISTS approvals (
  id TEXT PRIMARY KEY,
  issuer TEXT NOT NULL,
  subject TEXT NOT NULL,
  intent BYTEA NOT NULL,
  status TEXT NOT NULL,
  reviewer TEXT NOT NULL,
  created_at BIGINT NOT NULL,
  expires_at BIGINT NOT NULL,
  updated_at BIGINT NOT NULL,
  result BYTEA
);
CREATE INDEX IF NOT EXISTS approvals_owner ON approvals(issuer, subject, status, expires_at);
CREATE INDEX IF NOT EXISTS approvals_created ON approvals(created_at DESC);
CREATE TABLE IF NOT EXISTS approval_events (
  id TEXT PRIMARY KEY,
  approval_id TEXT NOT NULL REFERENCES approvals(id) ON DELETE CASCADE,
  action TEXT NOT NULL,
  actor TEXT NOT NULL,
  detail BYTEA,
  created_at BIGINT NOT NULL
);
CREATE INDEX IF NOT EXISTS approval_events_request ON approval_events(approval_id, created_at);
CREATE TABLE IF NOT EXISTS approval_votes (
  approval_id TEXT NOT NULL REFERENCES approvals(id) ON DELETE CASCADE,
  reviewer TEXT NOT NULL,
  created_at BIGINT NOT NULL,
  PRIMARY KEY(approval_id, reviewer)
);
CREATE TABLE IF NOT EXISTS approval_operations (
  operation_key TEXT PRIMARY KEY,
  request_hash TEXT NOT NULL,
  approval_id TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS approval_deliveries (
  sequence BIGINT PRIMARY KEY,
  event_id TEXT NOT NULL UNIQUE,
  approval_id TEXT NOT NULL,
  envelope BYTEA NOT NULL,
  notification BYTEA NOT NULL,
  notify_due BIGINT NOT NULL,
  archive_due BIGINT NOT NULL,
  notify_attempts INTEGER NOT NULL DEFAULT 0,
  archive_attempts INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS approval_delivery_archive ON approval_deliveries(archive_due, sequence);
CREATE INDEX IF NOT EXISTS approval_delivery_notify ON approval_deliveries(notify_due, sequence);

CREATE TABLE IF NOT EXISTS broker_sessions (
 id TEXT PRIMARY KEY, issuer TEXT NOT NULL, subject TEXT NOT NULL, resource TEXT NOT NULL,
 secret_hash TEXT NOT NULL, status TEXT NOT NULL, created_at BIGINT NOT NULL, expires_at BIGINT NOT NULL
);
CREATE TABLE IF NOT EXISTS client_grants (
 id TEXT PRIMARY KEY, issuer TEXT NOT NULL, subject TEXT NOT NULL, resource TEXT NOT NULL,
 session_id TEXT NOT NULL REFERENCES broker_sessions(id), client_id TEXT NOT NULL, endpoint_id TEXT NOT NULL,
 status TEXT NOT NULL, revision BIGINT NOT NULL, data BYTEA NOT NULL, credential_hash TEXT UNIQUE,
 exchange_hash TEXT, created_at BIGINT NOT NULL, expires_at BIGINT NOT NULL, request_expires_at BIGINT NOT NULL
);
CREATE INDEX IF NOT EXISTS client_grants_owner ON client_grants(issuer,subject,status,expires_at);
CREATE INDEX IF NOT EXISTS client_grants_session ON client_grants(session_id,client_id);
CREATE TABLE IF NOT EXISTS client_authorization_events (
 id TEXT PRIMARY KEY, grant_id TEXT NOT NULL, data BYTEA NOT NULL, created_at BIGINT NOT NULL
);

CREATE TABLE IF NOT EXISTS identities (
 id TEXT PRIMARY KEY, provider TEXT NOT NULL, kind TEXT NOT NULL, external_id TEXT NOT NULL,
 data BYTEA NOT NULL, UNIQUE(provider,kind,external_id)
);
CREATE TABLE IF NOT EXISTS sso_sessions (
 id TEXT PRIMARY KEY, data BYTEA NOT NULL, expires_at BIGINT NOT NULL
);
CREATE TABLE IF NOT EXISTS sso_refresh (
 hash TEXT PRIMARY KEY, session_id TEXT NOT NULL REFERENCES sso_sessions(id) ON DELETE CASCADE,
 used INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS sso_refresh_session ON sso_refresh(session_id);
