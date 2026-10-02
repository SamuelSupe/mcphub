package configstore

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/SamuelSupe/mcphub/v2/internal/config"
)

// Redemption and SSO session creation commit once. A lost response requires a new pairing.
func (s *Store) RedeemDeviceAuthorization(ctx context.Context, id, client string) (DeviceAuthorization, SSOSession, string, error) {
	var session SSOSession
	s.identityMu.Lock()
	defer s.identityMu.Unlock()
	s.grantMu.Lock()
	defer s.grantMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return DeviceAuthorization{}, session, "", err
	}
	defer tx.Rollback()
	d, err := s.readDevice(ctx, tx, "id=? AND client_id=?", id, client)
	if err != nil {
		return d, session, "", err
	}
	if d.Status != "approved" {
		return d, session, "", DeviceInvalid
	}
	if !time.Now().Before(d.ExpiresAt) {
		return d, session, "", DeviceExpired
	}
	p, err := s.deviceIdentity(ctx, tx, d)
	if err != nil {
		return d, session, "", err
	}
	var status string
	now := time.Now().UnixMilli()
	err = tx.QueryRowContext(ctx, "SELECT status FROM client_grants WHERE id=? AND expires_at>? AND request_expires_at>? AND session_id IN (SELECT id FROM broker_sessions WHERE status='active' AND expires_at>?)", d.Request.GrantID, now, now, now).Scan(&status)
	if err != nil || status != "confirmed" {
		return d, session, "", DeviceDenied
	}
	uid, policy, err := s.clientEndpointPolicyFrom(ctx, tx, d.Request.EndpointID)
	if err != nil || uid != d.Request.EndpointUID || policy != d.Request.EndpointPolicy {
		return d, session, "", DeviceDenied
	}
	ttl := 10 * time.Minute
	offline := slices.Contains(d.Scopes, "offline_access")
	if offline {
		ttl = 8 * time.Hour
	}
	scopes := []string{}
	for _, scope := range d.Scopes {
		if slices.Contains(p.Permissions.EffectiveScopes(config.AdminConfig{}), scope) {
			scopes = append(scopes, scope)
		}
	}
	scopes = append(scopes, d.Request.AllowedScopes...)
	slices.Sort(scopes)
	scopes = slices.Compact(scopes)
	if offline {
		scopes = append(scopes, "offline_access")
	}
	session, refresh, err := s.createSSOSessionTx(ctx, tx, SSOSession{UserID: p.ID, CredentialVersion: p.CredentialVersion, ClientID: client, Resource: d.Resource, Scopes: scopes, ExpiresAt: time.Now().Add(ttl)}, offline)
	if err != nil {
		return d, session, "", err
	}
	stored := d
	stored.Exchange, stored.Proof, stored.SessionProof = "", "", ""
	stored.Status = "redeemed"
	data, err := s.deviceData(stored)
	if err != nil {
		return d, session, "", err
	}
	res, err := tx.ExecContext(ctx, "UPDATE device_authorizations SET status='redeemed',data=?,sso_session_id=? WHERE id=? AND status='approved' AND expires_at>?", data, session.ID, id, time.Now().UnixMilli())
	if err != nil {
		return d, session, "", err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return d, session, "", DeviceInvalid
	}
	err = tx.Commit()
	return d, session, refresh, err
}

func (s *Store) validateDeviceExchange(ctx context.Context, tx *transaction, id string) error {
	d, err := s.readDevice(ctx, tx, "grant_id=?", id)
	if errors.Is(err, DeviceInvalid) {
		return nil
	}
	if err != nil {
		return err
	}
	if d.Status != "redeemed" {
		return DeviceInvalid
	}
	_, err = s.deviceIdentity(ctx, tx, d)
	return err
}

func (s *Store) maintainDevices(ctx context.Context, tx *transaction, cutoff int64) error {
	// Only failed/lost device deliveries are cleaned. Active grants retain their token family.
	_, err := tx.ExecContext(ctx, `DELETE FROM sso_refresh WHERE session_id IN (SELECT sso_session_id FROM device_authorizations WHERE expires_at<? AND status='redeemed' AND grant_id NOT IN (SELECT id FROM client_grants WHERE status='active'))`, time.Now().UnixMilli())
	if err == nil {
		_, err = tx.ExecContext(ctx, `DELETE FROM sso_sessions WHERE id IN (SELECT sso_session_id FROM device_authorizations WHERE expires_at<? AND status='redeemed' AND grant_id NOT IN (SELECT id FROM client_grants WHERE status='active'))`, time.Now().UnixMilli())
	}
	// Clear encrypted exchange material as soon as an abandoned request expires.
	rows, e := tx.QueryContext(ctx, "SELECT id,status,data FROM device_authorizations WHERE expires_at<=? AND status IN ('pending','approved','denied','canceled') LIMIT 256", time.Now().UnixMilli())
	if e != nil {
		return e
	}
	var expired []DeviceAuthorization
	for rows.Next() {
		var id, status string
		var data []byte
		if e = rows.Scan(&id, &status, &data); e != nil {
			break
		}
		plain, openErr := s.open("device:"+id, data)
		if openErr != nil {
			e = openErr
			break
		}
		var d DeviceAuthorization
		if e = json.Unmarshal(plain, &d); e != nil {
			break
		}
		expired = append(expired, d)
	}
	if e == nil {
		e = rows.Err()
	}
	rows.Close()
	if e != nil {
		return e
	}
	for _, d := range expired {
		d.Exchange, d.Proof, d.SessionProof = "", "", ""
		d.Status = "expired"
		data, e := s.deviceData(d)
		if e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, "UPDATE device_authorizations SET status='expired',data=? WHERE id=?", data, d.ID); e != nil {
			return e
		}
	}
	if err == nil {
		_, err = tx.ExecContext(ctx, "DELETE FROM device_authorizations WHERE expires_at<?", cutoff)
	}
	if err == nil {
		_, err = tx.ExecContext(ctx, "DELETE FROM device_code_attempts WHERE window_at<?", time.Now().Add(-time.Hour).UnixMilli())
	}
	return err
}

// A private device capability can cancel only its own request and undelivered token family.
func (s *Store) CancelDeviceAuthorization(ctx context.Context, code, client string) error {
	s.grantMu.Lock()
	defer s.grantMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	d, err := s.readDevice(ctx, tx, "device_hash=? AND client_id=?", SecretHash(code), client)
	if err != nil {
		return err
	}
	if d.Status == "completed" {
		return DeviceInvalid
	}
	if d.Request.GrantID != "" {
		var status string
		err = tx.QueryRowContext(ctx, "SELECT status FROM client_grants WHERE id=?", d.Request.GrantID).Scan(&status)
		if err != nil {
			return err
		}
		if status == "active" {
			return DeviceInvalid
		}
		if status == "confirmed" {
			if err = s.revokeGrantTx(ctx, tx, d.Request, "client_authorization_canceled"); err != nil {
				return err
			}
		}
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM sso_refresh WHERE session_id IN (SELECT sso_session_id FROM device_authorizations WHERE id=?)", d.ID); err == nil {
		_, err = tx.ExecContext(ctx, "DELETE FROM sso_sessions WHERE id IN (SELECT sso_session_id FROM device_authorizations WHERE id=?)", d.ID)
	}
	d.Exchange, d.Proof, d.SessionProof = "", "", ""
	data, e := s.deviceData(d)
	if e != nil {
		return e
	}
	if err == nil {
		_, err = tx.ExecContext(ctx, "UPDATE device_authorizations SET status='canceled',data=? WHERE id=?", data, d.ID)
	}
	if err == nil {
		err = tx.Commit()
	}
	return err
}
