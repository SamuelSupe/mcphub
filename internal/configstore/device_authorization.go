package configstore

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base32"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

const DeviceGrantType = "urn:ietf:params:oauth:grant-type:device_code"

type DeviceError string

func (e DeviceError) Error() string { return string(e) }

const (
	DevicePending  DeviceError = "authorization_pending"
	DeviceSlowDown DeviceError = "slow_down"
	DeviceDenied   DeviceError = "access_denied"
	DeviceExpired  DeviceError = "expired_token"
	DeviceInvalid  DeviceError = "invalid_grant"
	DeviceLimited  DeviceError = "temporarily_unavailable"
)

// Private fields are encrypted at rest and never serialized by the browser API.
type DeviceAuthorization struct {
	ID                  string      `json:"request_id"`
	ClientID            string      `json:"oauth_client_id"`
	Resource            string      `json:"resource"`
	Scopes              []string    `json:"scopes"`
	Request             ClientGrant `json:"request"`
	SessionProof        string      `json:"session_proof,omitempty"`
	TTLSeconds          int64       `json:"ttl_seconds"`
	UserCode            string      `json:"user_code"`
	Status              string      `json:"status"`
	CreatedAt           time.Time   `json:"created_at"`
	ExpiresAt           time.Time   `json:"expires_at"`
	ReplacesSession     string      `json:"replaces_broker_session_id,omitempty"`
	EndpointPolicy      string      `json:"endpoint_policy,omitempty"`
	IdentityVersion     string      `json:"identity_version,omitempty"`
	ConnectionsRevision int64       `json:"connections_revision"`
	Exchange            string      `json:"exchange,omitempty"`
	Proof               string      `json:"proof,omitempty"`
}

func normalizeDeviceCode(code string) string {
	return strings.ToUpper(strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(code), "-", ""), " ", ""))
}
func (s *Store) deviceData(d DeviceAuthorization) ([]byte, error) {
	data, err := json.Marshal(d)
	if err != nil {
		return nil, err
	}
	return s.seal("device:"+d.ID, data)
}
func (s *Store) readDevice(ctx context.Context, db identityReader, clause string, args ...any) (DeviceAuthorization, error) {
	var d DeviceAuthorization
	var data []byte
	var status string
	err := db.QueryRowContext(ctx, "SELECT id,status,data FROM device_authorizations WHERE "+clause, args...).Scan(&d.ID, &status, &data)
	if errors.Is(err, sql.ErrNoRows) {
		return d, DeviceInvalid
	}
	if err != nil {
		return d, err
	}
	plain, err := s.open("device:"+d.ID, data)
	if err == nil {
		err = json.Unmarshal(plain, &d)
	}
	d.Status = status
	d.Request.EndpointPolicy = d.EndpointPolicy
	return d, err
}
func (s *Store) CreateDeviceAuthorization(ctx context.Context, d DeviceAuthorization, address string) (DeviceAuthorization, string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return d, "", err
	}
	defer tx.Rollback()
	now := time.Now().UTC()
	var source, client, global int
	err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM device_authorizations WHERE address_hash=? AND created_at>?`, SecretHash(address), now.Add(-time.Minute).UnixMilli()).Scan(&source)
	if err == nil {
		err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM device_authorizations WHERE client_id=? AND created_at>?", d.ClientID, now.Add(-time.Minute).UnixMilli()).Scan(&client)
	}
	if err == nil {
		err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM device_authorizations WHERE expires_at>? AND status IN ('pending','approved')", now.UnixMilli()).Scan(&global)
	}
	if err != nil {
		return d, "", err
	}
	if source >= 10 || client >= 120 || global >= 1024 {
		return d, "", DeviceLimited
	}
	d.ID, d.Status, d.CreatedAt, d.ExpiresAt = "pr_"+rand.Text(), "pending", now, now.Add(5*time.Minute)
	code := make([]byte, 5)
	if _, err = rand.Read(code); err != nil {
		return d, "", err
	}
	text := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(code)
	d.UserCode = text[:4] + "-" + text[4:]
	deviceCode := rand.Text() + rand.Text()
	data, err := s.deviceData(d)
	if err != nil {
		return d, "", err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO device_authorizations(id,device_hash,user_hash,client_id,address_hash,status,created_at,expires_at,interval_seconds,next_poll,data) VALUES(?,?,?,?,?,'pending',?,?,5,?,?)`, d.ID, SecretHash(deviceCode), SecretHash(normalizeDeviceCode(d.UserCode)), d.ClientID, SecretHash(address), now.UnixMilli(), d.ExpiresAt.UnixMilli(), now.Add(5*time.Second).UnixMilli(), data)
	if err == nil {
		err = tx.Commit()
	}
	return d, deviceCode, err
}

// Rate limiting includes incorrect codes, before any private catalog is read.
func (s *Store) DeviceByUserCode(ctx context.Context, code, address string) (DeviceAuthorization, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return DeviceAuthorization{}, err
	}
	defer tx.Rollback()
	now := time.Now().UnixMilli()
	hash := SecretHash(address)
	var count int64
	// Increment in the database so concurrent processes cannot overwrite each other's attempts.
	err = tx.QueryRowContext(ctx, `INSERT INTO device_code_attempts(address_hash,window_at,attempts) VALUES(?,?,1)
ON CONFLICT(address_hash) DO UPDATE SET
window_at=CASE WHEN device_code_attempts.window_at<=? THEN excluded.window_at ELSE device_code_attempts.window_at END,
attempts=CASE WHEN device_code_attempts.window_at<=? THEN 1 ELSE device_code_attempts.attempts+1 END
RETURNING attempts`, hash, now, now-60000, now-60000).Scan(&count)
	if err != nil {
		return DeviceAuthorization{}, err
	}
	if err = tx.Commit(); err != nil {
		return DeviceAuthorization{}, err
	}
	if count > 20 {
		return DeviceAuthorization{}, DeviceLimited
	}
	if len(normalizeDeviceCode(code)) != 8 {
		return DeviceAuthorization{}, DeviceInvalid
	}
	d, err := s.readDevice(ctx, s.db, "user_hash=?", SecretHash(normalizeDeviceCode(code)))
	if err == nil && !time.Now().Before(d.ExpiresAt) {
		err = DeviceExpired
	}
	return d, err
}

func (s *Store) PollDeviceAuthorization(ctx context.Context, code, client, resource string) (DeviceAuthorization, error) {
	if len(code) < 32 || len(code) > 256 {
		return DeviceAuthorization{}, DeviceInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return DeviceAuthorization{}, err
	}
	defer tx.Rollback()
	d, err := s.readDevice(ctx, tx, "device_hash=? AND client_id=?", SecretHash(code), client)
	if err != nil {
		return d, err
	}
	if resource != "" && resource != d.Resource {
		return d, DeviceInvalid
	}
	if !time.Now().Before(d.ExpiresAt) {
		return d, DeviceExpired
	}
	switch d.Status {
	case "denied", "canceled":
		return d, DeviceDenied
	case "redeemed":
		return d, DeviceInvalid
	case "pending", "approved":
	default:
		return d, DeviceInvalid
	}
	var interval, next int64
	if err = tx.QueryRowContext(ctx, "SELECT interval_seconds,next_poll FROM device_authorizations WHERE id=?", d.ID).Scan(&interval, &next); err != nil {
		return d, err
	}
	now := time.Now().UnixMilli()
	result := DevicePending
	if now < next {
		interval += 5
		result = DeviceSlowDown
	}
	_, err = tx.ExecContext(ctx, "UPDATE device_authorizations SET interval_seconds=?,next_poll=? WHERE id=?", interval, now+interval*1000, d.ID)
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		return d, err
	}
	if result == DeviceSlowDown || d.Status == "pending" {
		return d, result
	}
	return d, nil
}

func (s *Store) deviceIdentity(ctx context.Context, db identityReader, d DeviceAuthorization) (EffectiveIdentity, error) {
	p, err := s.effectiveIdentityFrom(ctx, db, d.Request.Subject)
	if err != nil || p.Version != d.IdentityVersion {
		return p, DeviceDenied
	}
	if p.Provider != "mcphub:local" {
		var data []byte
		err = db.QueryRowContext(ctx, "SELECT value FROM metadata WHERE key=?", enterpriseKey).Scan(&data)
		if err == nil {
			data, err = s.open(enterpriseKey, data)
		}
		var c struct {
			Revision int64 `json:"revision"`
		}
		if err == nil {
			err = json.Unmarshal(data, &c)
		}
		if err != nil || c.Revision != d.ConnectionsRevision {
			return p, DeviceDenied
		}
	}
	return p, nil
}
func (s *Store) ConfirmDeviceAuthorization(ctx context.Context, id string, g ClientGrant, identity EffectiveIdentity, revision int64, ttl, maximum time.Duration) (ClientGrant, error) {
	if err := validateGrantOwner(g); err != nil {
		return g, err
	}
	if ttl <= 0 || ttl > maximum {
		return g, ErrGrantInsufficient
	}
	s.identityMu.Lock()
	defer s.identityMu.Unlock()
	s.grantMu.Lock()
	defer s.grantMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return g, err
	}
	defer tx.Rollback()
	d, err := s.readDevice(ctx, tx, "id=?", id)
	if err != nil {
		return g, err
	}
	if d.Status != "pending" {
		return g, DeviceInvalid
	}
	if !time.Now().Before(d.ExpiresAt) {
		return g, DeviceExpired
	}
	// The browser can narrow a request but cannot replace its instance or owner binding.
	if g.ClientID != d.Request.ClientID || g.ClientName != d.Request.ClientName || g.Resource != d.Resource {
		return g, DeviceInvalid
	}
	d.Request, d.IdentityVersion, d.ConnectionsRevision = g, identity.Version, revision
	if _, err = s.deviceIdentity(ctx, tx, d); err != nil {
		return g, err
	}
	uid, policy, err := s.clientEndpointPolicyFrom(ctx, tx, g.EndpointID)
	if err != nil || uid != g.EndpointUID || policy != g.EndpointPolicy {
		return g, ErrGrantReconfirmation
	}
	if g.SessionID != "" {
		var status string
		var expires int64
		err := tx.QueryRowContext(ctx, "SELECT status,expires_at FROM broker_sessions WHERE id=? AND issuer=? AND subject=? AND resource=? AND secret_hash=?", g.SessionID, g.Issuer, g.Subject, g.Resource, SecretHash(d.SessionProof)).Scan(&status, &expires)
		if err != nil {
			return g, DeviceDenied
		}
		if status != "active" || expires <= time.Now().UnixMilli() {
			d.ReplacesSession = g.SessionID
			g.SessionID = ""
		}
	}
	g, exchange, proof, err := s.createClientGrantTx(ctx, tx, g, d.SessionProof, ttl, maximum)
	if err != nil {
		return g, err
	}
	g.Status, g.ConfirmedBy, g.ConfirmedAt, g.PairingCode = "confirmed", g.Subject, time.Now().UTC(), d.UserCode
	g.RequestExpiresAt = d.ExpiresAt
	data, err := s.grantData(g)
	if err != nil {
		return g, err
	}
	_, err = tx.ExecContext(ctx, "UPDATE client_grants SET status='confirmed',data=?,request_expires_at=? WHERE id=?", data, g.RequestExpiresAt.UnixMilli(), g.GrantID)
	if err == nil {
		err = s.grantEvent(ctx, tx, g, "client_authorization_confirmed")
	}
	if err != nil {
		return g, err
	}
	d.Request, d.Exchange, d.Proof, d.SessionProof = g, exchange, proof, ""
	d.EndpointPolicy = g.EndpointPolicy
	d.Status = "approved"
	data, err = s.deviceData(d)
	if err != nil {
		return g, err
	}
	res, err := tx.ExecContext(ctx, "UPDATE device_authorizations SET status='approved',data=?,grant_id=? WHERE id=? AND status='pending' AND expires_at>?", data, g.GrantID, id, time.Now().UnixMilli())
	if err != nil {
		return g, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return g, DeviceInvalid
	}
	return g, tx.Commit()
}
func (s *Store) DenyDeviceAuthorization(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, "UPDATE device_authorizations SET status='denied' WHERE id=? AND status='pending' AND expires_at>?", id, time.Now().UnixMilli())
	if err == nil {
		if n, _ := res.RowsAffected(); n != 1 {
			return DeviceInvalid
		}
	}
	return err
}
