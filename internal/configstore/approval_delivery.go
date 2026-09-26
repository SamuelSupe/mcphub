package configstore

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/SamuelSupe/mcphub/v2/internal/config"
)

type AuditEnvelope struct {
	Entry     json.RawMessage `json:"entry"`
	Hash      string          `json:"hash"`
	Signature string          `json:"signature"`
}

type AuditEntry struct {
	ObjectKind   string         `json:"object_kind,omitempty"`
	Grant        *ClientGrant   `json:"client_grant,omitempty"`
	Sequence     int64          `json:"sequence"`
	PreviousHash string         `json:"previous_hash"`
	KeyID        string         `json:"key_id"`
	EventID      string         `json:"event_id"`
	ApprovalID   string         `json:"approval_id"`
	Action       string         `json:"action"`
	Actor        string         `json:"actor"`
	CreatedAt    time.Time      `json:"created_at"`
	IntentHash   string         `json:"intent_hash"`
	ResultHash   string         `json:"result_hash,omitempty"`
	Detail       ApprovalDetail `json:"detail"`
}

type deliveryHead struct {
	Delivery int64  `json:"delivery"`
	Sequence int64  `json:"sequence"`
	Hash     string `json:"hash"`
}

// ConfigureApprovalDelivery must run before any store workers start. Delivery
// secrets are static process configuration and never enter the management API.
func (s *Store) ConfigureApprovalDelivery(settings config.ApprovalSettings, publicURL string) {
	s.delivery, s.approvalURL = settings, publicURL
}

func (s *Store) enqueueApprovalEvent(ctx context.Context, tx *transaction, eventID, id, action, actor string, detail ApprovalDetail, now time.Time) error {
	if s.delivery.Notifications.URL == "" && s.delivery.AuditArchive.URL == "" {
		return nil
	}
	a, err := s.scanApproval(tx.QueryRowContext(ctx, "SELECT "+approvalColumns+" FROM approvals WHERE id=?", id))
	if err != nil {
		return err
	}
	return s.enqueueGovernanceEvent(ctx, tx, eventID, id, action, actor, detail, now, a.Intent, a.Result, a.ExpiresAt, nil)
}

func (s *Store) enqueueAuthorizationEvent(ctx context.Context, tx *transaction, g ClientGrant, action string, detail ApprovalDetail) error {
	now, eventID := time.Now().UTC(), rand.Text()
	actor := Actor(ctx)
	if actor == "local" {
		actor = g.Subject
	}
	payload, err := json.Marshal(map[string]any{"grant": g, "action": action, "actor": actor, "detail": detail, "created_at": now})
	if err != nil {
		return err
	}
	sealed, err := s.seal("client-event:"+eventID, payload)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO client_authorization_events(id,grant_id,data,created_at) VALUES(?,?,?,?)", eventID, g.GrantID, sealed, now.UnixMilli()); err != nil {
		return err
	}
	return s.enqueueGovernanceEvent(ctx, tx, eventID, g.GrantID, action, actor, detail, now, g, nil, g.ExpiresAt, &g)
}

func (s *Store) enqueueGovernanceEvent(ctx context.Context, tx *transaction, eventID, id, action, actor string, detail ApprovalDetail, now time.Time, intentValue any, result json.RawMessage, expires time.Time, grant *ClientGrant) error {
	if s.delivery.Notifications.URL == "" && s.delivery.AuditArchive.URL == "" {
		return nil
	}
	var head deliveryHead
	var sealed []byte
	err := tx.QueryRowContext(ctx, "SELECT value FROM metadata WHERE key='approval_delivery_head'").Scan(&sealed)
	if err == nil {
		plain, err := s.open("approval-delivery-head", sealed)
		if err != nil {
			return err
		}
		if err := json.Unmarshal(plain, &head); err != nil {
			return err
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	head.Delivery++
	var envelope []byte
	var notifyDue, archiveDue int64
	if s.delivery.AuditArchive.URL != "" {
		intent, err := json.Marshal(intentValue)
		if err != nil {
			return err
		}
		entry := AuditEntry{Sequence: head.Sequence + 1, PreviousHash: head.Hash, KeyID: s.delivery.AuditArchive.KeyID, EventID: eventID, ApprovalID: id, Action: action, Actor: actor, CreatedAt: now, IntentHash: fmt.Sprintf("%x", sha256.Sum256(intent)), Detail: detail}
		if len(result) > 0 {
			entry.ResultHash = fmt.Sprintf("%x", sha256.Sum256(result))
		}
		if grant != nil {
			entry.ObjectKind, entry.Grant = "client_grant", grant
		}
		data, err := json.Marshal(entry)
		if err != nil {
			return err
		}
		digest := sha256.Sum256(data)
		key, err := base64.StdEncoding.DecodeString(s.delivery.AuditArchive.SigningKey)
		if err != nil || len(key) != ed25519.PrivateKeySize {
			return errors.New("invalid audit signing key")
		}
		value := AuditEnvelope{Entry: data, Hash: hex.EncodeToString(digest[:]), Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(key, digest[:]))}
		envelope, err = json.Marshal(value)
		if err != nil {
			return err
		}
		head.Sequence, head.Hash = entry.Sequence, value.Hash
		archiveDue = now.UnixMilli()
	}
	notification, err := json.Marshal(map[string]any{"event_id": eventID, "approval_id": id, "action": action, "approval_url": s.approvalURL + "/?approval=" + id + "#approvals", "expires_at": expires})
	if err != nil {
		return err
	}
	if s.delivery.Notifications.URL != "" && grant == nil {
		notifyDue = now.UnixMilli()
	}
	envelope, err = s.seal("approval-archive:"+eventID, envelope)
	if err != nil {
		return err
	}
	notification, err = s.seal("approval-notification:"+eventID, notification)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO approval_deliveries(sequence,event_id,approval_id,envelope,notification,notify_due,archive_due) VALUES(?,?,?,?,?,?,?)`, head.Delivery, eventID, id, envelope, notification, notifyDue, archiveDue); err != nil {
		return err
	}
	plain, err := json.Marshal(head)
	if err != nil {
		return err
	}
	sealed, err = s.seal("approval-delivery-head", plain)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO metadata(key,value) VALUES('approval_delivery_head',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", sealed)
	return err
}

// VerifyAuditEnvelope checks a contiguous archive against a trusted public key
// and checkpoint. Keep the resulting checkpoint outside the configuration DB to
// detect a deleted tail or rollback on a later comparison.
func VerifyAuditEnvelope(value AuditEnvelope, keys map[string]ed25519.PublicKey, previousSequence int64, previousHash string) (AuditEntry, error) {
	var entry AuditEntry
	if json.Unmarshal(value.Entry, &entry) != nil {
		return entry, errors.New("invalid audit entry")
	}
	digest := sha256.Sum256(value.Entry)
	signature, err := base64.StdEncoding.DecodeString(value.Signature)
	key := keys[entry.KeyID]
	if err != nil || len(key) != ed25519.PublicKeySize || value.Hash != hex.EncodeToString(digest[:]) || !ed25519.Verify(key, digest[:], signature) || entry.Sequence != previousSequence+1 || entry.PreviousHash != previousHash {
		return entry, errors.New("audit signature or chain verification failed")
	}
	return entry, nil
}

type ApprovalDelivery struct {
	Sequence int64
	EventID  string
	Payload  []byte
	Attempts int
}

func (s *Store) NextApprovalDelivery(ctx context.Context, archive bool) (ApprovalDelivery, error) {
	column, payload, label := "notify", "notification", "approval-notification:"
	if archive {
		column, payload, label = "archive", "envelope", "approval-archive:"
	}
	var value ApprovalDelivery
	var sealed []byte
	var due int64
	// A failed archive record blocks subsequent records, preserving chain order.
	err := s.db.QueryRowContext(ctx, "SELECT sequence,event_id,"+payload+","+column+"_attempts,"+column+"_due FROM approval_deliveries WHERE "+column+"_due>0 ORDER BY sequence LIMIT 1").Scan(&value.Sequence, &value.EventID, &sealed, &value.Attempts, &due)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && due > time.Now().UnixMilli()) {
		return value, ErrNotFound
	}
	if err != nil {
		return value, err
	}
	value.Payload, err = s.open(label+value.EventID, sealed)
	return value, err
}

func (s *Store) CompleteApprovalDelivery(ctx context.Context, value ApprovalDelivery, archive, success bool) error {
	column := "notify"
	if archive {
		column = "archive"
	}
	var due int64
	if !success {
		due = time.Now().Add(min(time.Hour, 5*time.Second*time.Duration(1<<min(value.Attempts, 10)))).UnixMilli()
	}
	_, err := s.db.ExecContext(ctx, "UPDATE approval_deliveries SET "+column+"_due=?, "+column+"_attempts="+column+"_attempts+1 WHERE sequence=?", due, value.Sequence)
	return err
}

type ApprovalDeliveryStatus struct {
	NotificationsEnabled bool `json:"notifications_enabled"`
	ArchiveEnabled       bool `json:"archive_enabled"`
	NotificationsPending int  `json:"notifications_pending"`
	ArchivePending       int  `json:"archive_pending"`
	NotificationsFailed  int  `json:"notifications_failed"`
	ArchiveFailed        int  `json:"archive_failed"`
}

func (s *Store) ApprovalDeliveryStatus(ctx context.Context) (ApprovalDeliveryStatus, error) {
	value := ApprovalDeliveryStatus{NotificationsEnabled: s.delivery.Notifications.URL != "", ArchiveEnabled: s.delivery.AuditArchive.URL != ""}
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(CASE WHEN notify_due>0 THEN 1 ELSE 0 END),0), COALESCE(SUM(CASE WHEN archive_due>0 THEN 1 ELSE 0 END),0), COALESCE(SUM(CASE WHEN notify_due>0 AND notify_attempts>0 THEN 1 ELSE 0 END),0), COALESCE(SUM(CASE WHEN archive_due>0 AND archive_attempts>0 THEN 1 ELSE 0 END),0) FROM approval_deliveries`).Scan(&value.NotificationsPending, &value.ArchivePending, &value.NotificationsFailed, &value.ArchiveFailed)
	return value, err
}

func (s *Store) remindApprovals(ctx context.Context) error {
	if s.delivery.Notifications.URL == "" {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UTC()
	rows, err := tx.QueryContext(ctx, `SELECT id FROM approvals WHERE status IN ('pending','approved') AND expires_at>? AND expires_at<=? AND NOT EXISTS (SELECT 1 FROM approval_events WHERE approval_id=approvals.id AND action='reminded') LIMIT 500`, now.UnixMilli(), now.Add(5*time.Minute).UnixMilli())
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err := s.approvalEvent(ctx, tx, id, "reminded", "system", ApprovalDetail{}, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}
