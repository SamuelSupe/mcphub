package diagnostics

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestRecorderConcurrentWindowAndPagination(t *testing.T) {
	var recorder Recorder
	var workers sync.WaitGroup
	for i := range Capacity + 20 {
		workers.Go(func() {
			ctx := Begin(t.Context(), fmt.Sprintf("request-%d", i))
			Update(ctx, func(r *Record) { r.ClientID = "client-a"; r.StartedAt = time.Now().Add(-time.Second) })
			Outcome(ctx, "approval_pending", "approval_pending")
			if outcome := Result(ctx, "tool_error"); outcome != "approval_pending" {
				t.Errorf("MCP error envelope hid approval outcome: %q", outcome)
			}
			recorder.Finish(ctx, 200)
		})
	}
	workers.Wait()
	page := recorder.Query(Query{ClientID: "client-a", Limit: 37})
	if page.Statistics.Total != Capacity || page.Statistics.Outcomes["approval_pending"] != Capacity || page.Statistics.P95MS < 1000 {
		t.Fatalf("window statistics: %+v", page.Statistics)
	}
	seen := map[uint64]bool{}
	for {
		for _, r := range page.Records {
			if seen[r.Sequence] {
				t.Fatal("duplicate page entry")
			}
			seen[r.Sequence] = true
		}
		if page.NextCursor == 0 {
			break
		}
		page = recorder.Query(Query{ClientID: "client-a", Before: page.NextCursor, Limit: 37})
	}
	if len(seen) != Capacity {
		t.Fatalf("page count=%d", len(seen))
	}
	if recorder.Query(Query{ClientID: "other"}).Statistics.Total != 0 {
		t.Fatal("filter included another client")
	}
	recorder.mu.Lock()
	for i := range recorder.records {
		recorder.records[i].CompletedAt = time.Now().Add(-Retention - time.Second)
	}
	recorder.mu.Unlock()
	if recorder.Query(Query{}).Statistics.Total != 0 {
		t.Fatal("expired records retained in window")
	}
}
