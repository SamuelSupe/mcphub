package configstore

import (
	"context"
	"database/sql"
	"strconv"
	"strings"
)

type database struct {
	*sql.DB
	postgres bool
}

// Only repository-owned SQL passes through this adapter. PostgreSQL uses citext
// for identifiers, preserving SQLite's case-insensitive keys and foreign keys.
func bindSQL(query string, postgres bool) string {
	if !postgres {
		return query
	}
	query = strings.ReplaceAll(query, " COLLATE NOCASE", "")
	var out strings.Builder
	n := 0
	for _, ch := range query {
		if ch == '?' {
			n++
			out.WriteString("$" + strconv.Itoa(n))
		} else {
			out.WriteRune(ch)
		}
	}
	return out.String()
}

func (db *database) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return db.DB.ExecContext(ctx, bindSQL(query, db.postgres), args...)
}

func (db *database) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return db.DB.QueryContext(ctx, bindSQL(query, db.postgres), args...)
}

func (db *database) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return db.DB.QueryRowContext(ctx, bindSQL(query, db.postgres), args...)
}

func (db *database) BeginTx(ctx context.Context, opts *sql.TxOptions) (*transaction, error) {
	tx, err := db.DB.BeginTx(ctx, opts)
	if err != nil {
		return nil, err
	}
	if db.postgres {
		// Serialize configuration commits, including cross-table namespace checks
		// and revisions derived from deleted records' audit history.
		if _, err := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(724391028)"); err != nil {
			_ = tx.Rollback()
			return nil, err
		}
	}
	return &transaction{Tx: tx, postgres: db.postgres}, nil
}

type transaction struct {
	*sql.Tx
	postgres bool
}

func (tx *transaction) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return tx.Tx.ExecContext(ctx, bindSQL(query, tx.postgres), args...)
}

func (tx *transaction) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return tx.Tx.QueryContext(ctx, bindSQL(query, tx.postgres), args...)
}

func (tx *transaction) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return tx.Tx.QueryRowContext(ctx, bindSQL(query, tx.postgres), args...)
}

type actorKey struct{}

func WithActor(ctx context.Context, actor string) context.Context {
	return context.WithValue(ctx, actorKey{}, actor)
}

func Actor(ctx context.Context) string {
	if actor, _ := ctx.Value(actorKey{}).(string); actor != "" {
		return actor
	}
	return "local"
}
