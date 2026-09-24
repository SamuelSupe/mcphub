package configstore

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	_ "embed"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

//go:embed postgres.sql
var postgresSchema string

var ErrUninitialized = errors.New("configuration database has not been initialized")

func OpenPostgres(ctx context.Context, dsn string, key []byte, readOnly bool) (*Store, error) {
	if len(key) != 32 {
		return nil, errors.New("configuration encryption key must be 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	connection, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, errors.New("invalid PostgreSQL connection configuration")
	}
	if connection.ConnectTimeout == 0 {
		connection.ConnectTimeout = 10 * time.Second
	}
	if readOnly {
		connection.RuntimeParams["default_transaction_read_only"] = "on"
	}
	raw := stdlib.OpenDB(*connection)
	raw.SetMaxOpenConns(4)
	raw.SetMaxIdleConns(2)
	store := &Store{db: &database{DB: raw, postgres: true}, aead: aead}
	closeOnError := func(err error) (*Store, error) { _ = raw.Close(); return nil, err }
	if err := raw.PingContext(ctx); err != nil {
		// Driver connection errors may contain the DSN, username or password.
		return closeOnError(errors.New("cannot connect to PostgreSQL; check connection settings and database availability"))
	}
	if readOnly {
		var exists bool
		if err := raw.QueryRowContext(ctx, "SELECT to_regclass(format('%I.metadata', current_schema())) IS NOT NULL").Scan(&exists); err != nil {
			return closeOnError(err)
		}
		if !exists {
			return closeOnError(ErrUninitialized)
		}
		if err := store.ensureExistingCanary(ctx); err != nil {
			return closeOnError(err)
		}
		return store, nil
	}
	if err := store.migratePostgres(ctx); err != nil {
		return closeOnError(err)
	}
	return store, nil
}

func (s *Store) migratePostgres(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var exists bool
	if err := tx.QueryRowContext(ctx, "SELECT to_regclass(format('%I.metadata', current_schema())) IS NOT NULL").Scan(&exists); err != nil {
		return err
	}
	if exists {
		var sealed, version []byte
		if err := tx.QueryRowContext(ctx, "SELECT value FROM metadata WHERE key='key_canary'").Scan(&sealed); err != nil {
			return err
		}
		plain, err := s.open("key-canary", sealed)
		if err != nil || string(plain) != "mcphub-config-key-v1" {
			return errors.New("configuration database encryption key is incorrect")
		}
		if err := tx.QueryRowContext(ctx, "SELECT value FROM metadata WHERE key='schema_version'").Scan(&version); err != nil {
			return err
		}
		if string(version) != "2" {
			return fmt.Errorf("unsupported PostgreSQL configuration schema version %q", version)
		}
	}
	if _, err := tx.ExecContext(ctx, postgresSchema); err != nil {
		return fmt.Errorf("initialize PostgreSQL configuration schema: %w", err)
	}
	if !exists {
		sealed, err := s.seal("key-canary", []byte("mcphub-config-key-v1"))
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "INSERT INTO metadata(key, value) VALUES('key_canary', ?), ('schema_version', ?)", sealed, []byte("2")); err != nil {
			return err
		}
	}
	return tx.Commit()
}
