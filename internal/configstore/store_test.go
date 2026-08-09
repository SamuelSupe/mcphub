package configstore

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/SamuelSupe/mcphub/internal/config"
)

func TestBootstrapIsIdempotentAndKeepsTheOriginalSnapshot(t *testing.T) {
	ctx := context.Background()
	store, _, _ := newTestStore(t)
	first := config.BackendConfig{
		ID:             "alpha",
		URL:            "https://alpha.example.com/mcp",
		RequestTimeout: config.Duration{Duration: time.Second},
	}
	if err := store.Bootstrap(ctx, []config.BackendConfig{first}); err != nil {
		t.Fatalf("first Bootstrap() error: %v", err)
	}
	if initialized, err := store.Initialized(ctx); err != nil || !initialized {
		t.Fatalf("Initialized() = %v, %v; want true, nil", initialized, err)
	}

	second := first
	second.URL = "https://changed.example.com/mcp"
	if err := store.Bootstrap(ctx, []config.BackendConfig{second}); err != nil {
		t.Fatalf("second Bootstrap() error: %v", err)
	}
	records, err := store.List(ctx)
	if err != nil {
		t.Fatalf("List() error: %v", err)
	}
	if len(records) != 1 || records[0].Config.URL != first.URL || records[0].Revision != 1 {
		t.Fatalf("records after repeated bootstrap = %#v, want original revision-1 snapshot", records)
	}
	events, err := store.Events(ctx, 10)
	if err != nil {
		t.Fatalf("Events() error: %v", err)
	}
	if len(events) != 1 || events[0].Action != "bootstrap" || events[0].Actor != "local" {
		t.Fatalf("events after repeated bootstrap = %#v, want one bootstrap event", events)
	}
}

func TestOpenSecuresSQLiteDatabaseAndSidecars(t *testing.T) {
	ctx := context.Background()
	store, _, path := newTestStore(t)
	if _, err := store.Create(ctx, Record{Config: config.BackendConfig{ID: "alpha", URL: "https://alpha.example.com/mcp"}}); err != nil {
		t.Fatalf("Create() error: %v", err)
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		filename := path + suffix
		info, err := os.Stat(filename)
		if err != nil {
			t.Fatalf("stat %s: %v", filename, err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Errorf("%s permissions = %o, want 600", suffix, got)
		}
	}
}

func TestStoreEncryptsSecretsAndRejectsWrongKey(t *testing.T) {
	ctx := context.Background()
	store, key, dbPath := newTestStore(t)
	record := Record{
		Config: config.BackendConfig{
			ID:  "alpha",
			URL: "https://alpha.example.com/mcp",
			Headers: map[string]string{
				"X-API-Key": "header-secret-value",
			},
			OAuth: &config.OAuthConfig{
				Type:         "client_credentials",
				Issuer:       "https://idp.example.com",
				ClientID:     "client",
				ClientSecret: "oauth-secret-value",
			},
		},
		Enabled: true,
	}
	if _, err := store.Create(ctx, record); err != nil {
		t.Fatalf("Create() error: %v", err)
	}
	var public, sealed []byte
	if err := store.db.QueryRowContext(ctx, "SELECT config_json, secrets FROM backends WHERE id = 'alpha'").Scan(&public, &sealed); err != nil {
		t.Fatalf("read raw backend row: %v", err)
	}
	for name, raw := range map[string][]byte{"public config": public, "sealed secrets": sealed} {
		if bytes.Contains(raw, []byte("header-secret-value")) || bytes.Contains(raw, []byte("oauth-secret-value")) {
			t.Fatalf("%s contains plaintext secret: %q", name, raw)
		}
	}
	loaded, err := store.Get(ctx, "alpha")
	if err != nil {
		t.Fatalf("Get() error: %v", err)
	}
	if loaded.Config.Headers["X-API-Key"] != "header-secret-value" || loaded.Config.OAuth == nil || loaded.Config.OAuth.ClientSecret != "oauth-secret-value" {
		t.Fatalf("decrypted secrets = %#v, want original values", loaded.Config)
	}

	if err := store.Close(); err != nil {
		t.Fatalf("Close() error: %v", err)
	}
	wrongKey := make([]byte, 32)
	copy(wrongKey, key)
	wrongKey[0]++
	if wrong, err := Open(ctx, dbPath, wrongKey); err == nil {
		_ = wrong.Close()
		t.Fatal("Open() with a wrong key succeeded")
	} else if !bytes.Contains([]byte(err.Error()), []byte("incorrect")) {
		t.Fatalf("Open() wrong-key error = %v, want incorrect-key diagnostic", err)
	}
}

func TestUpdateRejectsStaleRevisionWithoutChangingStoredRecord(t *testing.T) {
	ctx := context.Background()
	store, _, _ := newTestStore(t)
	created, err := store.Create(ctx, Record{Config: config.BackendConfig{
		ID:  "alpha",
		URL: "https://alpha.example.com/mcp",
	}})
	if err != nil {
		t.Fatalf("Create() error: %v", err)
	}
	created.Config.URL = "https://updated.example.com/mcp"
	updated, err := store.Update(ctx, created, created.Revision)
	if err != nil {
		t.Fatalf("Update() error: %v", err)
	}
	if updated.Revision != 2 {
		t.Fatalf("updated revision = %d, want 2", updated.Revision)
	}

	stale := updated
	stale.Config.URL = "https://stale.example.com/mcp"
	if _, err := store.Update(ctx, stale, created.Revision); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale Update() error = %v, want ErrConflict", err)
	}
	events, err := store.Events(ctx, 10)
	if err != nil {
		t.Fatalf("Events() after stale update: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("events after stale update = %#v, want create plus one successful update", events)
	}
	for _, event := range events {
		if event.Actor != "local" {
			t.Fatalf("event actor = %q, want local: %#v", event.Actor, event)
		}
	}
	current, err := store.Get(ctx, "alpha")
	if err != nil {
		t.Fatalf("Get() after stale update: %v", err)
	}
	if current.Revision != 2 || current.Config.URL != "https://updated.example.com/mcp" {
		t.Fatalf("record after stale update = %#v, want revision 2 and updated URL", current)
	}
}

func TestBackendRevisionDoesNotReuseETagAfterDeleteAndRecreate(t *testing.T) {
	ctx := context.Background()
	store, _, _ := newTestStore(t)
	created, err := store.Create(ctx, Record{Config: config.BackendConfig{
		ID:  "alpha",
		URL: "https://alpha.example.com/mcp",
	}})
	if err != nil {
		t.Fatalf("Create() error: %v", err)
	}
	if err := store.Delete(ctx, created.Config.ID, created.Revision); err != nil {
		t.Fatalf("Delete() error: %v", err)
	}
	recreated, err := store.Create(ctx, Record{Config: config.BackendConfig{
		ID:  "alpha",
		URL: "https://recreated.example.com/mcp",
	}})
	if err != nil {
		t.Fatalf("recreate backend: %v", err)
	}
	if recreated.Revision <= created.Revision {
		t.Fatalf("recreated revision = %d, want greater than deleted revision %d", recreated.Revision, created.Revision)
	}
	stale := recreated
	stale.Config.URL = "https://stale.example.com/mcp"
	if _, err := store.Update(ctx, stale, created.Revision); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale update with old ETag error = %v, want ErrConflict", err)
	}
	if err := store.Delete(ctx, recreated.Config.ID, created.Revision); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale delete with old ETag error = %v, want ErrConflict", err)
	}
	current, err := store.Get(ctx, recreated.Config.ID)
	if err != nil {
		t.Fatalf("Get() after stale writes: %v", err)
	}
	if current.Revision != recreated.Revision || current.Config.URL != recreated.Config.URL {
		t.Fatalf("backend after stale writes = %#v, want recreated record unchanged", current)
	}
	events, err := store.Events(ctx, 20)
	if err != nil {
		t.Fatalf("Events() after backend ABA sequence: %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("backend ABA events = %#v, want create/delete/create", events)
	}
	for _, event := range events {
		if event.SourceKind != "backend" || event.SourceID != "alpha" {
			t.Fatalf("backend event source = %#v, want backend/alpha", event)
		}
	}
}

func newTestStore(t *testing.T) (*Store, []byte, string) {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatalf("generate test key: %v", err)
	}
	path := filepath.Join(t.TempDir(), "config.db")
	store, err := Open(context.Background(), path, key)
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, key, path
}
