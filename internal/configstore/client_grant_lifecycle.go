package configstore

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"time"

	"github.com/SamuelSupe/mcphub/v2/internal/config"
	"github.com/SamuelSupe/mcphub/v2/internal/httptool"
)

func (s *Store) CreateClientGrant(ctx context.Context, g ClientGrant, sessionProof string, ttl, maximum time.Duration) (ClientGrant, string, string, error) {
	if err := validateGrantOwner(g); err != nil {
		return ClientGrant{}, "", "", err
	}
	if ttl <= 0 || ttl > maximum {
		return ClientGrant{}, "", "", ErrGrantInsufficient
	}
	s.grantMu.Lock()
	defer s.grantMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ClientGrant{}, "", "", err
	}
	defer tx.Rollback()
	now := time.Now().UTC()
	var pending, active int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM client_grants WHERE issuer=? AND subject=? AND status IN ('pending','confirmed') AND request_expires_at>?", g.Issuer, g.Subject, now.UnixMilli()).Scan(&pending); err != nil {
		return ClientGrant{}, "", "", err
	}
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM client_grants WHERE issuer=? AND subject=? AND status='active' AND expires_at>? AND NOT (session_id=? AND client_id=?)", g.Issuer, g.Subject, now.UnixMilli(), g.SessionID, g.ClientID).Scan(&active); err != nil {
		return ClientGrant{}, "", "", err
	}
	if pending >= 16 || active >= 128 {
		return ClientGrant{}, "", "", ErrGrantLimit
	}
	var sessionExpiry int64
	if g.SessionID == "" {
		g.SessionID, sessionProof = "bs_"+rand.Text(), rand.Text()+rand.Text()
		sessionExpiry = now.Add(maximum).UnixMilli()
		_, err = tx.ExecContext(ctx, "INSERT INTO broker_sessions(id,issuer,subject,resource,secret_hash,status,created_at,expires_at) VALUES(?,?,?,?,?,'active',?,?)", g.SessionID, g.Issuer, g.Subject, g.Resource, SecretHash(sessionProof), now.UnixMilli(), sessionExpiry)
	} else {
		err = tx.QueryRowContext(ctx, "SELECT expires_at FROM broker_sessions WHERE id=? AND issuer=? AND subject=? AND resource=? AND secret_hash=? AND status='active' AND expires_at>?", g.SessionID, g.Issuer, g.Subject, g.Resource, SecretHash(sessionProof), now.UnixMilli()).Scan(&sessionExpiry)
		if errors.Is(err, sql.ErrNoRows) {
			return ClientGrant{}, "", "", ErrBrokerSessionEnded
		}
	}
	if err != nil {
		return ClientGrant{}, "", "", err
	}
	g.GrantID, g.Revision, g.Status = "gr_"+rand.Text(), 1, "pending"
	g.CreatedAt, g.ExpiresAt, g.RequestExpiresAt = now, now.Add(ttl), now.Add(5*time.Minute)
	if g.ExpiresAt.UnixMilli() > sessionExpiry {
		g.ExpiresAt = time.UnixMilli(sessionExpiry).UTC()
	}
	g.PairingCode = rand.Text()[:8]
	exchange := rand.Text() + rand.Text()
	data, err := s.grantData(g)
	if err != nil {
		return ClientGrant{}, "", "", err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO client_grants(id,issuer,subject,resource,session_id,client_id,endpoint_id,status,revision,data,exchange_hash,created_at,expires_at,request_expires_at) VALUES(?,?,?,?,?,?,?,'pending',1,?,?,?,?,?)`, g.GrantID, g.Issuer, g.Subject, g.Resource, g.SessionID, g.ClientID, g.EndpointID, data, SecretHash(exchange), now.UnixMilli(), g.ExpiresAt.UnixMilli(), g.RequestExpiresAt.UnixMilli())
	if err == nil {
		err = s.grantEvent(ctx, tx, g, "client_authorization_requested")
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		return ClientGrant{}, "", "", err
	}
	return g, exchange, sessionProof, nil
}

// Only the browser controller calls this after authenticating the owner and
// checking Origin, CSRF and current token scopes against the frozen request.
func (s *Store) DecideClientGrant(ctx context.Context, id, issuer, subject string, confirm bool) error {
	s.grantMu.Lock()
	defer s.grantMu.Unlock()
	g, err := s.GetClientGrant(ctx, id, issuer, subject)
	if err != nil {
		return err
	}
	if g.Status != "pending" {
		return grantFailure(g.Status)
	}
	uid, policy, err := s.ClientEndpointPolicy(ctx, g.EndpointID)
	if err != nil || uid != g.EndpointUID || policy != g.EndpointPolicy {
		return ErrGrantReconfirmation
	}
	g.Status = "denied"
	if confirm {
		g.Status, g.ConfirmedBy, g.ConfirmedAt = "confirmed", subject, time.Now().UTC()
	}
	data, err := s.grantData(g)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, "UPDATE client_grants SET status=?,data=? WHERE id=? AND status='pending' AND request_expires_at>? AND expires_at>? AND session_id IN (SELECT id FROM broker_sessions WHERE status='active' AND expires_at>?)", g.Status, data, id, time.Now().UnixMilli(), time.Now().UnixMilli(), time.Now().UnixMilli())
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return ErrGrantExpired
	}
	if err := s.grantEvent(ctx, tx, g, "client_authorization_"+g.Status); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ExchangeClientGrant(ctx context.Context, id, issuer, subject, exchange string) (ClientGrant, string, error) {
	if len(exchange) < 32 || len(exchange) > 256 {
		return ClientGrant{}, "", ErrGrantInvalid
	}
	s.grantMu.Lock()
	defer s.grantMu.Unlock()
	g, err := s.GetClientGrant(ctx, id, issuer, subject)
	if err != nil {
		return ClientGrant{}, "", err
	}
	if g.Status != "confirmed" || !time.Now().Before(g.RequestExpiresAt) {
		return ClientGrant{}, "", grantFailure(g.Status)
	}
	uid, policy, err := s.ClientEndpointPolicy(ctx, g.EndpointID)
	if err != nil || uid != g.EndpointUID || policy != g.EndpointPolicy {
		return ClientGrant{}, "", ErrGrantReconfirmation
	}
	secret := rand.Text() + rand.Text()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ClientGrant{}, "", err
	}
	defer tx.Rollback()
	var active int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM client_grants WHERE issuer=? AND subject=? AND status='active' AND expires_at>? AND NOT (session_id=? AND client_id=?)", issuer, subject, time.Now().UnixMilli(), g.SessionID, g.ClientID).Scan(&active); err != nil {
		return ClientGrant{}, "", err
	}
	if active >= 128 {
		return ClientGrant{}, "", ErrGrantLimit
	}
	result, err := tx.ExecContext(ctx, "UPDATE client_grants SET credential_hash=?,exchange_hash=NULL,status='active' WHERE id=? AND status='confirmed' AND exchange_hash=? AND request_expires_at>? AND expires_at>? AND session_id IN (SELECT id FROM broker_sessions WHERE status='active' AND expires_at>?)", SecretHash(secret), id, SecretHash(exchange), time.Now().UnixMilli(), time.Now().UnixMilli(), time.Now().UnixMilli())
	if err != nil {
		return ClientGrant{}, "", err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return ClientGrant{}, "", ErrGrantInvalid
	}
	rows, err := tx.QueryContext(ctx, "SELECT id,status,revision,data FROM client_grants WHERE session_id=? AND client_id=? AND id<>? AND status='active'", g.SessionID, g.ClientID, g.GrantID)
	if err != nil {
		return ClientGrant{}, "", err
	}
	var old []ClientGrant
	for rows.Next() {
		value, scanErr := s.scanGrant(rows)
		if scanErr != nil {
			rows.Close()
			return ClientGrant{}, "", scanErr
		}
		old = append(old, value)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return ClientGrant{}, "", err
	}
	for _, previous := range old {
		if err := s.revokeGrantTx(ctx, tx, previous, "client_authorization_replaced"); err != nil {
			return ClientGrant{}, "", err
		}
	}
	g.Status = "active"
	if err := s.grantEvent(ctx, tx, g, "client_authorization_activated"); err != nil {
		return ClientGrant{}, "", err
	}
	if err := tx.Commit(); err != nil {
		return ClientGrant{}, "", err
	}
	for _, previous := range old {
		s.cancelGrantLocked(previous.GrantID)
	}
	return g, secret, nil
}

func (s *Store) revokeGrantTx(ctx context.Context, tx *transaction, g ClientGrant, action string) error {
	_, err := tx.ExecContext(ctx, "UPDATE client_grants SET status='revoked',revision=revision+1,exchange_hash=NULL WHERE id=?", g.GrantID)
	if err != nil {
		return err
	}
	g.Status, g.Revision = "revoked", g.Revision+1
	return s.grantEvent(ctx, tx, g, action)
}

func (s *Store) RevokeClientGrant(ctx context.Context, id, issuer, subject string) error {
	s.grantMu.Lock()
	defer s.grantMu.Unlock()
	g, err := s.GetClientGrant(ctx, id, issuer, subject)
	if err != nil {
		return err
	}
	if g.Status == "revoked" {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := s.revokeGrantTx(ctx, tx, g, "client_authorization_revoked"); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.cancelGrantLocked(id)
	return nil
}

func (s *Store) RevokeBrokerSession(ctx context.Context, id, issuer, subject, proof string, browser bool) error {
	s.grantMu.Lock()
	defer s.grantMu.Unlock()
	var hash, status string
	if err := s.db.QueryRowContext(ctx, "SELECT secret_hash,status FROM broker_sessions WHERE id=? AND issuer=? AND subject=?", id, issuer, subject).Scan(&hash, &status); err != nil {
		return ErrGrantInvalid
	}
	if !browser && (len(proof) < 32 || hash != SecretHash(proof)) {
		return ErrGrantInvalid
	}
	if status != "active" {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, "SELECT id,status,revision,data FROM client_grants WHERE session_id=? AND status NOT IN ('revoked','denied')", id)
	if err != nil {
		return err
	}
	var grants []ClientGrant
	for rows.Next() {
		g, scanErr := s.scanGrant(rows)
		if scanErr != nil {
			rows.Close()
			return scanErr
		}
		grants = append(grants, g)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, g := range grants {
		if err := s.revokeGrantTx(ctx, tx, g, "broker_session_ended"); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, "UPDATE broker_sessions SET status='revoked' WHERE id=?", id); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	for _, g := range grants {
		s.cancelGrantLocked(g.GrantID)
	}
	return nil
}

func clientBackendPolicy(cfg config.BackendConfig) string {
	return policyHash(struct {
		URL       string
		Scopes    []string
		Rules     any
		Headers   any
		OAuth     any
		Published []string
	}{cfg.URL, cfg.RequiredScopes, cfg.ToolRules, cfg.Headers, cfg.OAuth, cfg.PublishedTools})
}
func clientGroupPolicy(cfg httptool.GroupConfig) string {
	return policyHash(struct {
		URL     string
		Scopes  []string
		Rules   any
		Headers any
		OAuth   any
	}{cfg.BaseURL, cfg.RequiredScopes, cfg.ToolRules, cfg.Headers, cfg.OAuth})
}
func (s *Store) ClientEndpointPolicy(ctx context.Context, id string) (string, string, error) {
	backend, err := s.Get(ctx, id)
	if err == nil {
		if !backend.Enabled {
			return "", "", ErrGrantReconfirmation
		}
		return backend.Config.EndpointUID, clientBackendPolicy(backend.Config), nil
	}
	if !errors.Is(err, ErrNotFound) && !errors.Is(err, sql.ErrNoRows) {
		return "", "", err
	}
	group, err := s.GetToolGroup(ctx, id)
	if err != nil || !group.Config.Enabled {
		return "", "", ErrGrantReconfirmation
	}
	return group.Config.EndpointUID, clientGroupPolicy(group.Config), nil
}
