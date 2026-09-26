package sso

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/SamuelSupe/mcphub/v2/internal/configstore"
	jose "github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	mcpauth "github.com/modelcontextprotocol/go-sdk/auth"
)

func (s *Server) grantedScopes(p configstore.EffectiveIdentity, requested []string) []string {
	available := p.Permissions.EffectiveScopes(s.cfg.Admin)
	available = append(available, "openid", "offline_access")
	result := []string{}
	for _, scope := range requested {
		if slices.Contains(available, scope) && !slices.Contains(result, scope) {
			result = append(result, scope)
		}
	}
	return result
}

func (s *Server) token(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if r.ParseForm() != nil {
		oauthError(w, 400, "invalid_request")
		return
	}
	for _, values := range r.PostForm {
		if len(values) != 1 {
			oauthError(w, 400, "invalid_request")
			return
		}
	}
	if len(r.URL.Query()) != 0 || r.Header.Get("Authorization") != "" || r.PostForm.Get("client_secret") != "" {
		oauthError(w, 400, "invalid_request")
		return
	}
	form := r.PostForm
	var session configstore.SSOSession
	var refresh string
	var code authorizationCode
	var identity configstore.EffectiveIdentity
	var err error
	switch form.Get("grant_type") {
	case "authorization_code":
		hash := configstore.SecretHash(form.Get("code"))
		s.mu.Lock()
		p, ok := s.codes[hash]
		delete(s.codes, hash)
		s.mu.Unlock()
		verifier := form.Get("code_verifier")
		digest := sha256.Sum256([]byte(verifier))
		challenge := base64.RawURLEncoding.EncodeToString(digest[:])
		if !ok || !time.Now().Before(p.Expires) || p.ClientID != form.Get("client_id") || p.Redirect != form.Get("redirect_uri") || p.Resource != form.Get("resource") || len(verifier) < 43 || len(verifier) > 128 || subtle.ConstantTimeCompare([]byte(p.Challenge), []byte(challenge)) != 1 {
			oauthError(w, 400, "invalid_grant")
			return
		}
		identity, err = s.tokenIdentity(r.Context(), p.UserID)
		if err != nil {
			tokenStoreError(w, err)
			return
		}
		p.Scopes = s.grantedScopes(identity, p.Scopes)
		code = p
		ttl := 10 * time.Minute
		offline := slices.Contains(p.Scopes, "offline_access")
		if offline {
			ttl = 8 * time.Hour
		}
		session, refresh, err = s.store.CreateSSOSession(r.Context(), configstore.SSOSession{UserID: p.UserID, ClientID: p.ClientID, Resource: p.Resource, Scopes: p.Scopes, ExpiresAt: time.Now().Add(ttl)}, offline)
	case "refresh_token":
		// A rotated credential is usable only by its original client and resource.
		session, err = s.store.SSORefreshSession(r.Context(), form.Get("refresh_token"))
		if err != nil {
			tokenStoreError(w, err)
			return
		}
		if session.ClientID != form.Get("client_id") || session.Resource != form.Get("resource") {
			oauthError(w, 400, "invalid_grant")
			return
		}
		identity, err = s.tokenIdentity(r.Context(), session.UserID)
		if err != nil {
			tokenStoreError(w, err)
			return
		}
		if form.Has("scope") && slices.ContainsFunc(strings.Fields(form.Get("scope")), func(scope string) bool { return !slices.Contains(session.Scopes, scope) }) {
			oauthError(w, 400, "invalid_scope")
			return
		}
		session, refresh, err = s.store.RotateSSORefresh(r.Context(), form.Get("refresh_token"), form.Get("client_id"), form.Get("resource"))
		if form.Has("scope") {
			session.Scopes = strings.Fields(form.Get("scope"))
		}
	default:
		oauthError(w, 400, "unsupported_grant_type")
		return
	}
	if err != nil {
		tokenStoreError(w, err)
		return
	}
	scopes := s.grantedScopes(identity, session.Scopes)
	now := time.Now()
	expires := now.Add(10 * time.Minute)
	if session.ExpiresAt.Before(expires) {
		expires = session.ExpiresAt
	}
	access, err := s.sign(session.UserID, session.Resource, expires, "at+jwt", map[string]any{"sid": session.ID, "scope": strings.Join(scopes, " "), "token_use": "access", "client_id": session.ClientID})
	if err != nil {
		oauthError(w, 500, "server_error")
		return
	}
	result := map[string]any{"access_token": access, "token_type": "Bearer", "expires_in": int64(expires.Sub(now).Seconds()), "scope": strings.Join(scopes, " ")}
	if refresh != "" {
		result["refresh_token"] = refresh
		result["refresh_token_expires_in"] = int64(session.ExpiresAt.Sub(now).Seconds())
	}
	if code.UserID != "" && slices.Contains(scopes, "openid") {
		id, err := s.sign(session.UserID, session.ClientID, expires, "JWT", map[string]any{"nonce": code.Nonce, "acr": code.ACR, "auth_time": code.AuthTime, "token_use": "id"})
		if err != nil {
			oauthError(w, 500, "server_error")
			return
		}
		result["id_token"] = id
	}
	respond(w, 200, result)
}

func tokenStoreError(w http.ResponseWriter, err error) {
	if errors.Is(err, sql.ErrNoRows) || errors.Is(err, configstore.ErrNotFound) || errors.Is(err, configstore.ErrIdentityDenied) {
		oauthError(w, 400, "invalid_grant")
	} else {
		oauthError(w, 503, "temporarily_unavailable")
	}
}

func (s *Server) tokenIdentity(ctx context.Context, id string) (configstore.EffectiveIdentity, error) {
	p, err := s.store.EffectiveIdentity(ctx, id)
	if err == nil && (p.Provider != s.cfg.Auth.SSO.Upstream.Namespace() || (s.cfg.Auth.SSO.DirectoryTokenEnv != "" && !p.DirectoryManaged)) {
		err = configstore.ErrIdentityDenied
	}
	return p, err
}

func (s *Server) sign(subject, audience string, expires time.Time, typ string, extra map[string]any) (string, error) {
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.EdDSA, Key: s.key}, (&jose.SignerOptions{}).WithType(jose.ContentType(typ)).WithHeader("kid", "mcphub-sso"))
	if err != nil {
		return "", err
	}
	now := time.Now()
	return jwt.Signed(signer).Claims(jwt.Claims{Issuer: s.cfg.Auth.Issuer, Subject: subject, Audience: jwt.Audience{audience}, IssuedAt: jwt.NewNumericDate(now), NotBefore: jwt.NewNumericDate(now), Expiry: jwt.NewNumericDate(expires)}).Claims(extra).Serialize()
}

type Verifier struct {
	server   *Server
	resource string
}

func (s *Server) Verifier(resource string) *Verifier { return &Verifier{server: s, resource: resource} }
func (v *Verifier) Ready() bool                      { return true }
func (v *Verifier) Verify(ctx context.Context, raw string, _ *http.Request) (*mcpauth.TokenInfo, error) {
	invalid := func() (*mcpauth.TokenInfo, error) {
		return nil, fmt.Errorf("%w: SSO credential or user permission is invalid", mcpauth.ErrInvalidToken)
	}
	if len(raw) > 32<<10 {
		return invalid()
	}
	token, err := jwt.ParseSigned(raw, []jose.SignatureAlgorithm{jose.EdDSA})
	if err != nil {
		return invalid()
	}
	var claims jwt.Claims
	var extra struct {
		Session  string `json:"sid"`
		Scope    string `json:"scope"`
		Use      string `json:"token_use"`
		ClientID string `json:"client_id"`
	}
	if token.Claims(v.server.key.Public(), &claims, &extra) != nil || claims.Subject == "" || claims.Expiry == nil || extra.Use != "access" || claims.ValidateWithLeeway(jwt.Expected{Issuer: v.server.cfg.Auth.Issuer, AnyAudience: jwt.Audience{v.resource}, Time: time.Now()}, 15*time.Second) != nil {
		return invalid()
	}
	session, err := v.server.store.SSOSession(ctx, extra.Session)
	if err != nil || session.UserID != claims.Subject || session.Resource != v.resource || session.ClientID != extra.ClientID {
		return invalid()
	}
	identity, err := v.server.tokenIdentity(ctx, claims.Subject)
	if err != nil {
		return invalid()
	}
	scopes := v.server.grantedScopes(identity, strings.Fields(extra.Scope))
	return &mcpauth.TokenInfo{UserID: identity.ID, Scopes: scopes, Expiration: claims.Expiry.Time(), Extra: map[string]any{"issuer": v.server.cfg.Auth.Issuer, "identity": &identity}}, nil
}

func Identity(info *mcpauth.TokenInfo) *configstore.EffectiveIdentity {
	if info == nil {
		return nil
	}
	p, _ := info.Extra["identity"].(*configstore.EffectiveIdentity)
	return p
}
