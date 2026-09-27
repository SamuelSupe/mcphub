package configstore

import (
	"testing"
	"time"

	"github.com/SamuelSupe/mcphub/v2/internal/diagnostics"
)

func TestRequestHistorySurvivesReopen(t *testing.T) {
	s, key, path := newTestStore(t)
	if _, err := s.db.ExecContext(t.Context(), "DROP TABLE request_records"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(t.Context(), "UPDATE metadata SET value=? WHERE key='schema_version'", []byte("8")); err != nil {
		t.Fatal(err)
	}
	s.Close()
	var err error
	s, err = Open(t.Context(), path, key)
	if err != nil {
		t.Fatal("upgrade from schema 8", err)
	}
	testRequestHistory(t, s)
	s.Close()
	reopened, err := Open(t.Context(), path, key)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	page, err := reopened.RequestHistory(t.Context(), diagnostics.Query{Subject: "history-user", Since: time.Now().Add(-48 * time.Hour), Until: time.Now(), Limit: 25})
	if err != nil || page.Statistics.Total != 2 {
		t.Fatalf("history lost on reopen: %+v %v", page, err)
	}
}

func testRequestHistory(t *testing.T, s *Store) {
	t.Helper()
	ctx := t.Context()
	now := time.Now().UTC()
	for i, age := range []time.Duration{time.Hour, 25 * time.Hour, 50 * time.Hour} {
		record := diagnostics.Record{RequestID: "history-request", Subject: "history-user", ClientID: "editor", Endpoint: "notes", Tool: "read", Method: "tools/call", Outcome: "success", DurationMS: int64(i+1) * 10, CompletedAt: now.Add(-age), StartedAt: now.Add(-age - time.Second)}
		if err := s.RecordRequest(ctx, record); err != nil {
			t.Fatal(err)
		}
	}
	q := diagnostics.Query{Subject: "history-user", Since: now.Add(-48 * time.Hour), Until: now, Limit: 1}
	page, err := s.RequestHistory(ctx, q)
	if err != nil || !page.Persisted || page.Statistics.Total != 2 || page.Statistics.AverageMS != 15 || page.Statistics.P95MS != 20 || len(page.Records) != 1 || page.NextCursor == 0 {
		t.Fatalf("history window/statistics: %+v %v", page, err)
	}
	q.Before = page.NextCursor
	next, err := s.RequestHistory(ctx, q)
	if err != nil || len(next.Records) != 1 || next.NextCursor != 0 || next.Records[0].Sequence == page.Records[0].Sequence || next.Statistics.Total != 2 {
		t.Fatalf("history pagination: %+v %v", next, err)
	}
	q.Subject = "another-user"
	if other, err := s.RequestHistory(ctx, q); err != nil || other.Statistics.Total != 0 {
		t.Fatal("history filter leaked records", err)
	}
	if err := s.PruneRequestHistory(ctx, now.Add(-48*time.Hour)); err != nil {
		t.Fatal(err)
	}
	q.Subject, q.Before, q.Since = "history-user", 0, now.Add(-72*time.Hour)
	if retained, err := s.RequestHistory(ctx, q); err != nil || retained.Statistics.Total != 2 {
		t.Fatal("retention removed current records or kept expired records", err)
	}
}
