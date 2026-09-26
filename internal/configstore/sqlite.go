package configstore

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"database/sql"
	"errors"
	"fmt"
	_ "modernc.org/sqlite"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
)

const schema = `
CREATE TABLE IF NOT EXISTS metadata (
  key TEXT PRIMARY KEY,
  value BLOB NOT NULL
);
CREATE TABLE IF NOT EXISTS backends (
  id TEXT PRIMARY KEY COLLATE NOCASE,
  enabled INTEGER NOT NULL,
  config_json BLOB NOT NULL,
  secrets BLOB NOT NULL,
  revision INTEGER NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  last_probe_at TEXT,
  last_probe_ok INTEGER,
  last_probe_json BLOB
);
CREATE TABLE IF NOT EXISTS events (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  backend_id TEXT,
  actor TEXT NOT NULL DEFAULT 'local',
  action TEXT NOT NULL,
  success INTEGER NOT NULL,
  message TEXT NOT NULL DEFAULT '',
  revision INTEGER NOT NULL DEFAULT 0,
  created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS tool_groups (
  id TEXT PRIMARY KEY COLLATE NOCASE,
  enabled INTEGER NOT NULL,
  config_json BLOB NOT NULL,
  secrets BLOB NOT NULL,
  revision INTEGER NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  last_probe_at TEXT,
  last_probe_ok INTEGER,
  last_probe_json BLOB
);
CREATE TABLE IF NOT EXISTS http_tools (
  group_id TEXT NOT NULL COLLATE NOCASE,
  name TEXT NOT NULL COLLATE NOCASE,
  config_json BLOB NOT NULL,
  revision INTEGER NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  PRIMARY KEY(group_id, name),
  FOREIGN KEY(group_id) REFERENCES tool_groups(id) ON DELETE CASCADE
);
CREATE TABLE IF NOT EXISTS openapi_imports (
  group_id TEXT NOT NULL COLLATE NOCASE,
  id TEXT NOT NULL COLLATE NOCASE,
  config_json BLOB NOT NULL,
  spec_document BLOB NOT NULL,
  spec_sha256 TEXT NOT NULL,
  revision INTEGER NOT NULL,
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
  intent BLOB NOT NULL,
  status TEXT NOT NULL,
  reviewer TEXT NOT NULL,
  created_at BIGINT NOT NULL,
  expires_at BIGINT NOT NULL,
  updated_at BIGINT NOT NULL,
  result BLOB
);
CREATE INDEX IF NOT EXISTS approvals_owner ON approvals(issuer, subject, status, expires_at);
CREATE INDEX IF NOT EXISTS approvals_created ON approvals(created_at DESC);
CREATE TABLE IF NOT EXISTS approval_events (
  id TEXT PRIMARY KEY,
  approval_id TEXT NOT NULL REFERENCES approvals(id) ON DELETE CASCADE,
  action TEXT NOT NULL,
  actor TEXT NOT NULL,
  detail BLOB,
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
  envelope BLOB NOT NULL,
  notification BLOB NOT NULL,
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
 status TEXT NOT NULL, revision BIGINT NOT NULL, data BLOB NOT NULL, credential_hash TEXT UNIQUE,
 exchange_hash TEXT, created_at BIGINT NOT NULL, expires_at BIGINT NOT NULL, request_expires_at BIGINT NOT NULL
);
CREATE INDEX IF NOT EXISTS client_grants_owner ON client_grants(issuer,subject,status,expires_at);
CREATE INDEX IF NOT EXISTS client_grants_session ON client_grants(session_id,client_id);
CREATE TABLE IF NOT EXISTS client_authorization_events (
 id TEXT PRIMARY KEY, grant_id TEXT NOT NULL, data BLOB NOT NULL, created_at BIGINT NOT NULL
);

CREATE TABLE IF NOT EXISTS identities (
 id TEXT PRIMARY KEY, provider TEXT NOT NULL, kind TEXT NOT NULL, external_id TEXT NOT NULL,
 data BLOB NOT NULL, UNIQUE(provider,kind,external_id)
);
CREATE TABLE IF NOT EXISTS sso_sessions (
 id TEXT PRIMARY KEY, data BLOB NOT NULL, expires_at BIGINT NOT NULL
);
CREATE TABLE IF NOT EXISTS sso_refresh (
 hash TEXT PRIMARY KEY, session_id TEXT NOT NULL REFERENCES sso_sessions(id) ON DELETE CASCADE,
 used INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS sso_refresh_session ON sso_refresh(session_id);

`

func Open(ctx context.Context, path string, key []byte) (*Store, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("configuration encryption key must be 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("initialize configuration encryption: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("initialize configuration encryption: %w", err)
	}
	parent := filepath.Dir(path)
	if _, err := os.Stat(parent); errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(parent, 0o700); err != nil {
			return nil, fmt.Errorf("create configuration database directory: %w", err)
		}
	} else if err != nil {
		return nil, fmt.Errorf("inspect configuration database directory: %w", err)
	}
	databaseExisted := false
	if info, statErr := os.Stat(path); statErr == nil {
		databaseExisted = info.Size() > 0
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect configuration database: %w", statErr)
	}
	databaseFile, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create configuration database: %w", err)
	}
	if err := databaseFile.Close(); err != nil {
		return nil, fmt.Errorf("create configuration database: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return nil, fmt.Errorf("secure configuration database: %w", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open configuration database: %w", err)
	}
	db.SetMaxOpenConns(1)
	store := &Store{db: &database{DB: db}, aead: aead}
	closeOnError := func(err error) (*Store, error) {
		_ = db.Close()
		return nil, err
	}
	if databaseExisted {
		if err := store.ensureExistingCanary(ctx); err != nil {
			return closeOnError(err)
		}
	}
	for _, pragma := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA foreign_keys=ON",
		"PRAGMA busy_timeout=5000",
	} {
		if _, err := db.ExecContext(ctx, pragma); err != nil {
			return closeOnError(fmt.Errorf("configure configuration database: %w", err))
		}
	}
	if err := migrateSchema(ctx, db); err != nil {
		return closeOnError(err)
	}
	if err := store.ensureEndpointUIDs(ctx); err != nil {
		return closeOnError(err)
	}
	if err := secureSQLiteFiles(path); err != nil {
		return closeOnError(err)
	}
	if !databaseExisted {
		if err := store.ensureCanary(ctx); err != nil {
			return closeOnError(err)
		}
	}
	return store, nil
}

func secureSQLiteFiles(path string) error {
	for _, filename := range []string{path, path + "-wal", path + "-shm", path + "-journal"} {
		if err := os.Chmod(filename, 0o600); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("secure configuration database file: %w", err)
		}
	}
	return nil
}

func OpenReadOnly(ctx context.Context, path string, key []byte) (*Store, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("configuration encryption key must be 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("initialize configuration encryption: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("initialize configuration encryption: %w", err)
	}
	databaseURL := url.URL{Scheme: "file", Path: filepath.ToSlash(path)}
	query := databaseURL.Query()
	query.Set("mode", "ro")
	databaseURL.RawQuery = query.Encode()
	db, err := sql.Open("sqlite", databaseURL.String())
	if err != nil {
		return nil, fmt.Errorf("open configuration database read-only: %w", err)
	}
	db.SetMaxOpenConns(1)
	store := &Store{db: &database{DB: db}, aead: aead}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("open configuration database read-only: %w", err)
	}
	if err := store.ensureExistingCanary(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

type schemaExecutor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func migrateSchema(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin configuration schema migration: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("initialize configuration database: %w", err)
	}
	var versionRaw []byte
	err = tx.QueryRowContext(ctx, "SELECT value FROM metadata WHERE key='schema_version'").Scan(&versionRaw)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("read configuration schema version: %w", err)
	}
	if err == nil {
		version, parseErr := strconv.Atoi(string(versionRaw))
		if parseErr != nil || version < 1 || version > 7 {
			return fmt.Errorf("unsupported configuration schema version %q", string(versionRaw))
		}
	}
	if err := ensureEventActorColumn(ctx, tx); err != nil {
		return err
	}
	if err := ensureEventSourceColumns(ctx, tx); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO metadata(key, value) VALUES('schema_version', '7')
ON CONFLICT(key) DO UPDATE SET value=excluded.value`); err != nil {
		return fmt.Errorf("record configuration schema version: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit configuration schema migration: %w", err)
	}
	return nil
}

func ensureEventActorColumn(ctx context.Context, db schemaExecutor) error {
	rows, err := db.QueryContext(ctx, "PRAGMA table_info(events)")
	if err != nil {
		return fmt.Errorf("inspect configuration event schema: %w", err)
	}
	defer rows.Close()
	hasActor := false
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			return fmt.Errorf("inspect configuration event schema: %w", err)
		}
		if name == "actor" {
			hasActor = true
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("inspect configuration event schema: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("inspect configuration event schema: %w", err)
	}
	if hasActor {
		return nil
	}
	if _, err := db.ExecContext(ctx, "ALTER TABLE events ADD COLUMN actor TEXT NOT NULL DEFAULT 'local'"); err != nil {
		return fmt.Errorf("upgrade configuration event schema: %w", err)
	}
	return nil
}

func ensureEventSourceColumns(ctx context.Context, db schemaExecutor) error {
	columns := map[string]bool{}
	rows, err := db.QueryContext(ctx, "PRAGMA table_info(events)")
	if err != nil {
		return fmt.Errorf("inspect configuration event schema: %w", err)
	}
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			_ = rows.Close()
			return fmt.Errorf("inspect configuration event schema: %w", err)
		}
		columns[name] = true
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("inspect configuration event schema: %w", err)
	}
	for _, column := range []string{"source_kind", "source_id"} {
		if columns[column] {
			continue
		}
		if _, err := db.ExecContext(ctx, "ALTER TABLE events ADD COLUMN "+column+" TEXT NOT NULL DEFAULT ''"); err != nil {
			return fmt.Errorf("upgrade configuration event schema: %w", err)
		}
	}
	_, err = db.ExecContext(ctx, `UPDATE events SET source_kind='backend', source_id=backend_id
WHERE source_kind='' AND backend_id IS NOT NULL AND backend_id<>''`)
	if err != nil {
		return fmt.Errorf("backfill configuration event sources: %w", err)
	}
	return nil
}
