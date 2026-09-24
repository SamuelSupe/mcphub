package configstore

import (
	"context"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/SamuelSupe/mcphub/internal/config"
	"github.com/SamuelSupe/mcphub/internal/ratelimit"
)

var (
	ErrNotFound = errors.New("backend not found")
	ErrConflict = errors.New("backend revision conflict")
)

type Store struct {
	db   *database
	aead cipher.AEAD
}

type Record struct {
	Config      config.BackendConfig
	Enabled     bool
	Revision    int64
	CreatedAt   time.Time
	UpdatedAt   time.Time
	LastProbeAt *time.Time
	LastProbeOK *bool
	LastProbe   json.RawMessage
}

type Event struct {
	ID         int64     `json:"id"`
	BackendID  string    `json:"backend_id,omitempty"`
	SourceKind string    `json:"source_kind,omitempty"`
	SourceID   string    `json:"source_id,omitempty"`
	Actor      string    `json:"actor"`
	Action     string    `json:"action"`
	Success    bool      `json:"success"`
	Message    string    `json:"message,omitempty"`
	Revision   int64     `json:"revision,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
}

type storedConfig struct {
	RateLimit         ratelimit.Config   `json:"rate_limit"`
	ID                string             `json:"id"`
	URL               string             `json:"url"`
	Required          bool               `json:"required"`
	RequiredScopes    []string           `json:"required_scopes,omitempty"`
	ToolRules         []config.ToolRule  `json:"tool_rules,omitempty"`
	RequestTimeoutNS  int64              `json:"request_timeout_ns"`
	AllowInsecureHTTP bool               `json:"allow_insecure_http"`
	HeaderNames       []string           `json:"header_names,omitempty"`
	OAuth             *storedOAuthConfig `json:"oauth,omitempty"`
}

type storedOAuthConfig struct {
	Type     string   `json:"type"`
	Issuer   string   `json:"issuer"`
	ClientID string   `json:"client_id"`
	Scopes   []string `json:"scopes,omitempty"`
}

type storedSecrets struct {
	Headers           map[string]string `json:"headers,omitempty"`
	OAuthClientSecret string            `json:"oauth_client_secret,omitempty"`
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) Initialized(ctx context.Context) (bool, error) {
	var value []byte
	err := s.db.QueryRowContext(ctx, "SELECT value FROM metadata WHERE key = 'bootstrap_complete'").Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read bootstrap state: %w", err)
	}
	return string(value) == "1", nil
}

func (s *Store) Bootstrap(ctx context.Context, backends []config.BackendConfig) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin backend bootstrap: %w", err)
	}
	defer tx.Rollback()
	var marker []byte
	err = tx.QueryRowContext(ctx, "SELECT value FROM metadata WHERE key = 'bootstrap_complete'").Scan(&marker)
	if err == nil {
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("read backend bootstrap marker: %w", err)
	}
	now := time.Now().UTC()
	for _, backend := range backends {
		record := Record{Config: backend, Enabled: true, Revision: 1, CreatedAt: now, UpdatedAt: now}
		if err := s.insertRecord(ctx, tx, record); err != nil {
			return err
		}
		if err := insertEvent(ctx, tx, backend.ID, "bootstrap", true, "", 1, now); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO metadata(key, value) VALUES('bootstrap_complete', '1')"); err != nil {
		return fmt.Errorf("write backend bootstrap marker: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit backend bootstrap: %w", err)
	}
	return nil
}

func (s *Store) List(ctx context.Context) ([]Record, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, enabled, config_json, secrets, revision, created_at, updated_at,
last_probe_at, last_probe_ok, last_probe_json FROM backends ORDER BY id COLLATE NOCASE`)
	if err != nil {
		return nil, fmt.Errorf("list backends: %w", err)
	}
	defer rows.Close()
	var records []Record
	for rows.Next() {
		record, err := s.scanRecord(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list backends: %w", err)
	}
	return records, nil
}

func (s *Store) Get(ctx context.Context, id string) (Record, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id, enabled, config_json, secrets, revision, created_at, updated_at,
last_probe_at, last_probe_ok, last_probe_json FROM backends WHERE id = ? COLLATE NOCASE`, id)
	record, err := s.scanRecord(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Record{}, ErrNotFound
	}
	return record, err
}

func (s *Store) Create(ctx context.Context, record Record) (Record, error) {
	record.CreatedAt = time.Now().UTC()
	record.UpdatedAt = record.CreatedAt
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Record{}, fmt.Errorf("begin backend create: %w", err)
	}
	defer tx.Rollback()
	var existing int
	if err := tx.QueryRowContext(ctx, "SELECT 1 FROM tool_groups WHERE id=? COLLATE NOCASE", record.Config.ID).Scan(&existing); err == nil {
		return Record{}, ErrConflict
	} else if !errors.Is(err, sql.ErrNoRows) {
		return Record{}, fmt.Errorf("check backend identity: %w", err)
	}
	record.Revision, err = nextSourceRevision(ctx, tx, "backend", record.Config.ID)
	if err != nil {
		return Record{}, err
	}
	if err := s.insertRecord(ctx, tx, record); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return Record{}, ErrConflict
		}
		return Record{}, err
	}
	if err := insertEvent(ctx, tx, record.Config.ID, "create", true, "", record.Revision, record.CreatedAt); err != nil {
		return Record{}, err
	}
	if err := tx.Commit(); err != nil {
		return Record{}, fmt.Errorf("commit backend create: %w", err)
	}
	return record, nil
}

func (s *Store) Update(ctx context.Context, record Record, expectedRevision int64) (Record, error) {
	current, err := s.Get(ctx, record.Config.ID)
	if err != nil {
		return Record{}, err
	}
	if current.Revision != expectedRevision {
		return Record{}, ErrConflict
	}
	record.Revision = expectedRevision + 1
	record.CreatedAt = current.CreatedAt
	record.UpdatedAt = time.Now().UTC()
	record.LastProbeAt = current.LastProbeAt
	record.LastProbeOK = current.LastProbeOK
	record.LastProbe = slices.Clone(current.LastProbe)
	public, secrets, err := s.encodeRecord(record)
	if err != nil {
		return Record{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Record{}, fmt.Errorf("begin backend update: %w", err)
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE backends SET enabled=?, config_json=?, secrets=?, revision=?, updated_at=?
WHERE id=? COLLATE NOCASE AND revision=?`, boolInt(record.Enabled), public, secrets, record.Revision,
		record.UpdatedAt.Format(time.RFC3339Nano), record.Config.ID, expectedRevision)
	if err != nil {
		return Record{}, fmt.Errorf("update backend: %w", err)
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return Record{}, ErrConflict
	}
	if err := insertEvent(ctx, tx, record.Config.ID, "update", true, "", record.Revision, record.UpdatedAt); err != nil {
		return Record{}, err
	}
	if err := tx.Commit(); err != nil {
		return Record{}, fmt.Errorf("commit backend update: %w", err)
	}
	return record, nil
}

func (s *Store) Delete(ctx context.Context, id string, expectedRevision int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin backend delete: %w", err)
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, "DELETE FROM backends WHERE id=? COLLATE NOCASE AND revision=?", id, expectedRevision)
	if err != nil {
		return fmt.Errorf("delete backend: %w", err)
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		var exists int
		if err := tx.QueryRowContext(ctx, "SELECT 1 FROM backends WHERE id=? COLLATE NOCASE", id).Scan(&exists); errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return ErrConflict
	}
	now := time.Now().UTC()
	if err := insertEvent(ctx, tx, id, "delete", true, "", expectedRevision+1, now); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit backend delete: %w", err)
	}
	return nil
}

func (s *Store) RecordFailure(ctx context.Context, backendID, action, message string) {
	_, _ = s.db.ExecContext(ctx, `INSERT INTO events(backend_id, source_kind, source_id, actor, action, success, message, created_at)
VALUES(?, 'backend', ?, ?, ?, 0, ?, ?)`, backendID, backendID, Actor(ctx), action, message, time.Now().UTC().Format(time.RFC3339Nano))
}

func (s *Store) UpdateProbe(ctx context.Context, id string, ok bool, result json.RawMessage) error {
	now := time.Now().UTC()
	updated, err := s.db.ExecContext(ctx, `UPDATE backends SET last_probe_at=?, last_probe_ok=?, last_probe_json=?
WHERE id=? COLLATE NOCASE`, now.Format(time.RFC3339Nano), boolInt(ok), []byte(result), id)
	if err != nil {
		return fmt.Errorf("update backend probe: %w", err)
	}
	changed, _ := updated.RowsAffected()
	if changed != 1 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) Events(ctx context.Context, limit int) ([]Event, error) {
	if limit < 1 || limit > 100 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, backend_id, source_kind, source_id, actor, action, success, message, revision, created_at
FROM events ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("list configuration events: %w", err)
	}
	defer rows.Close()
	events := make([]Event, 0, limit)
	for rows.Next() {
		var event Event
		var backendID sql.NullString
		var success int
		var created string
		if err := rows.Scan(&event.ID, &backendID, &event.SourceKind, &event.SourceID, &event.Actor, &event.Action, &success, &event.Message, &event.Revision, &created); err != nil {
			return nil, fmt.Errorf("scan configuration event: %w", err)
		}
		event.BackendID = backendID.String
		event.Success = success != 0
		event.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		events = append(events, event)
	}
	return events, rows.Err()
}

func (s *Store) insertRecord(ctx context.Context, tx *transaction, record Record) error {
	public, secrets, err := s.encodeRecord(record)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO backends(id, enabled, config_json, secrets, revision, created_at, updated_at)
VALUES(?, ?, ?, ?, ?, ?, ?)`, record.Config.ID, boolInt(record.Enabled), public, secrets, record.Revision,
		record.CreatedAt.Format(time.RFC3339Nano), record.UpdatedAt.Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("insert backend: %w", err)
	}
	return nil
}

func (s *Store) encodeRecord(record Record) ([]byte, []byte, error) {
	headerNames := make([]string, 0, len(record.Config.Headers))
	for name := range record.Config.Headers {
		headerNames = append(headerNames, name)
	}
	slices.Sort(headerNames)
	public := storedConfig{
		RateLimit:         record.Config.RateLimit,
		ID:                record.Config.ID,
		URL:               record.Config.URL,
		Required:          record.Config.Required,
		RequiredScopes:    record.Config.RequiredScopes,
		ToolRules:         record.Config.ToolRules,
		RequestTimeoutNS:  int64(record.Config.RequestTimeout.Duration),
		AllowInsecureHTTP: record.Config.AllowInsecureHTTP,
		HeaderNames:       headerNames,
	}
	secretValues := storedSecrets{Headers: record.Config.Headers}
	if record.Config.OAuth != nil {
		public.OAuth = &storedOAuthConfig{
			Type: record.Config.OAuth.Type, Issuer: record.Config.OAuth.Issuer,
			ClientID: record.Config.OAuth.ClientID, Scopes: record.Config.OAuth.Scopes,
		}
		secretValues.OAuthClientSecret = record.Config.OAuth.ClientSecret
	}
	publicJSON, err := json.Marshal(public)
	if err != nil {
		return nil, nil, fmt.Errorf("encode backend configuration: %w", err)
	}
	secretJSON, err := json.Marshal(secretValues)
	if err != nil {
		return nil, nil, fmt.Errorf("encode backend secrets: %w", err)
	}
	sealed, err := s.seal(record.Config.ID, secretJSON)
	if err != nil {
		return nil, nil, err
	}
	return publicJSON, sealed, nil
}

type rowScanner interface {
	Scan(...any) error
}

func (s *Store) scanRecord(row rowScanner) (Record, error) {
	var (
		record               Record
		id, created, updated string
		publicJSON, sealed   []byte
		enabled              int
		lastProbeAt          sql.NullString
		lastProbeOK          sql.NullInt64
		lastProbeJSON        []byte
	)
	if err := row.Scan(&id, &enabled, &publicJSON, &sealed, &record.Revision, &created, &updated,
		&lastProbeAt, &lastProbeOK, &lastProbeJSON); err != nil {
		return Record{}, err
	}
	var public storedConfig
	if err := json.Unmarshal(publicJSON, &public); err != nil {
		return Record{}, fmt.Errorf("decode backend %q configuration: %w", id, err)
	}
	plain, err := s.open(id, sealed)
	if err != nil {
		return Record{}, err
	}
	var secretValues storedSecrets
	if err := json.Unmarshal(plain, &secretValues); err != nil {
		return Record{}, fmt.Errorf("decode backend %q secrets: %w", id, err)
	}
	record.Config = config.BackendConfig{
		RateLimit: public.RateLimit,
		ID:        public.ID, URL: public.URL, Required: public.Required,
		RequiredScopes: slices.Clone(public.RequiredScopes), ToolRules: slices.Clone(public.ToolRules),
		RequestTimeout:    config.Duration{Duration: time.Duration(public.RequestTimeoutNS)},
		AllowInsecureHTTP: public.AllowInsecureHTTP, Headers: secretValues.Headers,
	}
	if public.OAuth != nil {
		record.Config.OAuth = &config.OAuthConfig{
			Type: public.OAuth.Type, Issuer: public.OAuth.Issuer, ClientID: public.OAuth.ClientID,
			ClientSecret: secretValues.OAuthClientSecret, Scopes: slices.Clone(public.OAuth.Scopes),
		}
	}
	record.Enabled = enabled != 0
	record.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	record.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
	if lastProbeAt.Valid {
		value, _ := time.Parse(time.RFC3339Nano, lastProbeAt.String)
		record.LastProbeAt = &value
	}
	if lastProbeOK.Valid {
		value := lastProbeOK.Int64 != 0
		record.LastProbeOK = &value
	}
	record.LastProbe = slices.Clone(lastProbeJSON)
	return record, nil
}

func (s *Store) ensureCanary(ctx context.Context) error {
	var sealed []byte
	err := s.db.QueryRowContext(ctx, "SELECT value FROM metadata WHERE key = 'key_canary'").Scan(&sealed)
	if errors.Is(err, sql.ErrNoRows) {
		value, err := s.seal("key-canary", []byte("mcphub-config-key-v1"))
		if err != nil {
			return err
		}
		if _, err := s.db.ExecContext(ctx, "INSERT INTO metadata(key, value) VALUES('key_canary', ?)", value); err != nil {
			return fmt.Errorf("write configuration key canary: %w", err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("read configuration key canary: %w", err)
	}
	plain, err := s.open("key-canary", sealed)
	if err != nil || string(plain) != "mcphub-config-key-v1" {
		return fmt.Errorf("configuration database encryption key is incorrect")
	}
	return nil
}

func (s *Store) ensureExistingCanary(ctx context.Context) error {
	var sealed []byte
	if err := s.db.QueryRowContext(ctx, "SELECT value FROM metadata WHERE key = 'key_canary'").Scan(&sealed); err != nil {
		return fmt.Errorf("read configuration key canary: %w", err)
	}
	plain, err := s.open("key-canary", sealed)
	if err != nil || string(plain) != "mcphub-config-key-v1" {
		return fmt.Errorf("configuration database encryption key is incorrect")
	}
	return nil
}

func (s *Store) seal(id string, plain []byte) ([]byte, error) {
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("generate configuration secret nonce: %w", err)
	}
	return s.aead.Seal(nonce, nonce, plain, []byte("mcphub-backend-secrets:"+strings.ToLower(id)+":v1")), nil
}

func (s *Store) open(id string, sealed []byte) ([]byte, error) {
	if len(sealed) < s.aead.NonceSize() {
		return nil, fmt.Errorf("backend %q encrypted secrets are invalid", id)
	}
	nonce := sealed[:s.aead.NonceSize()]
	plain, err := s.aead.Open(nil, nonce, sealed[s.aead.NonceSize():], []byte("mcphub-backend-secrets:"+strings.ToLower(id)+":v1"))
	if err != nil {
		return nil, fmt.Errorf("decrypt backend %q secrets: encryption key is incorrect or data is damaged", id)
	}
	return plain, nil
}

func insertEvent(ctx context.Context, tx *transaction, backendID, action string, success bool, message string, revision int64, now time.Time) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO events(backend_id, source_kind, source_id, actor, action, success, message, revision, created_at)
VALUES(?, 'backend', ?, ?, ?, ?, ?, ?, ?)`, backendID, backendID, Actor(ctx), action, boolInt(success), message, revision, now.Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("record configuration event: %w", err)
	}
	return nil
}

func nextSourceRevision(ctx context.Context, tx *transaction, kind, id string) (int64, error) {
	var revision int64
	err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(revision), 0) + 1 FROM events
		WHERE (source_kind=? AND source_id=? COLLATE NOCASE)
		   OR (?='backend' AND backend_id=? COLLATE NOCASE)`, kind, id, kind, id).Scan(&revision)
	if err != nil {
		return 0, fmt.Errorf("read next %s revision: %w", kind, err)
	}
	return revision, nil
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
