package configstore

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/SamuelSupe/mcphub/v2/internal/diagnostics"
)

func (s *Store) RecordRequest(ctx context.Context, record diagnostics.Record) error {
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	data, err = s.seal("request:"+record.RequestID, data)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO request_records(request_id,completed_at,subject,client_id,endpoint,tool,outcome,duration_ms,approval_wait_ms,data) VALUES(?,?,?,?,?,?,?,?,?,?)`, record.RequestID, record.CompletedAt.UnixMilli(), record.Subject, record.ClientID, record.Endpoint, record.Tool, record.Outcome, record.DurationMS, record.ApprovalWaitMS, data)
	return err
}

func requestFilter(q diagnostics.Query) (string, []any) {
	clauses := []string{"completed_at>=?", "completed_at<=?"}
	args := []any{q.Since.UnixMilli(), q.Until.UnixMilli()}
	for _, filter := range []struct{ column, value string }{
		{"request_id", q.RequestID}, {"subject", q.Subject}, {"client_id", q.ClientID},
		{"endpoint", q.Endpoint}, {"tool", q.Tool}, {"outcome", q.Outcome},
	} {
		if filter.value != "" {
			clauses = append(clauses, filter.column+"=?")
			args = append(args, filter.value)
		}
	}
	return " WHERE " + strings.Join(clauses, " AND "), args
}

func (s *Store) RequestRecords(ctx context.Context, q diagnostics.Query) ([]diagnostics.Record, uint64, error) {
	if q.Limit < 1 || q.Limit > 10000 {
		q.Limit = 25
	}
	where, args := requestFilter(q)
	if q.Before > 0 {
		where += " AND id<?"
		args = append(args, q.Before)
	}
	args = append(args, q.Limit+1)
	rows, err := s.db.QueryContext(ctx, "SELECT id,request_id,data FROM request_records"+where+" ORDER BY id DESC LIMIT ?", args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	records := []diagnostics.Record{}
	var next uint64
	for rows.Next() {
		var sequence uint64
		var id string
		var data []byte
		if err = rows.Scan(&sequence, &id, &data); err != nil {
			return nil, 0, err
		}
		if len(records) == q.Limit {
			next = records[len(records)-1].Sequence
			break
		}
		data, err = s.open("request:"+id, data)
		if err != nil {
			return nil, 0, err
		}
		var record diagnostics.Record
		if err = json.Unmarshal(data, &record); err != nil {
			return nil, 0, err
		}
		record.Sequence = sequence
		records = append(records, record)
	}
	return records, next, rows.Err()
}

func (s *Store) RequestHistory(ctx context.Context, q diagnostics.Query) (diagnostics.Page, error) {
	page := diagnostics.Page{Persisted: true, WindowStart: q.Since, WindowEnd: q.Until, Statistics: diagnostics.Statistics{Outcomes: map[string]int{}}}
	var err error
	page.Records, page.NextCursor, err = s.RequestRecords(ctx, q)
	if err != nil {
		return page, err
	}
	where, args := requestFilter(q)
	rows, err := s.db.QueryContext(ctx, `SELECT outcome,COUNT(*),SUM(duration_ms),SUM(approval_wait_ms),SUM(CASE WHEN approval_wait_ms>0 THEN 1 ELSE 0 END) FROM request_records`+where+" GROUP BY outcome", args...)
	if err != nil {
		return page, err
	}
	stats := &page.Statistics
	for rows.Next() {
		var outcome string
		var count, resumes int
		var duration, wait int64
		if err = rows.Scan(&outcome, &count, &duration, &wait, &resumes); err != nil {
			break
		}
		stats.Outcomes[outcome] = count
		stats.Total += count
		stats.AverageMS += duration
		stats.ApprovalWaitMS += wait
		stats.ApprovalResumes += resumes
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return page, err
	}
	if stats.Total > 0 {
		stats.AverageMS /= int64(stats.Total)
		args = append(args, (stats.Total*95+99)/100-1)
		if err = s.db.QueryRowContext(ctx, "SELECT duration_ms FROM request_records"+where+" ORDER BY duration_ms LIMIT 1 OFFSET ?", args...).Scan(&stats.P95MS); err != nil {
			return page, err
		}
	}
	if stats.ApprovalResumes > 0 {
		stats.ApprovalWaitMS /= int64(stats.ApprovalResumes)
	}
	return page, nil
}

func (s *Store) PruneRequestHistory(ctx context.Context, before time.Time) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM request_records WHERE completed_at<?", before.UnixMilli())
	return err
}
