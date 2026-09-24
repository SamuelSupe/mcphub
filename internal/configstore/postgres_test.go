package configstore

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/SamuelSupe/mcphub/internal/config"
	"github.com/SamuelSupe/mcphub/internal/httptool"
	"github.com/SamuelSupe/mcphub/internal/ratelimit"
)

// A real PostgreSQL run protects SQL dialect, transaction and bytea/citext
// boundaries that SQLite and mocks cannot exercise. Each run owns one schema.
func TestPostgresConfigurationLifecycle(t *testing.T) {
	dsn := os.Getenv("MCPHUB_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("MCPHUB_TEST_POSTGRES_DSN is not set")
	}
	ctx := WithActor(t.Context(), "admin-user-123")
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal("invalid test PostgreSQL DSN")
	}
	db := stdlib.OpenDB(*cfg)
	t.Cleanup(func() { db.Close() })
	if _, err := db.ExecContext(ctx, "CREATE EXTENSION IF NOT EXISTS citext WITH SCHEMA public"); err != nil {
		t.Fatal(err)
	}
	schema := "mcphub_test_" + strings.ToLower(rand.Text())
	if _, err := db.ExecContext(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := db.ExecContext(context.Background(), "DROP SCHEMA "+schema+" CASCADE"); err != nil {
			t.Error(err)
		}
	})
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		u, err := url.Parse(dsn)
		if err != nil {
			t.Fatal(err)
		}
		query := u.Query()
		query.Set("search_path", schema+",public")
		u.RawQuery = query.Encode()
		dsn = u.String()
	} else {
		dsn += " search_path=" + schema + ",public"
	}
	key := make([]byte, 32)
	rand.Read(key)
	if _, err := OpenPostgres(ctx, dsn, key, true); !errors.Is(err, ErrUninitialized) {
		t.Fatalf("read-only uninitialized: %v", err)
	}
	s, err := OpenPostgres(ctx, dsn, key, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	backend := config.BackendConfig{RateLimit: ratelimit.Config{RequestsPerSecond: 2.5, Burst: 4, MaxConcurrent: 3}, ID: "Alpha", URL: "https://alpha.example/mcp", Headers: map[string]string{"X-Key": "private-value"}}
	if err := s.Bootstrap(ctx, []config.BackendConfig{backend}); err != nil {
		t.Fatal(err)
	}
	if err := s.Bootstrap(ctx, nil); err != nil {
		t.Fatal(err)
	}
	loaded, err := s.Get(ctx, "ALPHA")
	if err != nil || loaded.Config.Headers["X-Key"] != "private-value" || loaded.Config.RateLimit != backend.RateLimit {
		t.Fatalf("case-insensitive encrypted read: %v", err)
	}
	var public, sealed []byte
	if err := s.db.QueryRowContext(ctx, "SELECT config_json,secrets FROM backends WHERE id=?", "alpha").Scan(&public, &sealed); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(public, []byte("private-value")) || bytes.Contains(sealed, []byte("private-value")) {
		t.Fatal("plaintext secret stored")
	}
	var successes atomic.Int32
	var wg sync.WaitGroup
	for range 6 {
		wg.Go(func() {
			if _, err := s.Update(ctx, loaded, loaded.Revision); err == nil {
				successes.Add(1)
			} else if !errors.Is(err, ErrConflict) {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if successes.Load() != 1 {
		t.Fatalf("concurrent successful updates: %d", successes.Load())
	}
	if err := s.Delete(ctx, "alpha", 2); err != nil {
		t.Fatal(err)
	}
	recreated, err := s.Create(ctx, loaded)
	if err != nil || recreated.Revision <= 2 {
		t.Fatalf("recreated revision: %d, %v", recreated.Revision, err)
	}
	if _, err := s.CreateToolGroup(ctx, ToolGroupRecord{Config: toolGroupFixture("ALPHA")}); !errors.Is(err, ErrConflict) {
		t.Fatalf("namespace conflict: %v", err)
	}
	groupConfig := toolGroupFixture("Catalog")
	groupConfig.RateLimit = backend.RateLimit
	group, err := s.CreateToolGroup(ctx, ToolGroupRecord{Config: groupConfig})
	if err != nil {
		t.Fatal(err)
	}
	reloadedGroup, err := s.GetToolGroup(ctx, "catalog")
	if err != nil || reloadedGroup.Config.RateLimit != groupConfig.RateLimit {
		t.Fatalf("stored rate limit: %+v, %v", reloadedGroup, err)
	}
	manual := HTTPToolRecord{GroupID: "Catalog", Config: httptool.ToolConfig{Name: "existing", Enabled: true, Method: "GET", Path: "/existing"}}
	if _, err := s.CreateHTTPTool(ctx, manual); err != nil {
		t.Fatal(err)
	}
	imp := openAPIImportFixture("Catalog", "source", []byte("spec-v1"))
	tool := manual
	tool.Config.ImportID = "source"
	tool.Config.Origin = "openapi"
	if _, _, err := s.CreateOpenAPIImport(ctx, imp, []HTTPToolRecord{tool}); !errors.Is(err, ErrConflict) {
		t.Fatalf("import conflict: %v", err)
	}
	if _, err := s.GetOpenAPIImport(ctx, "Catalog", "source"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("failed import was not atomic: %v", err)
	}
	tool.Config.Name = "imported"
	created, _, err := s.CreateOpenAPIImport(ctx, imp, []HTTPToolRecord{tool})
	if err != nil {
		t.Fatal(err)
	}
	created.Document = []byte("spec-v2")
	updated, _, err := s.ReplaceOpenAPIImport(ctx, created, created.Revision, []HTTPToolRecord{tool}, "refresh")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.MarkOpenAPIRefreshFailure(ctx, "catalog", "source", updated.Revision, "unavailable"); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkOpenAPIRefreshSuccess(ctx, "catalog", "source", updated.Revision); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteToolGroup(ctx, "catalog", group.Revision); err != nil {
		t.Fatal(err)
	}
	tools, err := s.ListHTTPTools(ctx, "catalog")
	if err != nil || len(tools) != 0 {
		t.Fatalf("tool cascade: %v", err)
	}
	imports, err := s.ListOpenAPIImports(ctx, "catalog")
	if err != nil || len(imports) != 0 {
		t.Fatalf("import cascade: %v", err)
	}
	events, err := s.Events(ctx, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) < 10 {
		t.Fatalf("missing audit events: %d", len(events))
	}
	for _, event := range events {
		if event.Actor != "admin-user-123" {
			t.Fatalf("lost audit actor: %#v", event)
		}
	}
	wrong := bytes.Clone(key)
	wrong[0]++
	if bad, err := OpenPostgres(ctx, dsn, wrong, false); err == nil {
		bad.Close()
		t.Fatal("wrong key accepted")
	}
	ro, err := OpenPostgres(ctx, dsn, key, true)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	if rows, err := ro.List(ctx); err != nil || len(rows) != 1 {
		t.Fatalf("read-only list: %v", err)
	}
	if _, err := ro.Create(ctx, Record{Config: config.BackendConfig{ID: "blocked"}}); err == nil {
		t.Fatal("read-only connection accepted a write")
	}
}
