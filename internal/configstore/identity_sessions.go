package configstore

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

type SSOSession struct {
	CredentialVersion int64     `json:"credential_version,omitempty"`
	ID                string    `json:"id"`
	UserID            string    `json:"user_id"`
	ClientID          string    `json:"client_id"`
	Resource          string    `json:"resource"`
	Scopes            []string  `json:"scopes"`
	ExpiresAt         time.Time `json:"expires_at"`
}

func (s *Store) SSOSigningKey(ctx context.Context) (ed25519.PrivateKey, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var sealed []byte
	err = tx.QueryRowContext(ctx, "SELECT value FROM metadata WHERE key='sso_signing_key'").Scan(&sealed)
	if errors.Is(err, sql.ErrNoRows) {
		_, key, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return nil, err
		}
		sealed, err = s.seal("sso_signing_key", key)
		if err != nil {
			return nil, err
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO metadata(key,value) VALUES('sso_signing_key',?)", sealed); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	plain, err := s.open("sso_signing_key", sealed)
	if err != nil {
		return nil, err
	}
	if len(plain) != ed25519.PrivateKeySize {
		return nil, errors.New("invalid SSO signing key")
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return ed25519.PrivateKey(plain), nil
}

func (s *Store) CreateSSOSession(ctx context.Context, session SSOSession, refresh bool) (SSOSession, string, error) {
	s.identityMu.Lock()
	defer s.identityMu.Unlock()
	p, err := s.effectiveIdentity(ctx, session.UserID)
	if err != nil || p.CredentialVersion != session.CredentialVersion {
		return session, "", ErrIdentityDenied
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return session, "", err
	}
	defer tx.Rollback()
	session, token, err := s.createSSOSessionTx(ctx, tx, session, refresh)
	if err == nil {
		err = tx.Commit()
	}
	return session, token, err
}

func (s *Store) createSSOSessionTx(ctx context.Context, tx *transaction, session SSOSession, refresh bool) (SSOSession, string, error) {
	session.ID = "ss_" + rand.Text()
	data, err := json.Marshal(session)
	if err != nil {
		return session, "", err
	}
	_, err = tx.ExecContext(ctx, "DELETE FROM sso_refresh WHERE session_id IN (SELECT id FROM sso_sessions WHERE expires_at<?)", time.Now().Unix())
	if err == nil {
		_, err = tx.ExecContext(ctx, "DELETE FROM sso_sessions WHERE expires_at<?", time.Now().Unix())
	}
	if err == nil {
		_, err = tx.ExecContext(ctx, "INSERT INTO sso_sessions(id,data,expires_at) VALUES(?,?,?)", session.ID, data, session.ExpiresAt.Unix())
	}
	token := ""
	if err == nil && refresh {
		token = rand.Text() + rand.Text()
		_, err = tx.ExecContext(ctx, "INSERT INTO sso_refresh(hash,session_id,used) VALUES(?,?,0)", SecretHash(token), session.ID)
	}
	return session, token, err
}

func readSSOSession(ctx context.Context, db identityReader, id string) (SSOSession, error) {
	var data []byte
	err := db.QueryRowContext(ctx, "SELECT data FROM sso_sessions WHERE id=? AND expires_at>?", id, time.Now().Unix()).Scan(&data)
	var session SSOSession
	if err == nil {
		err = json.Unmarshal(data, &session)
	}
	return session, err
}

func (s *Store) SSOSession(ctx context.Context, id string) (SSOSession, error) {
	return readSSOSession(ctx, s.db, id)
}

func revokeIdentitySessions(ctx context.Context, tx *transaction, users ...string) error {
	if len(users) == 0 {
		return nil
	}
	owners := make(map[string]bool, len(users))
	for _, id := range users {
		owners[id] = true
	}
	rows, err := tx.QueryContext(ctx, "SELECT data FROM sso_sessions")
	if err != nil {
		return err
	}
	ids := []string{}
	for rows.Next() {
		var data []byte
		var session SSOSession
		if err = rows.Scan(&data); err == nil {
			err = json.Unmarshal(data, &session)
		}
		if err != nil {
			break
		}
		if owners[session.UserID] {
			ids = append(ids, session.ID)
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	for _, sessionID := range ids {
		if _, err := tx.ExecContext(ctx, "DELETE FROM sso_refresh WHERE session_id=?", sessionID); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM sso_sessions WHERE id=?", sessionID); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) SSORefreshSession(ctx context.Context, token string) (SSOSession, error) {
	var data []byte
	err := s.db.QueryRowContext(ctx, "SELECT s.data FROM sso_sessions s JOIN sso_refresh r ON r.session_id=s.id WHERE r.hash=? AND s.expires_at>?", SecretHash(token), time.Now().Unix()).Scan(&data)
	var session SSOSession
	if err == nil {
		err = json.Unmarshal(data, &session)
	}
	return session, err
}

// Retaining consumed hashes detects replay and revokes the refresh family.
func (s *Store) RotateSSORefresh(ctx context.Context, token, client, resource string) (SSOSession, string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return SSOSession{}, "", err
	}
	defer tx.Rollback()
	var id string
	var used int
	if err = tx.QueryRowContext(ctx, "SELECT session_id,used FROM sso_refresh WHERE hash=?", SecretHash(token)).Scan(&id, &used); err != nil {
		return SSOSession{}, "", err
	}
	session, err := readSSOSession(ctx, tx, id)
	if err != nil {
		return session, "", err
	}
	if session.ClientID != client || session.Resource != resource {
		return session, "", ErrIdentityDenied
	}
	if used != 0 {
		if _, err = tx.ExecContext(ctx, "DELETE FROM sso_refresh WHERE session_id=?", id); err == nil {
			_, err = tx.ExecContext(ctx, "DELETE FROM sso_sessions WHERE id=?", id)
		}
		if err == nil {
			err = tx.Commit()
		}
		if err != nil {
			return session, "", err
		}
		return session, "", ErrIdentityDenied
	}
	if _, err = tx.ExecContext(ctx, "UPDATE sso_refresh SET used=1 WHERE hash=?", SecretHash(token)); err != nil {
		return session, "", err
	}
	next := rand.Text() + rand.Text()
	if _, err = tx.ExecContext(ctx, "INSERT INTO sso_refresh(hash,session_id,used) VALUES(?,?,0)", SecretHash(next), id); err != nil {
		return session, "", err
	}
	err = tx.Commit()
	return session, next, err
}
