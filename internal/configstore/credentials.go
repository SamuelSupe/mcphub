package configstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// CredentialBinding contains no upstream tokens. The secret is stored in Vault;
// the revision prevents a delayed OAuth callback from undoing a disconnect.
type CredentialBinding struct {
	ID          string    `json:"id"`
	Issuer      string    `json:"issuer"`
	Subject     string    `json:"subject"`
	EndpointID  string    `json:"endpoint_id"`
	EndpointUID string    `json:"endpoint_uid"`
	Policy      string    `json:"policy"`
	Revision    int64     `json:"revision"`
	Connected   bool      `json:"connected"`
	Account     string    `json:"account"`
	AccountID   string    `json:"account_id"`
	Scopes      []string  `json:"scopes"`
	ExpiresAt   time.Time `json:"expires_at"`
	Renewable   bool      `json:"renewable"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func credentialBindingKey(issuer, subject, uid string) string {
	data, _ := json.Marshal([]string{issuer, subject, uid})
	return SecretHash(string(data))
}

func (s *Store) CredentialBinding(ctx context.Context, issuer, subject, uid string) (CredentialBinding, error) {
	key := credentialBindingKey(issuer, subject, uid)
	var data []byte
	err := s.db.QueryRowContext(ctx, "SELECT data FROM credential_bindings WHERE id=?", key).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return CredentialBinding{Issuer: issuer, Subject: subject, EndpointUID: uid}, nil
	}
	if err != nil {
		return CredentialBinding{}, err
	}
	plain, err := s.open("credential-binding:"+key, data)
	if err != nil {
		return CredentialBinding{}, err
	}
	var b CredentialBinding
	err = json.Unmarshal(plain, &b)
	return b, err
}

func (s *Store) SaveCredentialBinding(ctx context.Context, b CredentialBinding, expected int64) (CredentialBinding, error) {
	if b.Issuer == "" || b.Subject == "" || b.EndpointUID == "" || b.EndpointID == "" {
		return CredentialBinding{}, fmt.Errorf("credential identity is required")
	}
	s.grantMu.Lock()
	defer s.grantMu.Unlock()
	previous, err := s.CredentialBinding(ctx, b.Issuer, b.Subject, b.EndpointUID)
	if err != nil {
		return CredentialBinding{}, err
	}
	if previous.Revision != expected {
		return CredentialBinding{}, ErrConflict
	}
	key := credentialBindingKey(b.Issuer, b.Subject, b.EndpointUID)
	b.Revision, b.UpdatedAt = expected+1, time.Now().UTC()
	plain, err := json.Marshal(b)
	if err != nil {
		return CredentialBinding{}, err
	}
	data, err := s.seal("credential-binding:"+key, plain)
	if err != nil {
		return CredentialBinding{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return CredentialBinding{}, err
	}
	defer tx.Rollback()
	var result sql.Result
	if expected == 0 {
		result, err = tx.ExecContext(ctx, "INSERT INTO credential_bindings(id,owner,revision,data) VALUES(?,?,?,?) ON CONFLICT(id) DO NOTHING", key, credentialBindingKey(b.Issuer, b.Subject, ""), b.Revision, data)
	} else {
		result, err = tx.ExecContext(ctx, "UPDATE credential_bindings SET revision=?,data=? WHERE id=? AND revision=?", b.Revision, data, key, expected)
	}
	if err != nil {
		return CredentialBinding{}, err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return CredentialBinding{}, ErrConflict
	}
	if b.Connected {
		if _, err := tx.ExecContext(ctx, "DELETE FROM credential_cleanup WHERE id=?", b.ID); err != nil {
			return CredentialBinding{}, err
		}
	}
	if previous.ID != "" && (previous.ID != b.ID || !b.Connected) {
		if _, err := tx.ExecContext(ctx, "INSERT INTO credential_cleanup(id,due_at) VALUES(?,?) ON CONFLICT(id) DO UPDATE SET due_at=excluded.due_at", previous.ID, time.Now().Unix()); err != nil {
			return CredentialBinding{}, err
		}
	}
	// Existing client consent cannot silently transfer to a different upstream
	// account. The initial account connection can fulfill an existing consent.
	var grantIDs []string
	if expected > 0 {
		rows, err := tx.QueryContext(ctx, "SELECT id,status,revision,data FROM client_grants WHERE issuer=? AND subject=? AND endpoint_id=? AND status IN ('active','confirmed','pending')", b.Issuer, b.Subject, b.EndpointID)
		if err != nil {
			return CredentialBinding{}, err
		}
		var grants []ClientGrant
		for rows.Next() {
			g, err := s.scanGrant(rows)
			if err != nil {
				rows.Close()
				return CredentialBinding{}, err
			}
			grants = append(grants, g)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return CredentialBinding{}, err
		}
		for _, g := range grants {
			if _, err := tx.ExecContext(ctx, "UPDATE client_grants SET status='reconfirmation_required',revision=revision+1,credential_hash=NULL,exchange_hash=NULL WHERE id=?", g.GrantID); err != nil {
				return CredentialBinding{}, err
			}
			g.Status, g.Revision = "reconfirmation_required", g.Revision+1
			if err := s.grantEvent(ctx, tx, g, "client_authorization_reconfirmation_required"); err != nil {
				return CredentialBinding{}, err
			}
			grantIDs = append(grantIDs, g.GrantID)
		}
	}
	action := "credential_connect"
	if !b.Connected {
		action = "credential_disconnect"
	}
	if err := insertEvent(ctx, tx, b.EndpointID, action, true, "", b.Revision, b.UpdatedAt); err != nil {
		return CredentialBinding{}, err
	}
	if err := tx.Commit(); err != nil {
		return CredentialBinding{}, err
	}
	for _, id := range grantIDs {
		s.cancelGrantLocked(id)
	}
	return b, nil
}

func (s *Store) ScheduleCredentialCleanup(ctx context.Context, id string, due time.Time) error {
	_, err := s.db.ExecContext(ctx, "INSERT INTO credential_cleanup(id,due_at) VALUES(?,?) ON CONFLICT(id) DO NOTHING", id, due.Unix())
	return err
}

func (s *Store) CredentialCleanup(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id FROM credential_cleanup WHERE due_at<=? ORDER BY due_at LIMIT 100", time.Now().Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (s *Store) FinishCredentialCleanup(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, "DELETE FROM credential_cleanup WHERE id=?", id)
	return err
}

func (s *Store) ListCredentialBindings(ctx context.Context, issuer, subject string) ([]CredentialBinding, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id,data FROM credential_bindings WHERE owner=?", credentialBindingKey(issuer, subject, ""))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []CredentialBinding{}
	for rows.Next() {
		var key string
		var data []byte
		if err := rows.Scan(&key, &data); err != nil {
			return nil, err
		}
		plain, err := s.open("credential-binding:"+key, data)
		if err != nil {
			return nil, err
		}
		var b CredentialBinding
		if err := json.Unmarshal(plain, &b); err != nil {
			return nil, err
		}
		result = append(result, b)
	}
	return result, rows.Err()
}
