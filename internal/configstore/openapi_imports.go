package configstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/SamuelSupe/mcphub/internal/openapiimport"
)

type OpenAPIImportRecord struct {
	GroupID            string
	Config             openapiimport.ImportConfig
	Document           []byte
	SHA256             string
	Revision           int64
	CreatedAt          time.Time
	UpdatedAt          time.Time
	LastRefreshAt      *time.Time
	LastRefreshOK      *bool
	LastRefreshMessage string
}

func (s *Store) ListOpenAPIImports(ctx context.Context, groupID string) ([]OpenAPIImportRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT group_id, id, config_json, spec_document, spec_sha256, revision,
created_at, updated_at, last_refresh_at, last_refresh_ok, last_refresh_message
FROM openapi_imports WHERE group_id=? COLLATE NOCASE ORDER BY id COLLATE NOCASE`, groupID)
	if err != nil {
		return nil, fmt.Errorf("list OpenAPI imports: %w", err)
	}
	defer rows.Close()
	var records []OpenAPIImportRecord
	for rows.Next() {
		record, err := scanOpenAPIImport(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, rows.Err()
}

func (s *Store) GetOpenAPIImport(ctx context.Context, groupID, id string) (OpenAPIImportRecord, error) {
	record, err := scanOpenAPIImport(s.db.QueryRowContext(ctx, `SELECT group_id, id, config_json, spec_document, spec_sha256, revision,
created_at, updated_at, last_refresh_at, last_refresh_ok, last_refresh_message
FROM openapi_imports WHERE group_id=? COLLATE NOCASE AND id=? COLLATE NOCASE`, groupID, id))
	if errors.Is(err, sql.ErrNoRows) {
		return OpenAPIImportRecord{}, ErrNotFound
	}
	return record, err
}

func (s *Store) CreateOpenAPIImport(ctx context.Context, record OpenAPIImportRecord, tools []HTTPToolRecord) (OpenAPIImportRecord, []HTTPToolRecord, error) {
	now := time.Now().UTC()
	record.CreatedAt, record.UpdatedAt = now, now
	encoded, err := json.Marshal(record.Config)
	if err != nil {
		return OpenAPIImportRecord{}, nil, fmt.Errorf("encode OpenAPI import: %w", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return OpenAPIImportRecord{}, nil, fmt.Errorf("begin OpenAPI import create: %w", err)
	}
	defer tx.Rollback()
	record.Revision, err = nextSourceRevision(ctx, tx, "openapi_import", record.GroupID+"/"+record.Config.ID)
	if err != nil {
		return OpenAPIImportRecord{}, nil, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO openapi_imports(group_id, id, config_json, spec_document, spec_sha256, revision, created_at, updated_at)
VALUES(?, ?, ?, ?, ?, ?, ?, ?)`, record.GroupID, record.Config.ID, encoded, record.Document, record.SHA256, record.Revision,
		now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return OpenAPIImportRecord{}, nil, ErrConflict
		}
		return OpenAPIImportRecord{}, nil, fmt.Errorf("insert OpenAPI import: %w", err)
	}
	createdTools := make([]HTTPToolRecord, len(tools))
	for index, tool := range tools {
		tool.GroupID, tool.Revision, tool.CreatedAt, tool.UpdatedAt = record.GroupID, 1, now, now
		toolJSON, err := json.Marshal(tool.Config)
		if err != nil {
			return OpenAPIImportRecord{}, nil, fmt.Errorf("encode imported HTTP tool: %w", err)
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO http_tools(group_id, name, config_json, revision, created_at, updated_at)
VALUES(?, ?, ?, 1, ?, ?)`, tool.GroupID, tool.Config.Name, toolJSON, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
		if err != nil {
			if strings.Contains(strings.ToLower(err.Error()), "unique") {
				return OpenAPIImportRecord{}, nil, ErrConflict
			}
			return OpenAPIImportRecord{}, nil, fmt.Errorf("insert imported HTTP tool: %w", err)
		}
		createdTools[index] = tool
	}
	if err := insertSourceEvent(ctx, tx, "openapi_import", record.GroupID+"/"+record.Config.ID, "create", true, "", record.Revision, now); err != nil {
		return OpenAPIImportRecord{}, nil, err
	}
	if err := tx.Commit(); err != nil {
		return OpenAPIImportRecord{}, nil, fmt.Errorf("commit OpenAPI import create: %w", err)
	}
	return record, createdTools, nil
}

func (s *Store) ReplaceOpenAPIImport(ctx context.Context, record OpenAPIImportRecord, expected int64, tools []HTTPToolRecord, action string) (OpenAPIImportRecord, []HTTPToolRecord, error) {
	current, err := s.GetOpenAPIImport(ctx, record.GroupID, record.Config.ID)
	if err != nil {
		return OpenAPIImportRecord{}, nil, err
	}
	if current.Revision != expected {
		return OpenAPIImportRecord{}, nil, ErrConflict
	}
	now := time.Now().UTC()
	record.Revision, record.CreatedAt, record.UpdatedAt = expected+1, current.CreatedAt, now
	ok := true
	record.LastRefreshAt, record.LastRefreshOK, record.LastRefreshMessage = &now, &ok, ""
	encoded, err := json.Marshal(record.Config)
	if err != nil {
		return OpenAPIImportRecord{}, nil, fmt.Errorf("encode OpenAPI import: %w", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return OpenAPIImportRecord{}, nil, fmt.Errorf("begin OpenAPI import replace: %w", err)
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE openapi_imports SET config_json=?, spec_document=?, spec_sha256=?, revision=?, updated_at=?,
last_refresh_at=?, last_refresh_ok=1, last_refresh_message='' WHERE group_id=? COLLATE NOCASE AND id=? COLLATE NOCASE AND revision=?`,
		encoded, record.Document, record.SHA256, record.Revision, now.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano),
		record.GroupID, record.Config.ID, expected)
	if err != nil {
		return OpenAPIImportRecord{}, nil, fmt.Errorf("update OpenAPI import: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return OpenAPIImportRecord{}, nil, ErrConflict
	}
	existingTools, err := importedToolsTx(ctx, tx, record.GroupID, record.Config.ID)
	if err != nil {
		return OpenAPIImportRecord{}, nil, err
	}
	if err := deleteImportedToolsTx(ctx, tx, record.GroupID, record.Config.ID); err != nil {
		return OpenAPIImportRecord{}, nil, err
	}
	createdTools := make([]HTTPToolRecord, len(tools))
	for index, tool := range tools {
		tool.GroupID, tool.Revision, tool.CreatedAt, tool.UpdatedAt = record.GroupID, 1, now, now
		if previous, exists := existingTools[strings.ToLower(tool.Config.Name)]; exists {
			tool.Revision = previous.Revision + 1
			tool.CreatedAt = previous.CreatedAt
		}
		toolJSON, _ := json.Marshal(tool.Config)
		_, err := tx.ExecContext(ctx, `INSERT INTO http_tools(group_id, name, config_json, revision, created_at, updated_at)
VALUES(?, ?, ?, ?, ?, ?)`, tool.GroupID, tool.Config.Name, toolJSON, tool.Revision,
			tool.CreatedAt.Format(time.RFC3339Nano), now.Format(time.RFC3339Nano))
		if err != nil {
			if strings.Contains(strings.ToLower(err.Error()), "unique") {
				return OpenAPIImportRecord{}, nil, ErrConflict
			}
			return OpenAPIImportRecord{}, nil, fmt.Errorf("replace imported HTTP tool: %w", err)
		}
		createdTools[index] = tool
	}
	if err := insertSourceEvent(ctx, tx, "openapi_import", record.GroupID+"/"+record.Config.ID, action, true, "", record.Revision, now); err != nil {
		return OpenAPIImportRecord{}, nil, err
	}
	if err := tx.Commit(); err != nil {
		return OpenAPIImportRecord{}, nil, fmt.Errorf("commit OpenAPI import replace: %w", err)
	}
	return record, createdTools, nil
}

func (s *Store) DeleteOpenAPIImport(ctx context.Context, groupID, id string, expected int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin OpenAPI import delete: %w", err)
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `DELETE FROM openapi_imports WHERE group_id=? COLLATE NOCASE AND id=? COLLATE NOCASE AND revision=?`, groupID, id, expected)
	if err != nil {
		return fmt.Errorf("delete OpenAPI import: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM openapi_imports WHERE group_id=? COLLATE NOCASE AND id=? COLLATE NOCASE`, groupID, id).Scan(&exists); errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return ErrConflict
	}
	if err := deleteImportedToolsTx(ctx, tx, groupID, id); err != nil {
		return err
	}
	now := time.Now().UTC()
	if err := insertSourceEvent(ctx, tx, "openapi_import", groupID+"/"+id, "delete", true, "", expected+1, now); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) MarkOpenAPIRefreshFailure(ctx context.Context, groupID, id string, expected int64, message string) error {
	now := time.Now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var previous sql.NullInt64
	var revision int64
	if err := tx.QueryRowContext(ctx, `SELECT last_refresh_ok, revision FROM openapi_imports
		WHERE group_id=? COLLATE NOCASE AND id=? COLLATE NOCASE AND revision=?`, groupID, id, expected).Scan(&previous, &revision); errors.Is(err, sql.ErrNoRows) {
		return ErrConflict
	} else if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE openapi_imports SET last_refresh_at=?, last_refresh_ok=0, last_refresh_message=?
		WHERE group_id=? COLLATE NOCASE AND id=? COLLATE NOCASE AND revision=?`, now.Format(time.RFC3339Nano), message, groupID, id, expected)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return ErrConflict
	}
	if !previous.Valid || previous.Int64 != 0 {
		if err := insertSourceEvent(ctx, tx, "openapi_import", groupID+"/"+id, "refresh_failed", false, message, revision, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) MarkOpenAPIRefreshSuccess(ctx context.Context, groupID, id string, expected int64) error {
	now := time.Now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var previous sql.NullInt64
	var revision int64
	if err := tx.QueryRowContext(ctx, `SELECT last_refresh_ok, revision FROM openapi_imports
		WHERE group_id=? COLLATE NOCASE AND id=? COLLATE NOCASE AND revision=?`, groupID, id, expected).Scan(&previous, &revision); errors.Is(err, sql.ErrNoRows) {
		return ErrConflict
	} else if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE openapi_imports SET last_refresh_at=?, last_refresh_ok=1, last_refresh_message=''
		WHERE group_id=? COLLATE NOCASE AND id=? COLLATE NOCASE AND revision=?`, now.Format(time.RFC3339Nano), groupID, id, expected)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return ErrConflict
	}
	if previous.Valid && previous.Int64 == 0 {
		if err := insertSourceEvent(ctx, tx, "openapi_import", groupID+"/"+id, "refresh_recovered", true, "", revision, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func deleteImportedToolsTx(ctx context.Context, tx *sql.Tx, groupID, importID string) error {
	rows, err := tx.QueryContext(ctx, `SELECT name, config_json FROM http_tools WHERE group_id=? COLLATE NOCASE`, groupID)
	if err != nil {
		return fmt.Errorf("list imported HTTP tools: %w", err)
	}
	var names []string
	for rows.Next() {
		var name string
		var encoded []byte
		if err := rows.Scan(&name, &encoded); err != nil {
			_ = rows.Close()
			return err
		}
		var value struct {
			ImportID string `json:"import_id"`
		}
		if json.Unmarshal(encoded, &value) == nil && strings.EqualFold(value.ImportID, importID) {
			names = append(names, name)
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("list imported HTTP tools: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("list imported HTTP tools: %w", err)
	}
	for _, name := range names {
		if _, err := tx.ExecContext(ctx, `DELETE FROM http_tools WHERE group_id=? COLLATE NOCASE AND name=? COLLATE NOCASE`, groupID, name); err != nil {
			return fmt.Errorf("delete imported HTTP tool: %w", err)
		}
	}
	return nil
}

func importedToolsTx(ctx context.Context, tx *sql.Tx, groupID, importID string) (map[string]HTTPToolRecord, error) {
	rows, err := tx.QueryContext(ctx, `SELECT group_id, name, config_json, revision, created_at, updated_at
FROM http_tools WHERE group_id=? COLLATE NOCASE`, groupID)
	if err != nil {
		return nil, fmt.Errorf("list imported HTTP tools: %w", err)
	}
	defer rows.Close()
	result := make(map[string]HTTPToolRecord)
	for rows.Next() {
		record, err := scanHTTPTool(rows)
		if err != nil {
			return nil, err
		}
		if strings.EqualFold(record.Config.ImportID, importID) {
			result[strings.ToLower(record.Config.Name)] = record
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list imported HTTP tools: %w", err)
	}
	return result, nil
}

func scanOpenAPIImport(row rowScanner) (OpenAPIImportRecord, error) {
	var record OpenAPIImportRecord
	var id, created, updated string
	var encoded []byte
	var refreshed sql.NullString
	var refreshOK sql.NullInt64
	if err := row.Scan(&record.GroupID, &id, &encoded, &record.Document, &record.SHA256, &record.Revision,
		&created, &updated, &refreshed, &refreshOK, &record.LastRefreshMessage); err != nil {
		return OpenAPIImportRecord{}, err
	}
	if err := json.Unmarshal(encoded, &record.Config); err != nil {
		return OpenAPIImportRecord{}, fmt.Errorf("decode OpenAPI import %q/%q: %w", record.GroupID, id, err)
	}
	record.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	record.UpdatedAt, _ = time.Parse(time.RFC3339Nano, updated)
	if refreshed.Valid {
		value, _ := time.Parse(time.RFC3339Nano, refreshed.String)
		record.LastRefreshAt = &value
	}
	if refreshOK.Valid {
		value := refreshOK.Int64 != 0
		record.LastRefreshOK = &value
	}
	return record, nil
}
