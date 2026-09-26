package configstore

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

type ApprovalDetail struct {
	Reason   string    `json:"reason,omitempty"`
	Outcome  string    `json:"outcome,omitempty"`
	ACR      string    `json:"acr,omitempty"`
	AuthTime time.Time `json:"auth_time,omitempty"`
}

type ApprovalEvent struct {
	Action    string         `json:"action"`
	Actor     string         `json:"actor"`
	CreatedAt time.Time      `json:"created_at"`
	Detail    ApprovalDetail `json:"detail"`
}

func validApprovalReason(reason string) bool {
	return strings.TrimSpace(reason) != "" && len(reason) <= 2048
}

func (s *Store) ApprovalHistory(ctx context.Context, id string) ([]ApprovalEvent, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id, action, actor, detail, created_at FROM approval_events WHERE approval_id=? ORDER BY created_at, id", id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := make([]ApprovalEvent, 0)
	for rows.Next() {
		var event ApprovalEvent
		var eventID string
		var sealed []byte
		var created int64
		if err := rows.Scan(&eventID, &event.Action, &event.Actor, &sealed, &created); err != nil {
			return nil, err
		}
		plain, err := s.open("approval-event:"+eventID, sealed)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(plain, &event.Detail); err != nil {
			return nil, err
		}
		event.CreatedAt = time.UnixMilli(created).UTC()
		events = append(events, event)
	}
	return events, rows.Err()
}

// An investigation adds evidence without declaring execution safe to retry.
func (s *Store) InvestigateApproval(ctx context.Context, id, actor string, detail ApprovalDetail) error {
	if !validApprovalReason(detail.Reason) || (detail.Outcome != "applied" && detail.Outcome != "not_applied" && detail.Outcome != "uncertain") {
		return errors.New("invalid investigation")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var status string
	if err := tx.QueryRowContext(ctx, "SELECT status FROM approvals WHERE id=?", id).Scan(&status); err != nil {
		return err
	}
	if status != "unknown" {
		return ErrConflict
	}
	var count int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM approval_events WHERE approval_id=? AND action='investigated'", id).Scan(&count); err != nil {
		return err
	}
	if count >= 50 {
		return ErrConflict
	}
	now := time.Now().UTC()
	if _, err := tx.ExecContext(ctx, "UPDATE approvals SET updated_at=? WHERE id=?", now.UnixMilli(), id); err != nil {
		return err
	}
	if err := s.approvalEvent(ctx, tx, id, "investigated", actor, detail, now); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) CancelApproval(ctx context.Context, id, issuer, subject, reason string) error {
	if issuer == "" || subject == "" || !validApprovalReason(reason) {
		return errors.New("invalid cancellation")
	}
	return s.endUnstartedApproval(ctx, id, "cancelled", issuer, subject, ApprovalDetail{Reason: reason})
}

func (s *Store) endUnstartedApproval(ctx context.Context, id, status, issuer, subject string, detail ApprovalDetail) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UTC()
	query := "UPDATE approvals SET status=?, updated_at=? WHERE id=? AND status IN ('pending','approved')"
	args := []any{status, now.UnixMilli(), id}
	if issuer != "" {
		query += " AND issuer=? AND subject=? AND expires_at>?"
		args = append(args, issuer, subject, now.UnixMilli())
	}
	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return ErrConflict
	}
	actor := subject
	if actor == "" {
		actor = "system"
	}
	if err := s.approvalEvent(ctx, tx, id, status, actor, detail, now); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) MaintainApprovals(ctx context.Context, retention time.Duration) error {
	if _, err := s.recoverApprovalBatch(ctx, false); err != nil {
		return err
	}
	if err := s.remindApprovals(ctx); err != nil {
		return err
	}
	// Bound each maintenance transaction; the next minute continues a large
	// backlog without monopolizing the single-instance configuration writer.
	_, err := s.db.ExecContext(ctx, `DELETE FROM approvals WHERE id IN (SELECT id FROM approvals WHERE status NOT IN ('pending','approved','executing') AND updated_at<? AND NOT EXISTS (SELECT 1 FROM approval_deliveries WHERE approval_id=approvals.id AND archive_due>0) ORDER BY updated_at LIMIT 500)`, time.Now().Add(-retention).UnixMilli())
	if err == nil {
		_, err = s.db.ExecContext(ctx, `DELETE FROM approval_deliveries WHERE sequence IN (SELECT sequence FROM approval_deliveries WHERE notify_due=0 AND archive_due=0 ORDER BY sequence LIMIT 500)`)
	}
	return err
}

func (s *Store) recoverApprovalBatch(ctx context.Context, startup bool) (int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	now := time.Now().UTC()
	query := "SELECT id, status FROM approvals WHERE status IN ('pending','approved') AND expires_at<=? LIMIT 500"
	args := []any{now.UnixMilli()}
	if startup {
		query = "SELECT id, status FROM approvals WHERE status IN ('pending','approved','executing') LIMIT 500"
		args = nil
	}
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	type entry struct{ id, status string }
	var values []entry
	for rows.Next() {
		var item entry
		if err := rows.Scan(&item.id, &item.status); err != nil {
			rows.Close()
			return 0, err
		}
		values = append(values, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, err
	}
	for _, value := range values {
		status := "expired"
		if value.status == "executing" {
			status = "unknown"
		}
		if _, err := tx.ExecContext(ctx, "UPDATE approvals SET status=?, updated_at=? WHERE id=?", status, now.UnixMilli(), value.id); err != nil {
			return 0, err
		}
		if err := s.approvalEvent(ctx, tx, value.id, status, "system", ApprovalDetail{}, now); err != nil {
			return 0, err
		}
	}
	return len(values), tx.Commit()
}
