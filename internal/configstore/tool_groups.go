package configstore

import (
	"bytes"
	"context"
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
	"github.com/SamuelSupe/mcphub/internal/httptool"
)

type ToolGroupRecord struct {
	Config      httptool.GroupConfig
	Revision    int64
	CreatedAt   time.Time
	UpdatedAt   time.Time
	LastProbeAt *time.Time
	LastProbeOK *bool
	LastProbe   json.RawMessage
}

type HTTPToolRecord struct {
	GroupID   string
	Config    httptool.ToolConfig
	Revision  int64
	CreatedAt time.Time
	UpdatedAt time.Time
}

type storedToolGroup struct {
	ID                   string             `json:"id"`
	BaseURL              string             `json:"base_url"`
	RequiredScopes       []string           `json:"required_scopes,omitempty"`
	ToolRules            []config.ToolRule  `json:"tool_rules,omitempty"`
	RequestTimeoutNS     int64              `json:"request_timeout_ns"`
	MaxResponseBodyBytes int64              `json:"max_response_body_bytes"`
	HeaderNames          []string           `json:"header_names,omitempty"`
	OAuth                *storedOAuthConfig `json:"oauth,omitempty"`
}

func (s *Store) ListToolGroups(ctx context.Context) ([]ToolGroupRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, enabled, config_json, secrets, revision, created_at, updated_at,
last_probe_at, last_probe_ok, last_probe_json FROM tool_groups ORDER BY id COLLATE NOCASE`)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "no such table") {
			return nil, nil
		}
		return nil, fmt.Errorf("list tool groups: %w", err)
	}
	var records []ToolGroupRecord
	for rows.Next() {
		record, err := s.scanToolGroup(rows)
		if err != nil {
			_ = rows.Close()
			return nil, err
		}
		records = append(records, record)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("list tool groups: %w", err)
	}
	for index := range records {
		tools, err := s.ListHTTPTools(ctx, records[index].Config.ID)
		if err != nil {
			return nil, err
		}
		records[index].Config.Tools = make([]httptool.ToolConfig, len(tools))
		for toolIndex, tool := range tools {
			records[index].Config.Tools[toolIndex] = tool.Config
		}
	}
	return records, nil
}

func (s *Store) GetToolGroup(ctx context.Context, id string) (ToolGroupRecord, error) {
	record, err := s.scanToolGroup(s.db.QueryRowContext(ctx, `SELECT id, enabled, config_json, secrets, revision, created_at, updated_at,
last_probe_at, last_probe_ok, last_probe_json FROM tool_groups WHERE id=? COLLATE NOCASE`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return ToolGroupRecord{}, ErrNotFound
	}
	if err != nil {
		return ToolGroupRecord{}, err
	}
	tools, err := s.ListHTTPTools(ctx, record.Config.ID)
	if err != nil {
		return ToolGroupRecord{}, err
	}
	record.Config.Tools = make([]httptool.ToolConfig, len(tools))
	for index, tool := range tools {
		record.Config.Tools[index] = tool.Config
	}
	return record, nil
}

func (s *Store) CreateToolGroup(ctx context.Context, record ToolGroupRecord) (ToolGroupRecord, error) {
	record.Config.Tools = nil
	record.CreatedAt = time.Now().UTC()
	record.UpdatedAt = record.CreatedAt
	public, secrets, err := s.encodeToolGroup(record.Config)
	if err != nil {
		return ToolGroupRecord{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ToolGroupRecord{}, fmt.Errorf("begin tool group create: %w", err)
	}
	defer tx.Rollback()
	var existing int
	if err := tx.QueryRowContext(ctx, "SELECT 1 FROM backends WHERE id=? COLLATE NOCASE", record.Config.ID).Scan(&existing); err == nil {
		return ToolGroupRecord{}, ErrConflict
	} else if !errors.Is(err, sql.ErrNoRows) {
		return ToolGroupRecord{}, fmt.Errorf("check tool group identity: %w", err)
	}
	record.Revision, err = nextSourceRevision(ctx, tx, "tool_group", record.Config.ID)
	if err != nil {
		return ToolGroupRecord{}, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO tool_groups(id, enabled, config_json, secrets, revision, created_at, updated_at)
VALUES(?, ?, ?, ?, ?, ?, ?)`, record.Config.ID, boolInt(record.Config.Enabled), public, secrets, record.Revision,
		record.CreatedAt.Format(time.RFC3339Nano), record.UpdatedAt.Format(time.RFC3339Nano))
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return ToolGroupRecord{}, ErrConflict
		}
		return ToolGroupRecord{}, fmt.Errorf("insert tool group: %w", err)
	}
	if err := insertSourceEvent(ctx, tx, "tool_group", record.Config.ID, "create", true, "", record.Revision, record.CreatedAt); err != nil {
		return ToolGroupRecord{}, err
	}
	if err := tx.Commit(); err != nil {
		return ToolGroupRecord{}, fmt.Errorf("commit tool group create: %w", err)
	}
	return record, nil
}

func (s *Store) UpdateToolGroup(ctx context.Context, record ToolGroupRecord, expected int64) (ToolGroupRecord, error) {
	current, err := s.GetToolGroup(ctx, record.Config.ID)
	if err != nil {
		return ToolGroupRecord{}, err
	}
	if current.Revision != expected {
		return ToolGroupRecord{}, ErrConflict
	}
	record.Config.Tools = current.Config.Tools
	record.Revision = expected + 1
	record.CreatedAt = current.CreatedAt
	record.UpdatedAt = time.Now().UTC()
	record.LastProbeAt, record.LastProbeOK, record.LastProbe = current.LastProbeAt, current.LastProbeOK, slices.Clone(current.LastProbe)
	public, secrets, err := s.encodeToolGroup(record.Config)
	if err != nil {
		return ToolGroupRecord{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ToolGroupRecord{}, fmt.Errorf("begin tool group update: %w", err)
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE tool_groups SET enabled=?, config_json=?, secrets=?, revision=?, updated_at=?
WHERE id=? COLLATE NOCASE AND revision=?`, boolInt(record.Config.Enabled), public, secrets, record.Revision,
		record.UpdatedAt.Format(time.RFC3339Nano), record.Config.ID, expected)
	if err != nil {
		return ToolGroupRecord{}, fmt.Errorf("update tool group: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return ToolGroupRecord{}, ErrConflict
	}
	if err := insertSourceEvent(ctx, tx, "tool_group", record.Config.ID, "update", true, "", record.Revision, record.UpdatedAt); err != nil {
		return ToolGroupRecord{}, err
	}
	if err := tx.Commit(); err != nil {
		return ToolGroupRecord{}, fmt.Errorf("commit tool group update: %w", err)
	}
	return record, nil
}

func (s *Store) DeleteToolGroup(ctx context.Context, id string, expected int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tool group delete: %w", err)
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, "DELETE FROM tool_groups WHERE id=? COLLATE NOCASE AND revision=?", id, expected)
	if err != nil {
		return fmt.Errorf("delete tool group: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		var exists int
		if err := tx.QueryRowContext(ctx, "SELECT 1 FROM tool_groups WHERE id=? COLLATE NOCASE", id).Scan(&exists); errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return ErrConflict
	}
	now := time.Now().UTC()
	if err := insertSourceEvent(ctx, tx, "tool_group", id, "delete", true, "", expected+1, now); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ListHTTPTools(ctx context.Context, groupID string) ([]HTTPToolRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT group_id, name, config_json, revision, created_at, updated_at
FROM http_tools WHERE group_id=? COLLATE NOCASE ORDER BY name COLLATE NOCASE`, groupID)
	if err != nil {
		return nil, fmt.Errorf("list HTTP tools: %w", err)
	}
	defer rows.Close()
	var records []HTTPToolRecord
	for rows.Next() {
		record, err := scanHTTPTool(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

func (s *Store) GetHTTPTool(ctx context.Context, groupID, name string) (HTTPToolRecord, error) {
	record, err := scanHTTPTool(s.db.QueryRowContext(ctx, `SELECT group_id, name, config_json, revision, created_at, updated_at
FROM http_tools WHERE group_id=? COLLATE NOCASE AND name=? COLLATE NOCASE`, groupID, name))
	if errors.Is(err, sql.ErrNoRows) {
		return HTTPToolRecord{}, ErrNotFound
	}
	return record, err
}

func (s *Store) CreateHTTPTool(ctx context.Context, record HTTPToolRecord) (HTTPToolRecord, error) {
	record.CreatedAt = time.Now().UTC()
	record.UpdatedAt = record.CreatedAt
	encoded, err := json.Marshal(record.Config)
	if err != nil {
		return HTTPToolRecord{}, fmt.Errorf("encode HTTP tool: %w", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return HTTPToolRecord{}, fmt.Errorf("begin HTTP tool create: %w", err)
	}
	defer tx.Rollback()
	record.Revision, err = nextSourceRevision(ctx, tx, "http_tool", record.GroupID+"."+record.Config.Name)
	if err != nil {
		return HTTPToolRecord{}, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO http_tools(group_id, name, config_json, revision, created_at, updated_at)
VALUES(?, ?, ?, ?, ?, ?)`, record.GroupID, record.Config.Name, encoded, record.Revision,
		record.CreatedAt.Format(time.RFC3339Nano), record.UpdatedAt.Format(time.RFC3339Nano))
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return HTTPToolRecord{}, ErrConflict
		}
		return HTTPToolRecord{}, fmt.Errorf("insert HTTP tool: %w", err)
	}
	if err := insertSourceEvent(ctx, tx, "http_tool", record.GroupID+"."+record.Config.Name, "create", true, "", record.Revision, record.CreatedAt); err != nil {
		return HTTPToolRecord{}, err
	}
	if err := tx.Commit(); err != nil {
		return HTTPToolRecord{}, fmt.Errorf("commit HTTP tool create: %w", err)
	}
	return record, nil
}

func (s *Store) UpdateHTTPTool(ctx context.Context, record HTTPToolRecord, expected int64) (HTTPToolRecord, error) {
	current, err := s.GetHTTPTool(ctx, record.GroupID, record.Config.Name)
	if err != nil {
		return HTTPToolRecord{}, err
	}
	if current.Revision != expected {
		return HTTPToolRecord{}, ErrConflict
	}
	record.Revision = expected + 1
	record.CreatedAt = current.CreatedAt
	record.UpdatedAt = time.Now().UTC()
	encoded, err := json.Marshal(record.Config)
	if err != nil {
		return HTTPToolRecord{}, fmt.Errorf("encode HTTP tool: %w", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return HTTPToolRecord{}, fmt.Errorf("begin HTTP tool update: %w", err)
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE http_tools SET config_json=?, revision=?, updated_at=?
WHERE group_id=? COLLATE NOCASE AND name=? COLLATE NOCASE AND revision=?`, encoded, record.Revision,
		record.UpdatedAt.Format(time.RFC3339Nano), record.GroupID, record.Config.Name, expected)
	if err != nil {
		return HTTPToolRecord{}, fmt.Errorf("update HTTP tool: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return HTTPToolRecord{}, ErrConflict
	}
	if err := insertSourceEvent(ctx, tx, "http_tool", record.GroupID+"."+record.Config.Name, "update", true, "", record.Revision, record.UpdatedAt); err != nil {
		return HTTPToolRecord{}, err
	}
	if err := tx.Commit(); err != nil {
		return HTTPToolRecord{}, fmt.Errorf("commit HTTP tool update: %w", err)
	}
	return record, nil
}

func (s *Store) DeleteHTTPTool(ctx context.Context, groupID, name string, expected int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin HTTP tool delete: %w", err)
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `DELETE FROM http_tools WHERE group_id=? COLLATE NOCASE AND name=? COLLATE NOCASE AND revision=?`, groupID, name, expected)
	if err != nil {
		return fmt.Errorf("delete HTTP tool: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM http_tools WHERE group_id=? COLLATE NOCASE AND name=? COLLATE NOCASE`, groupID, name).Scan(&exists); errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return ErrConflict
	}
	now := time.Now().UTC()
	if err := insertSourceEvent(ctx, tx, "http_tool", groupID+"."+name, "delete", true, "", expected+1, now); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) UpdateToolGroupProbe(ctx context.Context, id string, ok bool, result json.RawMessage) error {
	now := time.Now().UTC()
	updated, err := s.db.ExecContext(ctx, `UPDATE tool_groups SET last_probe_at=?, last_probe_ok=?, last_probe_json=?
WHERE id=? COLLATE NOCASE`, now.Format(time.RFC3339Nano), boolInt(ok), []byte(result), id)
	if err != nil {
		return fmt.Errorf("update tool group probe: %w", err)
	}
	if changed, _ := updated.RowsAffected(); changed != 1 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) encodeToolGroup(group httptool.GroupConfig) ([]byte, []byte, error) {
	names := make([]string, 0, len(group.Headers))
	for name := range group.Headers {
		names = append(names, name)
	}
	slices.Sort(names)
	public := storedToolGroup{
		ID: group.ID, BaseURL: group.BaseURL, RequiredScopes: group.RequiredScopes, ToolRules: group.ToolRules,
		RequestTimeoutNS: int64(group.RequestTimeout), MaxResponseBodyBytes: group.MaxResponseBodyBytes, HeaderNames: names,
	}
	secrets := storedSecrets{Headers: group.Headers}
	if group.OAuth != nil {
		public.OAuth = &storedOAuthConfig{Type: group.OAuth.Type, Issuer: group.OAuth.Issuer, ClientID: group.OAuth.ClientID, Scopes: group.OAuth.Scopes}
		secrets.OAuthClientSecret = group.OAuth.ClientSecret
	}
	publicJSON, err := json.Marshal(public)
	if err != nil {
		return nil, nil, fmt.Errorf("encode tool group configuration: %w", err)
	}
	secretJSON, err := json.Marshal(secrets)
	if err != nil {
		return nil, nil, fmt.Errorf("encode tool group secrets: %w", err)
	}
	sealed, err := s.sealSource("tool-group", group.ID, secretJSON)
	return publicJSON, sealed, err
}

func (s *Store) scanToolGroup(row rowScanner) (ToolGroupRecord, error) {
	var record ToolGroupRecord
	var id, created, updated string
	var publicJSON, sealed, probe []byte
	var enabled int
	var probeAt sql.NullString
	var probeOK sql.NullInt64
	if err := row.Scan(&id, &enabled, &publicJSON, &sealed, &record.Revision, &created, &updated, &probeAt, &probeOK, &probe); err != nil {
		return ToolGroupRecord{}, err
	}
	var public storedToolGroup
	if err := json.Unmarshal(publicJSON, &public); err != nil {
		return ToolGroupRecord{}, fmt.Errorf("decode tool group %q configuration: %w", id, err)
	}
	plain, err := s.openSource("tool-group", id, sealed)
	if err != nil {
		return ToolGroupRecord{}, err
	}
	var secrets storedSecrets
	if err := json.Unmarshal(plain, &secrets); err != nil {
		return ToolGroupRecord{}, fmt.Errorf("decode tool group %q secrets: %w", id, err)
	}
	record.Config = httptool.GroupConfig{
		ID: public.ID, BaseURL: public.BaseURL, Enabled: enabled != 0, RequiredScopes: slices.Clone(public.RequiredScopes),
		ToolRules: slices.Clone(public.ToolRules), RequestTimeout: time.Duration(public.RequestTimeoutNS),
		MaxResponseBodyBytes: public.MaxResponseBodyBytes, Headers: secrets.Headers,
	}
	if public.OAuth != nil {
		record.Config.OAuth = &config.OAuthConfig{Type: public.OAuth.Type, Issuer: public.OAuth.Issuer, ClientID: public.OAuth.ClientID, ClientSecret: secrets.OAuthClientSecret, Scopes: slices.Clone(public.OAuth.Scopes)}
	}
	record.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	record.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
	if probeAt.Valid {
		value, _ := time.Parse(time.RFC3339Nano, probeAt.String)
		record.LastProbeAt = &value
	}
	if probeOK.Valid {
		value := probeOK.Int64 != 0
		record.LastProbeOK = &value
	}
	record.LastProbe = slices.Clone(probe)
	return record, nil
}

func scanHTTPTool(row rowScanner) (HTTPToolRecord, error) {
	var record HTTPToolRecord
	var name, created, updated string
	var encoded []byte
	if err := row.Scan(&record.GroupID, &name, &encoded, &record.Revision, &created, &updated); err != nil {
		return HTTPToolRecord{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	if err := decoder.Decode(&record.Config); err != nil {
		return HTTPToolRecord{}, fmt.Errorf("decode HTTP tool %q.%q: %w", record.GroupID, name, err)
	}
	record.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	record.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
	return record, nil
}

func (s *Store) sealSource(kind, id string, plain []byte) ([]byte, error) {
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("generate configuration secret nonce: %w", err)
	}
	aad := []byte("mcphub-" + kind + "-secrets:" + strings.ToLower(id) + ":v1")
	return s.aead.Seal(nonce, nonce, plain, aad), nil
}

func (s *Store) openSource(kind, id string, sealed []byte) ([]byte, error) {
	if len(sealed) < s.aead.NonceSize() {
		return nil, fmt.Errorf("%s %q encrypted secrets are invalid", kind, id)
	}
	nonce := sealed[:s.aead.NonceSize()]
	aad := []byte("mcphub-" + kind + "-secrets:" + strings.ToLower(id) + ":v1")
	plain, err := s.aead.Open(nil, nonce, sealed[s.aead.NonceSize():], aad)
	if err != nil {
		return nil, fmt.Errorf("decrypt %s %q secrets: encryption key is incorrect or data is damaged", kind, id)
	}
	return plain, nil
}

func insertSourceEvent(ctx context.Context, tx *sql.Tx, kind, id, action string, success bool, message string, revision int64, now time.Time) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO events(source_kind, source_id, actor, action, success, message, revision, created_at)
VALUES(?, ?, 'local', ?, ?, ?, ?, ?)`, kind, id, action, boolInt(success), message, revision, now.Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("record configuration event: %w", err)
	}
	return nil
}
