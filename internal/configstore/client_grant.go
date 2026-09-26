package configstore

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/SamuelSupe/mcphub/v2/internal/config"
)

const GrantHeader = "MCPHub-Grant"

type GrantError string

func (e GrantError) Error() string { return string(e) }

const (
	ErrGrantRequired       GrantError = "client_grant_required"
	ErrGrantInvalid        GrantError = "client_grant_invalid"
	ErrGrantRevoked        GrantError = "client_grant_revoked"
	ErrGrantExpired        GrantError = "client_grant_expired"
	ErrGrantInsufficient   GrantError = "client_grant_insufficient"
	ErrGrantReconfirmation GrantError = "client_grant_reconfirmation_required"
	ErrGrantLimit          GrantError = "client_authorization_limit"
	ErrBrokerSessionEnded  GrantError = "client_broker_session_ended"
)

type GrantCapabilities struct {
	Tools         bool `json:"tools"`
	Prompts       bool `json:"prompts"`
	Resources     bool `json:"resources"`
	Subscriptions bool `json:"subscriptions"`
}

type GrantBinding struct {
	GrantID     string `json:"grant_id"`
	Revision    int64  `json:"grant_revision"`
	SessionID   string `json:"broker_session_id"`
	ClientID    string `json:"client_instance_id"`
	EndpointUID string `json:"endpoint_uid"`
}

type ClientGrant struct {
	GrantBinding
	Issuer             string                `json:"issuer"`
	Subject            string                `json:"subject"`
	Resource           string                `json:"resource"`
	ClientName         string                `json:"client_name"`
	EndpointID         string                `json:"endpoint_id"`
	AllowedScopes      []string              `json:"allowed_scopes"`
	AllowedTools       []string              `json:"allowed_tools"`
	ResourceRules      []config.ResourceRule `json:"resource_rules"`
	AllowWriteRequests bool                  `json:"allow_write_requests"`
	Capabilities       GrantCapabilities     `json:"capabilities"`
	Status             string                `json:"status"`
	CreatedAt          time.Time             `json:"created_at"`
	ExpiresAt          time.Time             `json:"expires_at"`
	RequestExpiresAt   time.Time             `json:"request_expires_at"`
	ConfirmedAt        time.Time             `json:"confirmed_at,omitempty"`
	ConfirmedBy        string                `json:"confirmed_by,omitempty"`
	PairingCode        string                `json:"pairing_code"`
	EndpointPolicy     string                `json:"-"`
	ToolPolicies       map[string]string     `json:"tool_policies"`
}

// The encrypted envelope includes the policy fingerprint, which is never
// accepted from a client or exposed as a credential.
type storedGrant struct {
	Grant          ClientGrant `json:"grant"`
	EndpointPolicy string      `json:"endpoint_policy"`
}

func SecretHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func (g ClientGrant) EffectiveScopes(scopes []string) []string {
	var effective []string
	for _, scope := range scopes {
		if slices.Contains(g.AllowedScopes, scope) {
			effective = append(effective, scope)
		}
	}
	slices.Sort(effective)
	return slices.Compact(effective)
}

func (g ClientGrant) AllowsTool(endpoint, tool, effect string, arguments json.RawMessage) bool {
	if endpoint != g.EndpointID || !g.Capabilities.Tools || !slices.Contains(g.AllowedTools, tool) || (effect != "read" && !g.AllowWriteRequests) {
		return false
	}
	_, err := config.CheckToolResources(g.ResourceRules, arguments)
	return err == nil
}

func (s *Store) grantData(g ClientGrant) ([]byte, error) {
	data, err := json.Marshal(storedGrant{Grant: g, EndpointPolicy: g.EndpointPolicy})
	if err != nil {
		return nil, err
	}
	return s.seal("client-grant:"+g.GrantID, data)
}

func (s *Store) scanGrant(row rowScanner) (ClientGrant, error) {
	var id, status string
	var revision int64
	var encrypted []byte
	if err := row.Scan(&id, &status, &revision, &encrypted); err != nil {
		return ClientGrant{}, err
	}
	plain, err := s.open("client-grant:"+id, encrypted)
	if err != nil {
		return ClientGrant{}, err
	}
	var data storedGrant
	if err := json.Unmarshal(plain, &data); err != nil {
		return ClientGrant{}, err
	}
	g := data.Grant
	g.Status, g.Revision, g.EndpointPolicy = status, revision, data.EndpointPolicy
	if (status == "active" || status == "confirmed") && !time.Now().Before(g.ExpiresAt) {
		g.Status = "expired"
	}
	if (status == "pending" || status == "confirmed") && !time.Now().Before(g.RequestExpiresAt) {
		g.Status = "expired"
	}
	return g, nil
}

func (s *Store) GetClientGrant(ctx context.Context, id, issuer, subject string) (ClientGrant, error) {
	g, err := s.scanGrant(s.db.QueryRowContext(ctx, "SELECT id,status,revision,data FROM client_grants WHERE id=? AND issuer=? AND subject=?", id, issuer, subject))
	if errors.Is(err, sql.ErrNoRows) {
		return ClientGrant{}, ErrGrantInvalid
	}
	return g, err
}

func (s *Store) ListClientGrants(ctx context.Context, issuer, subject string) ([]ClientGrant, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT id,status,revision,data FROM client_grants WHERE issuer=? AND subject=? ORDER BY CASE WHEN status='active' THEN 0 WHEN status IN ('pending','confirmed') THEN 1 WHEN status='reconfirmation_required' THEN 2 ELSE 3 END, created_at DESC LIMIT 256", issuer, subject)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	values := []ClientGrant{}
	for rows.Next() {
		g, err := s.scanGrant(rows)
		if err != nil {
			return nil, err
		}
		values = append(values, g)
	}
	return values, rows.Err()
}

func (s *Store) checkGrant(ctx context.Context, g ClientGrant) error {
	switch g.Status {
	case "active":
	case "expired":
		return ErrGrantExpired
	case "reconfirmation_required":
		return ErrGrantReconfirmation
	case "revoked":
		return ErrGrantRevoked
	default:
		return ErrGrantInvalid
	}
	var status string
	var expires int64
	if err := s.db.QueryRowContext(ctx, "SELECT status,expires_at FROM broker_sessions WHERE id=? AND issuer=? AND subject=? AND resource=?", g.SessionID, g.Issuer, g.Subject, g.Resource).Scan(&status, &expires); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrGrantInvalid
		}
		return err
	}
	if status != "active" {
		return ErrGrantRevoked
	}
	if time.Now().UnixMilli() >= expires {
		return ErrGrantExpired
	}
	uid, policy, err := s.ClientEndpointPolicy(ctx, g.EndpointID)
	if err != nil || uid != g.EndpointUID || policy != g.EndpointPolicy {
		return ErrGrantReconfirmation
	}
	return nil
}

func (s *Store) AuthenticateClientGrant(ctx context.Context, secret, issuer, subject, resource string) (ClientGrant, error) {
	if len(secret) < 32 || len(secret) > 256 {
		return ClientGrant{}, ErrGrantInvalid
	}
	g, err := s.scanGrant(s.db.QueryRowContext(ctx, "SELECT id,status,revision,data FROM client_grants WHERE credential_hash=? AND issuer=? AND subject=? AND resource=?", SecretHash(secret), issuer, subject, resource))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ClientGrant{}, ErrGrantInvalid
		}
		return ClientGrant{}, err
	}
	return g, s.checkGrant(ctx, g)
}

// Admission and revocation share a short mutex. It is released before upstream
// I/O: revoking a grant cancels admitted work without waiting for long calls.
func (s *Store) AdmitClientGrant(ctx context.Context, binding GrantBinding, issuer, subject string) (context.Context, func(), error) {
	s.grantMu.Lock()
	defer s.grantMu.Unlock()
	g, err := s.GetClientGrant(ctx, binding.GrantID, issuer, subject)
	if err == nil && g.GrantBinding != binding {
		err = ErrGrantReconfirmation
	}
	if err == nil {
		err = s.checkGrant(ctx, g)
	}
	if err != nil {
		return ctx, nil, err
	}
	if s.grantCalls == nil {
		s.grantCalls = make(map[string]map[string]context.CancelFunc)
	}
	if s.grantCalls[g.GrantID] == nil {
		s.grantCalls[g.GrantID] = make(map[string]context.CancelFunc)
	}
	if len(s.grantCalls[g.GrantID]) >= 128 {
		return ctx, nil, ErrGrantLimit
	}
	callID := rand.Text()
	callCtx, cancel := context.WithDeadline(ctx, g.ExpiresAt)
	s.grantCalls[g.GrantID][callID] = cancel
	return callCtx, func() {
		cancel()
		s.grantMu.Lock()
		delete(s.grantCalls[g.GrantID], callID)
		if len(s.grantCalls[g.GrantID]) == 0 {
			delete(s.grantCalls, g.GrantID)
		}
		s.grantMu.Unlock()
	}, nil
}

func (s *Store) cancelGrantLocked(id string) {
	for _, cancel := range s.grantCalls[id] {
		cancel()
	}
}

func grantFailure(status string) error {
	switch status {
	case "expired":
		return ErrGrantExpired
	case "revoked", "denied":
		return ErrGrantRevoked
	default:
		return ErrGrantInvalid
	}
}

func validateGrantOwner(g ClientGrant) error {
	if g.Issuer == "" || g.Subject == "" || g.Resource == "" || g.EndpointUID == "" || g.EndpointPolicy == "" || len(g.ClientID) < 16 || len(g.ClientID) > 128 || len(g.ClientName) == 0 || len(g.ClientName) > 128 {
		return ErrGrantInvalid
	}
	if strings.ContainsAny(g.ClientID+g.ClientName, "\r\n\t\x00") {
		return ErrGrantInvalid
	}
	if len(g.AllowedScopes) > 64 || len(g.AllowedTools) > 256 || len(g.ResourceRules) > 32 || (!g.Capabilities.Tools && len(g.AllowedTools) > 0) || (g.Capabilities.Subscriptions && !g.Capabilities.Resources) {
		return ErrGrantInsufficient
	}
	if (g.Capabilities.Prompts || g.Capabilities.Resources || g.Capabilities.Subscriptions) && len(g.ResourceRules) > 0 {
		return errors.New("tool argument restrictions cannot authorize prompts or resource URIs; use a separate client grant")
	}
	if len(g.ResourceRules) > 0 {
		if err := config.ValidateToolRules("client grant", []config.ToolRule{{Match: "*", ResourceRules: g.ResourceRules}}); err != nil {
			return fmt.Errorf("%w: %v", ErrGrantInsufficient, err)
		}
	}
	return nil
}

func (s *Store) grantEvent(ctx context.Context, tx *transaction, g ClientGrant, action string) error {
	detail := ApprovalDetail{Reason: fmt.Sprintf("client=%s session=%s endpoint=%s revision=%d", g.ClientID, g.SessionID, g.EndpointUID, g.Revision)}
	return s.enqueueAuthorizationEvent(ctx, tx, g, action, detail)
}
