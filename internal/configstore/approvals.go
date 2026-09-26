package configstore

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/SamuelSupe/mcphub/v2/internal/config"
)

const ApprovalTTL = 5 * time.Minute

var ErrApprovalLimit = errors.New("too many pending approvals")
var ErrApprovalCursor = errors.New("invalid approval cursor")
var ErrOperationConflict = errors.New("operation ID was already used for a different or retired request")

// Params includes arguments, request state and execution metadata. The complete
// intent and result are encrypted with the configuration database key.
type ApprovalIntent struct {
	ClientGrant       *GrantBinding               `json:"client_grant,omitempty"`
	EndpointUID       string                      `json:"endpoint_uid,omitempty"`
	ConfigurationHash string                      `json:"configuration_hash,omitempty"`
	Kind              string                      `json:"kind,omitempty"`
	Change            json.RawMessage             `json:"change,omitempty"`
	OperationID       string                      `json:"operation_id,omitempty"`
	Issuer            string                      `json:"issuer"`
	Subject           string                      `json:"subject"`
	Tool              string                      `json:"tool"`
	BackendID         string                      `json:"backend_id"`
	Target            string                      `json:"target"`
	Effect            string                      `json:"effect"`
	Policy            string                      `json:"policy"`
	Params            json.RawMessage             `json:"params"`
	Rules             []config.ToolApprovalPolicy `json:"rules,omitempty"`
	Summary           []ApprovalSummary           `json:"summary,omitempty"`
	Preview           json.RawMessage             `json:"preview,omitempty"`
	PreviewPolicy     string                      `json:"preview_policy,omitempty"`
	PendingTTL        time.Duration               `json:"pending_ttl,omitempty"`
	ExecutionTTL      time.Duration               `json:"execution_ttl,omitempty"`
}

type ApprovalSummary struct {
	Action          string                     `json:"action"`
	Environment     string                     `json:"environment,omitempty"`
	Resources       map[string]json.RawMessage `json:"resources,omitempty"`
	VersionArgument string                     `json:"version_argument,omitempty"`
	Version         string                     `json:"version,omitempty"`
}

type Approval struct {
	ApprovedBy []string        `json:"approved_by"`
	Reused     bool            `json:"reused,omitempty"`
	ID         string          `json:"id"`
	Status     string          `json:"status"`
	Reviewer   string          `json:"reviewer"`
	CreatedAt  time.Time       `json:"created_at"`
	ExpiresAt  time.Time       `json:"expires_at"`
	UpdatedAt  time.Time       `json:"updated_at"`
	Intent     ApprovalIntent  `json:"intent"`
	Result     json.RawMessage `json:"-"`
}

func (s *Store) CreateApproval(ctx context.Context, intent ApprovalIntent) (Approval, error) {
	plain, err := json.Marshal(intent)
	if err != nil {
		return Approval{}, err
	}
	limit := 64 << 10
	if intent.Kind == "configuration" {
		limit = 32 << 20
	}
	if len(plain) > limit {
		return Approval{}, errors.New("approval request exceeds storage limit")
	}
	now := time.Now().UTC()
	wait := intent.PendingTTL
	if wait <= 0 {
		wait = ApprovalTTL
	}
	a := Approval{ID: rand.Text(), Status: "pending", CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(wait), Intent: intent}
	sealed, err := s.seal("approval:"+a.ID, plain)
	if err != nil {
		return Approval{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Approval{}, err
	}
	defer tx.Rollback()
	operationKey, requestHash := approvalOperation(intent)
	if operationKey != "" {
		var id, hash string
		err := tx.QueryRowContext(ctx, "SELECT approval_id, request_hash FROM approval_operations WHERE operation_key=?", operationKey).Scan(&id, &hash)
		if errors.Is(err, sql.ErrNoRows) && (intent.ClientGrant != nil || intent.EndpointUID != "") {
			legacy := intent
			legacy.ClientGrant = nil
			legacy.EndpointUID = ""
			legacyKey, _ := approvalOperation(legacy)
			err = tx.QueryRowContext(ctx, "SELECT approval_id, request_hash FROM approval_operations WHERE operation_key=?", legacyKey).Scan(&id, &hash)
		}
		if err == nil {
			if hash != requestHash {
				return Approval{}, ErrOperationConflict
			}
			existing, err := s.scanApproval(tx.QueryRowContext(ctx, "SELECT "+approvalColumns+" FROM approvals WHERE id=?", id))
			if errors.Is(err, ErrNotFound) {
				return Approval{}, ErrOperationConflict
			}
			if err != nil {
				return Approval{}, err
			}
			if err := s.loadApprovalVotes(ctx, tx, &existing); err != nil {
				return Approval{}, err
			}
			if (existing.Intent.ClientGrant == nil) != (intent.ClientGrant == nil) || (intent.ClientGrant != nil && *existing.Intent.ClientGrant != *intent.ClientGrant) {
				return Approval{}, ErrOperationConflict
			}
			existing.Reused = true
			return existing, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return Approval{}, err
		}
	}
	if operationKey == "" {
		rows, err := tx.QueryContext(ctx, "SELECT "+approvalColumns+" FROM approvals WHERE issuer=? AND subject=? AND status IN ('pending','approved') AND expires_at>? LIMIT 20", intent.Issuer, intent.Subject, now.UnixMilli())
		if err != nil {
			return Approval{}, err
		}
		var duplicate *Approval
		for rows.Next() {
			existing, err := s.scanApproval(rows)
			if err != nil {
				rows.Close()
				return Approval{}, err
			}
			if sameApprovalIntent(existing.Intent, intent) {
				duplicate = &existing
				break
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return Approval{}, err
		}
		if duplicate != nil {
			if err := s.loadApprovalVotes(ctx, tx, duplicate); err != nil {
				return Approval{}, err
			}
			duplicate.Reused = true
			return *duplicate, nil
		}
	}
	var count int
	err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM approvals WHERE issuer=? AND subject=? AND status IN ('pending','approved') AND expires_at>?`, intent.Issuer, intent.Subject, now.UnixMilli()).Scan(&count)
	if err != nil {
		return Approval{}, err
	}
	if count >= 20 {
		return Approval{}, ErrApprovalLimit
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO approvals(id, issuer, subject, intent, status, reviewer, created_at, expires_at, updated_at) VALUES(?, ?, ?, ?, 'pending', '', ?, ?, ?)`, a.ID, intent.Issuer, intent.Subject, sealed, now.UnixMilli(), a.ExpiresAt.UnixMilli(), now.UnixMilli())
	if err != nil {
		return Approval{}, err
	}
	if operationKey != "" {
		if _, err := tx.ExecContext(ctx, "INSERT INTO approval_operations(operation_key, request_hash, approval_id) VALUES(?, ?, ?)", operationKey, requestHash, a.ID); err != nil {
			return Approval{}, err
		}
	}
	if err := s.approvalEvent(ctx, tx, a.ID, "requested", intent.Subject, ApprovalDetail{}, now); err != nil {
		return Approval{}, err
	}
	return a, tx.Commit()
}

const approvalColumns = "id, intent, status, reviewer, created_at, expires_at, updated_at, result"

func (s *Store) scanApproval(row rowScanner) (Approval, error) {
	var a Approval
	var sealed, result []byte
	var created, expires, updated int64
	if err := row.Scan(&a.ID, &sealed, &a.Status, &a.Reviewer, &created, &expires, &updated, &result); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return a, ErrNotFound
		}
		return a, err
	}
	plain, err := s.open("approval:"+a.ID, sealed)
	if err != nil {
		return a, err
	}
	if err := json.Unmarshal(plain, &a.Intent); err != nil {
		return a, err
	}
	a.CreatedAt, a.ExpiresAt, a.UpdatedAt = time.UnixMilli(created).UTC(), time.UnixMilli(expires).UTC(), time.UnixMilli(updated).UTC()
	if len(result) > 0 {
		a.Result, err = s.open("approval-result:"+a.ID, result)
		if err != nil {
			return a, err
		}
	}
	if (a.Status == "pending" || a.Status == "approved") && !time.Now().Before(a.ExpiresAt) {
		a.Status = "expired"
	}
	return a, nil
}

func (s *Store) GetApproval(ctx context.Context, id string) (Approval, error) {
	a, err := s.scanApproval(s.db.QueryRowContext(ctx, "SELECT "+approvalColumns+" FROM approvals WHERE id=?", id))
	if err == nil {
		err = s.loadApprovalVotes(ctx, s.db, &a)
	}
	return a, err
}

type ApprovalQuery struct {
	Status, Subject, Cursor string
	Limit                   int
}

func ApprovalCursor(a Approval) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(a.CreatedAt.UnixMilli(), 10) + ":" + a.ID))
}

func (s *Store) ListApprovals(ctx context.Context, query ApprovalQuery) ([]Approval, error) {
	// Return the recent audit trail; results stay private to the original caller.
	statement := `SELECT id, intent, CASE WHEN status IN ('pending','approved') AND expires_at<=? THEN 'expired' ELSE status END, reviewer, created_at, expires_at, updated_at, NULL FROM approvals WHERE 1=1`
	args := []any{time.Now().UnixMilli()}
	if query.Subject != "" {
		statement += " AND subject=?"
		args = append(args, query.Subject)
	}
	if query.Status != "" {
		statement += " AND CASE WHEN status IN ('pending','approved') AND expires_at<=? THEN 'expired' ELSE status END=?"
		args = append(args, time.Now().UnixMilli(), query.Status)
	}
	if query.Cursor != "" {
		raw, err := base64.RawURLEncoding.DecodeString(query.Cursor)
		stamp, id, ok := strings.Cut(string(raw), ":")
		at, parseErr := strconv.ParseInt(stamp, 10, 64)
		if err != nil || !ok || parseErr != nil || len(id) != 26 {
			return nil, ErrApprovalCursor
		}
		statement += " AND (created_at<? OR (created_at=? AND id<?))"
		args = append(args, at, at, id)
	}
	limit := query.Limit
	if limit < 1 || limit > 100 {
		limit = 100
	}
	statement += " ORDER BY created_at DESC, id DESC LIMIT ?"
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, statement, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := make([]Approval, 0)
	for rows.Next() {
		a, err := s.scanApproval(rows)
		if err != nil {
			return nil, err
		}
		values = append(values, a)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	for i := range values {
		if err := s.loadApprovalVotes(ctx, s.db, &values[i]); err != nil {
			return nil, err
		}
	}
	return values, nil
}

func (s *Store) DecideApproval(ctx context.Context, id, decision, reviewer string, detail ApprovalDetail) error {
	if (decision != "approved" && decision != "rejected" && decision != "revoked") || reviewer == "" || !validApprovalReason(detail.Reason) {
		return errors.New("invalid approval decision")
	}
	from := "pending"
	var expiry time.Time
	if decision == "approved" {
		return s.voteApproval(ctx, id, reviewer, detail)
	}
	if decision == "revoked" {
		from = "approved"
	}
	return s.transitionApproval(ctx, id, from, decision, reviewer, nil, true, expiry, detail)
}

// Operation identities survive history retention, so a delayed client cannot
// accidentally reuse an old key after its result has been purged.
func approvalOperation(intent ApprovalIntent) (string, string) {
	if intent.OperationID == "" {
		return "", ""
	}
	endpoint := intent.BackendID
	if intent.EndpointUID != "" {
		endpoint = intent.EndpointUID
	}
	if intent.ClientGrant != nil {
		endpoint = intent.ClientGrant.EndpointUID
	}
	namespace, _ := json.Marshal([]string{intent.Issuer, intent.Subject, intent.Kind, endpoint, intent.Tool, intent.OperationID})
	canonical := canonicalApprovalParams(intent.Params)
	if intent.Kind == "configuration" {
		canonical = intent.Change
	}
	return fmt.Sprintf("%x", sha256.Sum256(namespace)), fmt.Sprintf("%x", sha256.Sum256(canonical))
}

func canonicalApprovalParams(params json.RawMessage) []byte {
	var request map[string]any
	decoder := json.NewDecoder(strings.NewReader(string(params)))
	decoder.UseNumber()
	if decoder.Decode(&request) == nil {
		if meta, ok := request["_meta"].(map[string]any); ok {
			delete(meta, "progressToken")
			if len(meta) == 0 {
				delete(request, "_meta")
			}
		}
	}
	canonical, _ := json.Marshal(request)
	return canonical
}

func sameApprovalIntent(a, b ApprovalIntent) bool {
	a.Params, b.Params = canonicalApprovalParams(a.Params), canonicalApprovalParams(b.Params)
	x, err := json.Marshal(a)
	y, other := json.Marshal(b)
	return err == nil && other == nil && string(x) == string(y)
}

type approvalQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func (s *Store) loadApprovalVotes(ctx context.Context, db approvalQueryer, a *Approval) error {
	rows, err := db.QueryContext(ctx, "SELECT reviewer FROM approval_votes WHERE approval_id=? ORDER BY created_at, reviewer", a.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	a.ApprovedBy = []string{}
	for rows.Next() {
		var reviewer string
		if err := rows.Scan(&reviewer); err != nil {
			return err
		}
		a.ApprovedBy = append(a.ApprovedBy, reviewer)
	}
	return rows.Err()
}

func (s *Store) voteApproval(ctx context.Context, id, reviewer string, detail ApprovalDetail) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	a, err := s.scanApproval(tx.QueryRowContext(ctx, "SELECT "+approvalColumns+" FROM approvals WHERE id=?", id))
	if err != nil {
		return err
	}
	if a.Status != "pending" {
		return ErrConflict
	}
	if err := s.loadApprovalVotes(ctx, tx, &a); err != nil {
		return err
	}
	if slices.Contains(a.ApprovedBy, reviewer) || (config.ApprovalQuorum(a.Intent.Rules) == 2 && reviewer == a.Intent.Subject) {
		return ErrConflict
	}
	now := time.Now().UTC()
	if _, err := tx.ExecContext(ctx, "INSERT INTO approval_votes(approval_id, reviewer, created_at) VALUES(?, ?, ?)", id, reviewer, now.UnixMilli()); err != nil {
		return err
	}
	action := "reviewed"
	if len(a.ApprovedBy)+1 >= config.ApprovalQuorum(a.Intent.Rules) {
		a.Status, action = "approved", "approved"
		duration := a.Intent.ExecutionTTL
		if duration <= 0 {
			duration = ApprovalTTL
		}
		a.ExpiresAt = now.Add(duration)
	}
	if _, err := tx.ExecContext(ctx, "UPDATE approvals SET status=?, reviewer=?, updated_at=?, expires_at=? WHERE id=?", a.Status, reviewer, now.UnixMilli(), a.ExpiresAt.UnixMilli(), id); err != nil {
		return err
	}
	if err := s.approvalEvent(ctx, tx, id, action, reviewer, detail, now); err != nil {
		return err
	}
	return tx.Commit()
}

// ClaimApproval is the only admission to execution. Concurrent resumes can win
// once; neither expiry nor a process restart ever resets this state to approved.
func (s *Store) ClaimApproval(ctx context.Context, id string) error {
	return s.transitionApproval(ctx, id, "approved", "executing", "", nil, true, time.Time{}, ApprovalDetail{})
}

func (s *Store) FinishApproval(ctx context.Context, id, status string, result json.RawMessage) error {
	if status != "succeeded" && status != "failed" && status != "unknown" {
		return errors.New("invalid execution status")
	}
	var sealed []byte
	var err error
	if len(result) > 0 {
		sealed, err = s.seal("approval-result:"+id, result)
		if err != nil {
			return err
		}
	}
	return s.transitionApproval(ctx, id, "executing", status, "", sealed, false, time.Time{}, ApprovalDetail{})
}

func (s *Store) ExpireApproval(ctx context.Context, id string) error {
	return s.endUnstartedApproval(ctx, id, "expired", "", "", ApprovalDetail{})
}

// MCPHub supports one process per database. An interrupted upstream write has an
// unknown outcome, and requires investigation instead of an automatic replay.
func (s *Store) RecoverApprovals(ctx context.Context) error {
	for {
		count, err := s.recoverApprovalBatch(ctx, true)
		if err != nil || count < 500 {
			return err
		}
	}
}

func (s *Store) transitionApproval(ctx context.Context, id, from, to, reviewer string, result []byte, checkExpiry bool, expiry time.Time, detail ApprovalDetail) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UTC()
	query := `UPDATE approvals SET status=?, reviewer=CASE WHEN ?='' THEN reviewer ELSE ? END, updated_at=?, result=?, expires_at=CASE WHEN CAST(? AS BIGINT)=0 THEN expires_at ELSE ? END WHERE id=? AND status=?`
	var expires int64
	if !expiry.IsZero() {
		expires = expiry.UnixMilli()
	}
	args := []any{to, reviewer, reviewer, now.UnixMilli(), result, expires, expires, id, from}
	if checkExpiry {
		query += " AND expires_at>?"
		args = append(args, now.UnixMilli())
	}
	changed, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	count, err := changed.RowsAffected()
	if err != nil {
		return err
	}
	if count != 1 {
		return ErrConflict
	}
	actor := reviewer
	if actor == "" {
		actor = Actor(ctx)
	}
	if err := s.approvalEvent(ctx, tx, id, to, actor, detail, now); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) approvalEvent(ctx context.Context, tx *transaction, id, action, actor string, detail ApprovalDetail, now time.Time) error {
	eventID := rand.Text()
	plain, err := json.Marshal(detail)
	if err != nil {
		return err
	}
	sealed, err := s.seal("approval-event:"+eventID, plain)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO approval_events(id, approval_id, action, actor, detail, created_at) VALUES(?, ?, ?, ?, ?, ?)`, eventID, id, action, actor, sealed, now.UnixMilli()); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO events(backend_id, source_kind, source_id, actor, action, success, message, created_at) VALUES('', 'approval', ?, ?, ?, 1, '', ?)`, id, actor, "approval_"+action, now.Format(time.RFC3339Nano))
	if err != nil {
		return fmt.Errorf("record approval event: %w", err)
	}
	return s.enqueueApprovalEvent(ctx, tx, eventID, id, action, actor, detail, now)
}
