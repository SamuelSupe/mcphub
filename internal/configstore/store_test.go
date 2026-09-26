package configstore

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SamuelSupe/mcphub/v2/internal/config"
)

func TestApprovalLifecycle(t *testing.T) {
	s, key, path := newTestStore(t)
	legacy, err := s.CreateApproval(t.Context(), ApprovalIntent{Subject: "legacy", Params: []byte(`{"arguments":{}}`)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(t.Context(), "DROP TABLE approval_events"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(t.Context(), "UPDATE metadata SET value=? WHERE key='schema_version'", []byte("3")); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(t.Context(), path, key)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if record, err := s.GetApproval(t.Context(), legacy.ID); err != nil || record.Intent.Subject != "legacy" {
		t.Fatal("schema 3 upgrade lost an approval")
	}
	testApprovalLifecycle(t, s)
	testApprovalGovernance(t, s)
}

func testApprovalLifecycle(t *testing.T, s *Store) {
	t.Helper()
	ctx := t.Context()
	intent := ApprovalIntent{Issuer: "https://issuer.example", Subject: "requester", Tool: "db.update", Params: []byte(`{"arguments":{"project":"confidential-project","n":9007199254740993}}`)}
	create := func() Approval {
		t.Helper()
		a, err := s.CreateApproval(ctx, intent)
		if err != nil {
			t.Fatal(err)
		}
		return a
	}
	a := create()
	var sealed []byte
	if err := s.db.QueryRowContext(ctx, "SELECT intent FROM approvals WHERE id=?", a.ID).Scan(&sealed); err != nil || bytes.Contains(sealed, []byte("confidential-project")) {
		t.Fatalf("approval encryption: %v", err)
	}
	if err := s.ClaimApproval(ctx, a.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("unapproved execution: %v", err)
	}
	if err := s.DecideApproval(ctx, a.ID, "approved", "reviewer", ApprovalDetail{Reason: "Reviewed exact operation"}); err != nil {
		t.Fatal(err)
	}
	var claims atomic.Int32
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if err := s.ClaimApproval(ctx, a.ID); err == nil {
				claims.Add(1)
			} else if !errors.Is(err, ErrConflict) {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if claims.Load() != 1 {
		t.Fatalf("execution admissions = %d", claims.Load())
	}
	result := []byte(`{"content":[{"type":"text","text":"confidential-result"}]}`)
	if err := s.FinishApproval(ctx, a.ID, "succeeded", result); err != nil {
		t.Fatal(err)
	}
	loaded, err := s.GetApproval(ctx, a.ID)
	if err != nil || loaded.Status != "succeeded" || loaded.Reviewer != "reviewer" || !bytes.Equal(loaded.Result, result) || !bytes.Equal(loaded.Intent.Params, intent.Params) {
		t.Fatalf("saved approval: %+v %v", loaded, err)
	}
	if err := s.db.QueryRowContext(ctx, "SELECT result FROM approvals WHERE id=?", a.ID).Scan(&sealed); err != nil || bytes.Contains(sealed, []byte("confidential-result")) {
		t.Fatalf("result encryption: %v", err)
	}
	if err := s.ClaimApproval(ctx, a.ID); !errors.Is(err, ErrConflict) {
		t.Fatal("completed approval was reusable")
	}
	rejected := create()
	if err := s.DecideApproval(ctx, rejected.ID, "rejected", "reviewer", ApprovalDetail{Reason: "Resource change rejected"}); err != nil {
		t.Fatal(err)
	}
	if err := s.ClaimApproval(ctx, rejected.ID); !errors.Is(err, ErrConflict) {
		t.Fatal("rejected approval was executable")
	}
	expired := create()
	if err := s.DecideApproval(ctx, expired.ID, "approved", "reviewer", ApprovalDetail{Reason: "Reviewed exact operation"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, "UPDATE approvals SET expires_at=? WHERE id=?", time.Now().Add(-time.Minute).UnixMilli(), expired.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.ClaimApproval(ctx, expired.ID); !errors.Is(err, ErrConflict) {
		t.Fatal("expired approval was executable")
	}
	interrupted := create()
	if err := s.DecideApproval(ctx, interrupted.ID, "approved", "reviewer", ApprovalDetail{Reason: "Reviewed exact operation"}); err != nil {
		t.Fatal(err)
	}
	if err := s.ClaimApproval(ctx, interrupted.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.RecoverApprovals(ctx); err != nil {
		t.Fatal(err)
	}
	loaded, err = s.GetApproval(ctx, interrupted.ID)
	if err != nil || loaded.Status != "unknown" {
		t.Fatalf("interrupted write: %+v %v", loaded, err)
	}
	if err := s.ClaimApproval(ctx, interrupted.ID); !errors.Is(err, ErrConflict) {
		t.Fatal("interrupted write was replayable")
	}
	for _, stop := range []string{"cancelled", "revoked"} {
		a := create()
		if err := s.DecideApproval(ctx, a.ID, "approved", "reviewer", ApprovalDetail{Reason: "Reviewed"}); err != nil {
			t.Fatal(err)
		}
		var winners atomic.Int32
		start := make(chan struct{})
		var race sync.WaitGroup
		race.Go(func() {
			<-start
			if err := s.ClaimApproval(ctx, a.ID); err == nil {
				winners.Add(1)
			} else if !errors.Is(err, ErrConflict) {
				t.Error(err)
			}
		})
		race.Go(func() {
			<-start
			var err error
			if stop == "cancelled" {
				err = s.CancelApproval(ctx, a.ID, intent.Issuer, intent.Subject, "Request withdrawn")
			} else {
				err = s.DecideApproval(ctx, a.ID, "revoked", "reviewer", ApprovalDetail{Reason: "Approval withdrawn"})
			}
			if err == nil {
				winners.Add(1)
			} else if !errors.Is(err, ErrConflict) {
				t.Error(err)
			}
		})
		close(start)
		race.Wait()
		if winners.Load() != 1 {
			t.Fatalf("%s and execution both succeeded", stop)
		}
	}
	if err := s.InvestigateApproval(ctx, interrupted.ID, "reviewer", ApprovalDetail{Reason: "Checked private upstream receipt", Outcome: "applied"}); err != nil {
		t.Fatal(err)
	}
	history, err := s.ApprovalHistory(ctx, interrupted.ID)
	if err != nil || len(history) != 5 || history[len(history)-1].Detail.Outcome != "applied" {
		t.Fatalf("investigation audit: %+v %v", history, err)
	}
	if err := s.ClaimApproval(ctx, interrupted.ID); !errors.Is(err, ErrConflict) {
		t.Fatal("investigation made an unknown write replayable")
	}
	if err := s.db.QueryRowContext(ctx, "SELECT detail FROM approval_events WHERE approval_id=? AND action='investigated'", interrupted.ID).Scan(&sealed); err != nil || bytes.Contains(sealed, []byte("private upstream")) {
		t.Fatal("investigation detail was not encrypted")
	}
	for i := range 4 {
		intent.Target = string(rune('a' + i))
		create()
	}
	seen := map[string]bool{}
	cursor := ""
	for {
		page, err := s.ListApprovals(ctx, ApprovalQuery{Status: "pending", Subject: intent.Subject, Cursor: cursor, Limit: 2})
		if err != nil {
			t.Fatal(err)
		}
		for _, value := range page {
			if seen[value.ID] {
				t.Fatal("pagination repeated a request")
			}
			seen[value.ID] = true
			cursor = ApprovalCursor(value)
		}
		if len(page) < 2 {
			break
		}
	}
	if len(seen) != 4 {
		t.Fatalf("pagination omitted pending requests: %d", len(seen))
	}
	if _, err := s.db.ExecContext(ctx, "UPDATE approvals SET updated_at=? WHERE id=?", time.Now().Add(-48*time.Hour).UnixMilli(), interrupted.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.MaintainApprovals(ctx, 24*time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetApproval(ctx, interrupted.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("expired retention kept sensitive approval data")
	}
	history, err = s.ApprovalHistory(ctx, interrupted.ID)
	if err != nil || len(history) != 0 {
		t.Fatal("retention left detailed audit data behind")
	}
}

func TestBootstrapIsIdempotentAndKeepsTheOriginalSnapshot(t *testing.T) {
	ctx := context.Background()
	store, _, _ := newTestStore(t)
	first := config.BackendConfig{
		ID:             "alpha",
		URL:            "https://alpha.example.com/mcp",
		RequestTimeout: config.Duration{Duration: time.Second},
		PublishedTools: []string{"search"},
		ToolRules:      []config.ToolRule{{Match: "search", ResourceRules: []config.ResourceRule{{Argument: "/project", AllowedValues: []string{"work"}}}}},
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
	if !reflect.DeepEqual(records[0].Config.PublishedTools, first.PublishedTools) || !reflect.DeepEqual(records[0].Config.ToolRules, first.ToolRules) {
		t.Fatal("tool publication policy did not round trip")
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

// Both database engines must serialize operation registration and reviewer votes,
// and commit archive delivery with the decision, not in an eventual callback.
func testApprovalGovernance(t *testing.T, s *Store) {
	t.Helper()
	ctx := t.Context()
	public, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	s.ConfigureApprovalDelivery(config.ApprovalSettings{Notifications: config.ApprovalWebhook{URL: "https://notify.example", Secret: "test-notification-secret-32-bytes-only"}, AuditArchive: config.ApprovalArchive{URL: "https://archive.example", KeyID: "test", SigningKey: base64.StdEncoding.EncodeToString(key)}}, "https://admin.example")
	defer s.ConfigureApprovalDelivery(config.ApprovalSettings{}, "")
	intent := ApprovalIntent{Issuer: "https://issuer.example", Subject: "operation-owner", Tool: "db.write", BackendID: "db", OperationID: "change-123", Params: []byte(`{"arguments":{"operation_id":"change-123","n":9007199254740993},"_meta":{"progressToken":1}}`), Rules: []config.ToolApprovalPolicy{{RequiredApprovals: 2}}, PendingTTL: 2 * time.Minute}
	var wg sync.WaitGroup
	ids := make(chan string, 8)
	for range 8 {
		wg.Go(func() {
			a, err := s.CreateApproval(ctx, intent)
			if err != nil {
				t.Error(err)
				return
			}
			ids <- a.ID
		})
	}
	wg.Wait()
	close(ids)
	id := ""
	for value := range ids {
		if id != "" && id != value {
			t.Fatal("concurrent duplicate operations created different approvals")
		}
		id = value
	}
	if id == "" {
		t.Fatal("operation was not created")
	}
	progress := intent
	progress.Params = []byte(`{"_meta":{"progressToken":999},"arguments":{"n":9007199254740993,"operation_id":"change-123"}}`)
	if a, err := s.CreateApproval(ctx, progress); err != nil || !a.Reused || a.ID != id {
		t.Fatalf("transport metadata broke operation identity: %v", err)
	}
	changed := intent
	changed.Params = []byte(`{"arguments":{"operation_id":"change-123","n":9007199254740992}}`)
	if _, err := s.CreateApproval(ctx, changed); !errors.Is(err, ErrOperationConflict) {
		t.Fatalf("operation accepted changed arguments: %v", err)
	}
	if err := s.DecideApproval(ctx, id, "approved", intent.Subject, ApprovalDetail{Reason: "self"}); !errors.Is(err, ErrConflict) {
		t.Fatal("two-person approval allowed its requester to vote")
	}
	var votes atomic.Int32
	for range 8 {
		wg.Go(func() {
			if err := s.DecideApproval(ctx, id, "approved", "reviewer-a", ApprovalDetail{Reason: "Reviewed"}); err == nil {
				votes.Add(1)
			} else if !errors.Is(err, ErrConflict) {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if votes.Load() != 1 {
		t.Fatalf("duplicate reviewer counted %d times", votes.Load())
	}
	if err := s.ClaimApproval(ctx, id); !errors.Is(err, ErrConflict) {
		t.Fatal("first vote admitted execution")
	}
	if err := s.MaintainApprovals(ctx, 24*time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := s.MaintainApprovals(ctx, 24*time.Hour); err != nil {
		t.Fatal(err)
	}
	var reminders int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM approval_events WHERE approval_id=? AND action='reminded'", id).Scan(&reminders); err != nil || reminders != 1 {
		t.Fatalf("reminders=%d err=%v", reminders, err)
	}
	if err := s.DecideApproval(ctx, id, "approved", "reviewer-b", ApprovalDetail{Reason: "Independently reviewed"}); err != nil {
		t.Fatal(err)
	}
	a, err := s.GetApproval(ctx, id)
	if err != nil || a.Status != "approved" || len(a.ApprovedBy) != 2 {
		t.Fatalf("quorum not persisted: %+v %v", a, err)
	}
	if err := s.ClaimApproval(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishApproval(ctx, id, "unknown", []byte(`{"message":"inspect upstream"}`)); err != nil {
		t.Fatal(err)
	}
	if duplicate, err := s.CreateApproval(ctx, intent); err != nil || duplicate.ID != id || duplicate.Status != "unknown" {
		t.Fatal("unknown operation was recreated")
	}
	if _, err := s.db.ExecContext(ctx, "UPDATE approvals SET updated_at=? WHERE id=?", time.Now().Add(-48*time.Hour).UnixMilli(), id); err != nil {
		t.Fatal(err)
	}
	if err := s.MaintainApprovals(ctx, 24*time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetApproval(ctx, id); err != nil {
		t.Fatal("retention deleted unarchived evidence")
	}
	delivery, err := s.NextApprovalDelivery(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteApprovalDelivery(ctx, delivery, true, false); err != nil {
		t.Fatal(err)
	}
	health, err := s.ApprovalDeliveryStatus(ctx)
	if err != nil || health.ArchiveFailed != 1 {
		t.Fatalf("archive failure not observable: %+v %v", health, err)
	}
	if _, err := s.NextApprovalDelivery(ctx, true); !errors.Is(err, ErrNotFound) {
		t.Fatal("retry backoff was not respected")
	}
	if _, err := s.db.ExecContext(ctx, "UPDATE approval_deliveries SET archive_due=1 WHERE archive_due>0"); err != nil {
		t.Fatal(err)
	}
	var sequence int64
	hash := ""
	for {
		value, err := s.NextApprovalDelivery(ctx, true)
		if errors.Is(err, ErrNotFound) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		var envelope AuditEnvelope
		if err := json.Unmarshal(value.Payload, &envelope); err != nil {
			t.Fatal(err)
		}
		entry, err := VerifyAuditEnvelope(envelope, map[string]ed25519.PublicKey{"test": public}, sequence, hash)
		if err != nil {
			t.Fatal(err)
		}
		tampered := envelope
		tampered.Entry = append([]byte(nil), envelope.Entry...)
		tampered.Entry[0] = ' '
		if _, err := VerifyAuditEnvelope(tampered, map[string]ed25519.PublicKey{"test": public}, sequence, hash); err == nil {
			t.Fatal("tampered audit entry verified")
		}
		sequence, hash = entry.Sequence, envelope.Hash
		if err := s.CompleteApprovalDelivery(ctx, value, true, true); err != nil {
			t.Fatal(err)
		}
	}
	if sequence < 6 {
		t.Fatalf("archive missed lifecycle events: %d", sequence)
	}
	if err := s.MaintainApprovals(ctx, 24*time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetApproval(ctx, id); !errors.Is(err, ErrNotFound) {
		t.Fatal("archived result was not purged")
	}
	if _, err := s.CreateApproval(ctx, intent); !errors.Is(err, ErrOperationConflict) {
		t.Fatal("retention made operation ID reusable")
	}
}
