package configstore

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"time"
)

// Parent tables precede their dependents. This fixed inventory is also the
// restore SQL allowlist; a backup cannot name arbitrary tables or columns.
var backupTables = []string{"metadata", "backends", "tool_groups", "http_tools", "openapi_imports", "events", "identities", "local_accounts", "sso_sessions", "sso_refresh", "broker_sessions", "client_grants", "client_authorization_events", "device_authorizations", "device_code_attempts", "approvals", "approval_events", "approval_votes", "approval_operations", "approval_deliveries", "request_records", "credential_bindings", "credential_cleanup"}

type BackupManifest struct {
	Format        int       `json:"format"`
	Schema        string    `json:"schema"`
	Version       string    `json:"version"`
	Engine        string    `json:"engine"`
	KeyID         string    `json:"key_id"`
	Configuration string    `json:"configuration_sha256"`
	CreatedAt     time.Time `json:"created_at"`
	SHA256        string    `json:"sha256"`
	Bytes         int64     `json:"bytes"`
	Rows          int64     `json:"rows"`
}

type backupValue struct {
	Kind  string `json:"kind"`
	Value string `json:"value"`
}
type backupRow struct {
	Table   string        `json:"table"`
	Columns []string      `json:"columns"`
	Values  []backupValue `json:"values"`
}

func EncryptionKeyID(key []byte) string {
	sum := sha256.Sum256(key)
	return hex.EncodeToString(sum[:8])
}

// A single read transaction provides one consistent snapshot on both engines.
// Logical rows keep memory bounded and make restore independent of WAL files.
func (s *Store) Backup(ctx context.Context, directory, version, keyID, configuration string) (manifest BackupManifest, err error) {
	if err = os.Mkdir(directory, 0700); err != nil {
		return manifest, err
	}
	complete := false
	defer func() {
		if !complete {
			_ = os.RemoveAll(directory)
		}
	}()
	f, err := os.OpenFile(filepath.Join(directory, "database.jsonl"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return manifest, err
	}
	defer f.Close()
	opts := &sql.TxOptions{ReadOnly: true}
	if s.db.postgres {
		opts.Isolation = sql.LevelRepeatableRead
	}
	tx, err := s.db.DB.BeginTx(ctx, opts)
	if err != nil {
		return manifest, err
	}
	defer tx.Rollback()
	var schema []byte
	if err = tx.QueryRowContext(ctx, "SELECT value FROM metadata WHERE key='schema_version'").Scan(&schema); err != nil {
		return manifest, err
	}
	manifest = BackupManifest{Format: 1, Schema: string(schema), Version: version, Engine: "sqlite", KeyID: keyID, Configuration: configuration, CreatedAt: time.Now().UTC()}
	if s.db.postgres {
		manifest.Engine = "postgres"
	}
	hash := sha256.New()
	encoder := json.NewEncoder(io.MultiWriter(f, hash))
	for _, table := range backupTables {
		rows, e := tx.QueryContext(ctx, "SELECT * FROM "+table)
		if e != nil {
			return manifest, e
		}
		columns, e := rows.Columns()
		if e != nil {
			rows.Close()
			return manifest, e
		}
		for rows.Next() {
			values := make([]any, len(columns))
			targets := make([]any, len(values))
			for i := range values {
				targets[i] = &values[i]
			}
			if e = rows.Scan(targets...); e != nil {
				rows.Close()
				return manifest, e
			}
			row := backupRow{Table: table, Columns: columns, Values: make([]backupValue, len(values))}
			for i, value := range values {
				switch v := value.(type) {
				case nil:
					row.Values[i].Kind = "null"
				case []byte:
					row.Values[i] = backupValue{"blob", base64.StdEncoding.EncodeToString(v)}
				case string:
					row.Values[i] = backupValue{"text", v}
				case int64:
					row.Values[i] = backupValue{"integer", strconv.FormatInt(v, 10)}
				case bool:
					row.Values[i] = backupValue{"integer", "0"}
					if v {
						row.Values[i].Value = "1"
					}
				default:
					rows.Close()
					return manifest, fmt.Errorf("unsupported backup value in %s", table)
				}
			}
			if e = encoder.Encode(row); e != nil {
				rows.Close()
				return manifest, e
			}
			manifest.Rows++
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return manifest, e
		}
	}
	if err = tx.Commit(); err != nil {
		return manifest, err
	}
	if err = f.Sync(); err != nil {
		return manifest, err
	}
	stat, err := f.Stat()
	if err != nil {
		return manifest, err
	}
	manifest.Bytes, manifest.SHA256 = stat.Size(), hex.EncodeToString(hash.Sum(nil))
	raw, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return manifest, err
	}
	if err = os.WriteFile(filepath.Join(directory, "manifest.json"), raw, 0600); err != nil {
		return manifest, err
	}
	complete = true
	return manifest, nil
}

func VerifyBackup(directory, keyID string) (BackupManifest, error) {
	var manifest BackupManifest
	raw, err := os.ReadFile(filepath.Join(directory, "manifest.json"))
	if err != nil {
		return manifest, err
	}
	if len(raw) > 16384 || json.Unmarshal(raw, &manifest) != nil || manifest.Format != 1 || manifest.Schema != "10" || manifest.KeyID != keyID || manifest.Rows < 1 || manifest.Bytes < 1 {
		return manifest, errors.New("unsupported backup or encryption key mismatch")
	}
	file, err := os.Open(filepath.Join(directory, "database.jsonl"))
	if err != nil {
		return manifest, err
	}
	defer file.Close()
	hash := sha256.New()
	size, err := io.Copy(hash, file)
	if err != nil || size != manifest.Bytes || hex.EncodeToString(hash.Sum(nil)) != manifest.SHA256 {
		return manifest, errors.New("backup checksum mismatch")
	}
	return manifest, nil
}

// Restore accepts only a newly initialized empty database. Security state from
// the snapshot is invalidated inside the same transaction as the restored rows.
func (s *Store) RestoreBackup(ctx context.Context, directory, keyID string) (BackupManifest, error) {
	manifest, err := VerifyBackup(directory, keyID)
	if err != nil {
		return manifest, err
	}
	if (manifest.Engine == "postgres") != s.db.postgres || !slices.Contains([]string{"sqlite", "postgres"}, manifest.Engine) {
		return manifest, errors.New("backup engine differs from destination")
	}
	file, err := os.Open(filepath.Join(directory, "database.jsonl"))
	if err != nil {
		return manifest, err
	}
	defer file.Close()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return manifest, err
	}
	defer tx.Rollback()
	columns := map[string][]string{}
	for _, table := range backupTables {
		var count int64
		if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil {
			return manifest, err
		}
		if table != "metadata" && count != 0 {
			return manifest, errors.New("restore requires an empty destination database")
		}
		rows, e := tx.QueryContext(ctx, "SELECT * FROM "+table+" LIMIT 0")
		if e != nil {
			return manifest, e
		}
		columns[table], e = rows.Columns()
		rows.Close()
		if e != nil {
			return manifest, e
		}
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM metadata"); err != nil {
		return manifest, err
	}
	decoder := json.NewDecoder(io.LimitReader(file, manifest.Bytes+1))
	var count int64
	hash := sha256.New()
	if _, err = file.Seek(0, 0); err != nil {
		return manifest, err
	}
	decoder = json.NewDecoder(io.TeeReader(io.LimitReader(file, manifest.Bytes+1), hash))
	for {
		var row backupRow
		err = decoder.Decode(&row)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return manifest, err
		}
		if !slices.Contains(backupTables, row.Table) || !slices.Equal(row.Columns, columns[row.Table]) || len(row.Values) != len(row.Columns) {
			return manifest, errors.New("invalid backup row schema")
		}
		values := make([]any, len(row.Values))
		placeholders := ""
		for i, v := range row.Values {
			if i > 0 {
				placeholders += ","
			}
			placeholders += "?"
			switch v.Kind {
			case "null":
				values[i] = nil
			case "text":
				values[i] = v.Value
			case "blob":
				values[i], err = base64.StdEncoding.DecodeString(v.Value)
			case "integer":
				values[i], err = strconv.ParseInt(v.Value, 10, 64)
			default:
				err = errors.New("invalid backup value")
			}
			if err != nil {
				return manifest, err
			}
		}
		// Column order is verified against the destination schema; it is never SQL.
		if _, err = tx.ExecContext(ctx, "INSERT INTO "+row.Table+" VALUES("+placeholders+")", values...); err != nil {
			return manifest, fmt.Errorf("restore %s: %w", row.Table, err)
		}
		count++
	}
	if count != manifest.Rows || hex.EncodeToString(hash.Sum(nil)) != manifest.SHA256 {
		return manifest, errors.New("backup changed during restore")
	}
	var canary []byte
	if err = tx.QueryRowContext(ctx, "SELECT value FROM metadata WHERE key='key_canary'").Scan(&canary); err != nil {
		return manifest, err
	}
	plain, e := s.open("key-canary", canary)
	if e != nil || string(plain) != "mcphub-config-key-v1" {
		return manifest, errors.New("backup encryption key mismatch")
	}
	for _, query := range []string{"DELETE FROM sso_refresh", "DELETE FROM sso_sessions", "DELETE FROM metadata WHERE key='sso_signing_key'", "UPDATE broker_sessions SET status='revoked'", "UPDATE client_grants SET status='revoked',revision=revision+1,exchange_hash=NULL", "UPDATE device_authorizations SET status='denied' WHERE status NOT IN ('completed','denied')", "UPDATE approvals SET status='revoked' WHERE status IN ('pending','approved','executing')"} {
		if _, err = tx.ExecContext(ctx, query); err != nil {
			return manifest, err
		}
	}
	if s.db.postgres {
		for _, table := range []string{"events", "request_records"} {
			if _, err = tx.ExecContext(ctx, "SELECT setval(pg_get_serial_sequence('"+table+"','id'), COALESCE(MAX(id),1), COUNT(*)>0) FROM "+table); err != nil {
				return manifest, err
			}
		}
	}
	if err = insertSourceEvent(ctx, tx, "recovery", "database", "backup_restored_security_revoked", true, "", 0, time.Now().UTC()); err != nil {
		return manifest, err
	}
	if err = tx.Commit(); err != nil {
		return manifest, err
	}
	return manifest, nil
}

func (s *Store) RecordOperation(ctx context.Context, kind string, value any) error {
	if !slices.Contains([]string{"backup", "recovery_drill", "recovery"}, kind) {
		return errors.New("invalid operation")
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, "INSERT INTO metadata(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", "operation:"+kind, raw)
	return err
}

func (s *Store) Operations(ctx context.Context) (map[string]json.RawMessage, error) {
	values := map[string]json.RawMessage{}
	rows, err := s.db.QueryContext(ctx, "SELECT key,value FROM metadata WHERE key LIKE 'operation:%'")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var key string
		var raw []byte
		if err = rows.Scan(&key, &raw); err != nil {
			return nil, err
		}
		values[key[len("operation:"):]] = json.RawMessage(raw)
	}
	return values, rows.Err()
}
