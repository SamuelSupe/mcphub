package sso

import (
	"context"
	"crypto/subtle"
	"errors"
	"html/template"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/SamuelSupe/mcphub/v2/internal/config"
	"github.com/SamuelSupe/mcphub/v2/internal/configstore"
	"golang.org/x/oauth2"
	"golang.org/x/time/rate"
)

type loginAttempts struct {
	limiter *rate.Limiter
	seen    time.Time
}

// Account lockouts persist in the store. This separate, bounded IP limit also
// protects hashing capacity when an attacker rotates nonexistent usernames.
func (s *Server) AuthenticateLocal(ctx context.Context, username, password, code, address string, mfa bool) (configstore.Identity, bool, error) {
	if !s.cfg.Auth.Builtin() {
		return configstore.Identity{}, false, configstore.ErrCredentials
	}
	release, err := s.reservePasswordAttempt(address, "")
	if err != nil {
		return configstore.Identity{}, false, err
	}
	defer release()
	return s.store.AuthenticateLocal(ctx, username, password, code, mfa)
}

func (s *Server) Builtin() bool { return s.cfg.Auth.Builtin() }
func (s *Server) EnterpriseEnabled() bool {
	c := s.Connections()
	return c.OIDC.Enabled || c.LDAP.Enabled
}
func (s *Server) Initialized(ctx context.Context) bool { return s.store.LocalInitialized(ctx) }

func (s *Server) LocalCredentials(ctx context.Context, user configstore.Identity, client, resource string, scopes []string) (*oauth2.Token, error) {
	if !s.cfg.Auth.Builtin() || user.Provider != config.LocalIdentityProvider || !slices.ContainsFunc(s.cfg.Auth.SSO.Clients, func(c config.SSOClient) bool { return c.ID == client && slices.Contains(c.Resources, resource) }) {
		return nil, configstore.ErrIdentityDenied
	}
	identity, err := s.tokenIdentity(ctx, user.ID)
	if err != nil || identity.CredentialVersion != user.CredentialVersion {
		return nil, configstore.ErrIdentityDenied
	}
	session, refresh, err := s.store.CreateSSOSession(ctx, configstore.SSOSession{UserID: user.ID, CredentialVersion: user.CredentialVersion, ClientID: client, Resource: resource, Scopes: s.grantedScopes(identity, append(slices.Clone(scopes), "offline_access")), ExpiresAt: time.Now().Add(8 * time.Hour)}, true)
	if err != nil {
		return nil, err
	}
	return s.sessionToken(ctx, session, refresh)
}

func (s *Server) RefreshCredentials(ctx context.Context, refresh, client, resource string) (*oauth2.Token, error) {
	session, next, err := s.store.RotateSSORefresh(ctx, refresh, client, resource)
	if err != nil {
		return nil, err
	}
	return s.sessionToken(ctx, session, next)
}

func (s *Server) sessionToken(ctx context.Context, session configstore.SSOSession, refresh string) (*oauth2.Token, error) {
	identity, err := s.tokenIdentity(ctx, session.UserID)
	if err != nil || identity.CredentialVersion != session.CredentialVersion {
		return nil, configstore.ErrIdentityDenied
	}
	expires := time.Now().Add(10 * time.Minute)
	if session.ExpiresAt.Before(expires) {
		expires = session.ExpiresAt
	}
	scopes := strings.Join(s.grantedScopes(identity, session.Scopes), " ")
	access, err := s.sign(session.UserID, session.Resource, expires, "at+jwt", map[string]any{"sid": session.ID, "scope": scopes, "token_use": "access", "client_id": session.ClientID})
	return (&oauth2.Token{AccessToken: access, TokenType: "Bearer", RefreshToken: refresh, Expiry: expires}).WithExtra(map[string]any{"scope": scopes}), err
}

func (s *Server) beginLocalLogin(w http.ResponseWriter, r *http.Request, state string, p login) {
	p.enterpriseQuery = r.URL.Query()
	s.mu.Lock()
	s.cleanupLocked()
	if len(s.pending) >= 1024 {
		s.mu.Unlock()
		http.Error(w, "too many pending logins", 429)
		return
	}
	s.pending[state] = p
	s.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: "__Host-mcphub-sso-" + state, Value: p.browser, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: 300})
	s.renderLocalLogin(w, state, r.URL.Query(), "")
}

var loginPage = template.Must(template.New("login").Parse(`<!doctype html><html lang="{{.Lang}}"><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>MCPHub · {{.Title}}</title><style>body{margin:0;background:#f4f5f7;color:#20252b;font:16px system-ui}main{max-width:380px;margin:8vh auto;padding:32px;background:white;border:1px solid #dde1e7;border-radius:14px}h1{font-size:24px}label{display:block;margin-top:16px}input,select,button{box-sizing:border-box;width:100%;padding:12px;margin-top:8px;font:inherit;border:1px solid #bbc2ce;border-radius:7px}button{background:#2453db;color:white;border:0;margin:24px 0}p{line-height:1.6}a{color:#2453db}</style><main><strong>MCPHub</strong><h1>{{.Title}}</h1><p>{{.Message}}</p><form method="post" action="/sso/login"><input type="hidden" name="state" value="{{.State}}"><input type="hidden" name="lang" value="{{.Lang}}">{{if .LDAP}}<label>{{.SourceLabel}}<select name="provider"><option value="local">{{.LocalLabel}}</option><option value="ldap" {{if eq .SelectedLDAP "ldap"}}selected{{end}}>LDAP</option></select></label>{{end}}<label>{{.Username}}<input name="username" autocomplete="username" required maxlength="256"></label><label>{{.Password}}<input name="password" type="password" autocomplete="current-password" required maxlength="1024"></label><label>{{.OTP}}<input name="code" autocomplete="one-time-code" inputmode="numeric" pattern="[0-9]{6}" maxlength="6"></label><button>{{.Title}}</button></form>{{if .Enterprise}}<a href="{{.Enterprise}}">{{.EnterpriseLabel}}</a>{{end}}<p><a href="{{.LanguageURL}}">{{.LanguageLabel}}</a></p></main></html>`))

func (s *Server) renderLocalLogin(w http.ResponseWriter, state string, query url.Values, message string) {
	lang := query.Get("lang")
	data := map[string]string{"Lang": "zh-CN", "Title": "登录", "Username": "账号", "Password": "密码", "OTP": "动态验证码（本地账号启用 MFA 时必填）", "EnterpriseLabel": "使用 OIDC 企业登录", "LanguageLabel": "English", "Message": message, "State": state}
	if lang == "en" {
		data = map[string]string{"Lang": "en", "Title": "Sign in", "Username": "Username", "Password": "Password", "OTP": "Authenticator code (local accounts with MFA)", "EnterpriseLabel": "Enterprise OIDC sign-in", "LanguageLabel": "中文", "Message": message, "State": state}
	}
	q := url.Values{"state": {state}, "lang": {"en"}}
	if lang == "en" {
		q.Set("lang", "zh-CN")
	}
	data["LanguageURL"] = "/sso/login?" + q.Encode()
	c := s.Connections()
	if c.LDAP.Enabled {
		data["LDAP"], data["SourceLabel"], data["LocalLabel"] = "enabled", "账号来源", "MCPHub 本地账号"
		if lang == "en" {
			data["SourceLabel"], data["LocalLabel"] = "Account source", "MCPHub local account"
		}
		data["SelectedLDAP"] = query.Get("provider")
		if data["SelectedLDAP"] == "" {
			s.mu.Lock()
			source := s.pending[state].enterpriseQuery.Get("source")
			s.mu.Unlock()
			if source == "enterprise" || source == "ldap" {
				data["SelectedLDAP"] = "ldap"
			}
		}
	}
	if c.OIDC.Enabled {
		s.mu.Lock()
		p := s.pending[state]
		s.mu.Unlock()
		q, _ := url.ParseQuery(p.enterpriseQuery.Encode())
		if q.Get("client_id") != "" {
			q.Set("source", "oidc")
			data["Enterprise"] = "/sso/authorize?" + q.Encode()
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	s.mu.Lock()
	callback := s.pending[state].downstream.Redirect
	s.mu.Unlock()
	w.Header().Set("Content-Security-Policy", browserFormPolicy(callback))
	_ = loginPage.Execute(w, data)
}

func (s *Server) localLogin(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.Auth.Builtin() || (r.Method != "GET" && r.Method != "POST") {
		http.NotFound(w, r)
		return
	}
	if r.Method == "POST" {
		r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
		issuer, _ := url.Parse(s.cfg.Auth.Issuer)
		if r.Header.Get("Origin") != issuer.Scheme+"://"+issuer.Host || r.ParseForm() != nil || len(r.URL.Query()) != 0 {
			http.Error(w, "invalid login request", 400)
			return
		}
		for _, v := range r.PostForm {
			if len(v) != 1 {
				http.Error(w, "duplicate parameter", 400)
				return
			}
		}
	}
	q := r.URL.Query()
	if r.Method == "POST" {
		q = r.PostForm
	}
	state := q.Get("state")
	s.mu.Lock()
	p, ok := s.pending[state]
	s.mu.Unlock()
	cookie, err := r.Cookie("__Host-mcphub-sso-" + state)
	if !ok || !time.Now().Before(p.expires) || err != nil || subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(p.browser)) != 1 {
		http.Error(w, "login expired; restart authorization", 400)
		return
	}
	if r.Method == "GET" {
		s.renderLocalLogin(w, state, q, "")
		return
	}
	var user configstore.Identity
	var mfa bool
	c := s.Connections()
	if p.connectionsRevision != c.Revision {
		http.Error(w, "identity source changed; restart login", 400)
		return
	}
	if q.Get("provider") == "ldap" {
		user, err = s.authenticateLDAP(r.Context(), c.LDAP, q.Get("username"), q.Get("password"), r.RemoteAddr)
	} else if q.Get("provider") == "" || q.Get("provider") == "local" {
		user, mfa, err = s.AuthenticateLocal(r.Context(), q.Get("username"), q.Get("password"), q.Get("code"), r.RemoteAddr, false)
	} else {
		err = configstore.ErrCredentials
	}
	if err != nil {
		if errors.Is(err, configstore.ErrLoginLimited) {
			w.Header().Set("Retry-After", "300")
		}
		message := "账号、密码或动态验证码无效，或账号不可用。请稍后重试。"
		if q.Get("lang") == "en" {
			message = "Invalid credentials or account unavailable. Please retry later."
		}
		s.renderLocalLogin(w, state, q, message)
		return
	}
	if _, err := s.tokenIdentity(r.Context(), user.ID); err != nil {
		s.mu.Lock()
		delete(s.pending, state)
		s.mu.Unlock()
		http.SetCookie(w, &http.Cookie{Name: "__Host-mcphub-sso-" + state, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: -1})
		if errors.Is(err, configstore.ErrIdentityDenied) {
			s.redirectResult(w, r, p.downstream, "", "account_access_required")
		} else {
			http.Error(w, "identity storage unavailable", 503)
		}
		return
	}
	s.mu.Lock()
	current, ok := s.pending[state]
	delete(s.pending, state)
	s.cleanupLocked()
	if !ok || current.connectionsRevision != s.connections.Revision || !time.Now().Before(current.expires) || len(s.codes) >= 1024 {
		s.mu.Unlock()
		http.Error(w, "login expired", 400)
		return
	}
	code := p.downstream
	code.UserID, code.CredentialVersion, code.AuthTime, code.Expires = user.ID, user.CredentialVersion, time.Now().Unix(), time.Now().Add(time.Minute)
	code.ACR = "urn:mcphub:auth:password"
	if q.Get("provider") == "ldap" {
		code.ACR = "urn:mcphub:auth:ldap-password"
	}
	if mfa {
		code.ACR = config.LocalMFAACR
	}
	s.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: "__Host-mcphub-sso-" + state, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: -1})
	s.finishAuthorization(w, r, code)
}

func (s *Server) reservePasswordAttempt(address, account string) (func(), error) {
	address, _, _ = net.SplitHostPort(address)
	s.mu.Lock()
	now := time.Now()
	for key, value := range s.attempts {
		if now.Sub(value.seen) > 10*time.Minute {
			delete(s.attempts, key)
		}
	}
	p := s.attempts[address]
	if p == nil && len(s.attempts) < 4096 {
		// A private TLS proxy shares its peer IP across users. Keep this limit
		// above normal team sign-in traffic; account lockouts and the four hash
		// slots enforce the tighter credential and memory bounds.
		p = &loginAttempts{limiter: rate.NewLimiter(10, 100)}
		s.attempts[address] = p
	}
	allowed := p != nil && p.limiter.Allow()
	if account != "" {
		key := "account:" + account
		entry := s.attempts[key]
		if entry == nil && len(s.attempts) < 4096 {
			entry = &loginAttempts{limiter: rate.NewLimiter(rate.Every(12*time.Second), 5)}
			s.attempts[key] = entry
		}
		allowed = allowed && entry != nil && entry.limiter.Allow()
		if entry != nil {
			entry.seen = now
		}
	}
	if p != nil {
		p.seen = now
	}
	s.mu.Unlock()
	if !allowed {
		return nil, configstore.ErrLoginLimited
	}
	select {
	case s.passwordSlots <- struct{}{}:
		return func() { <-s.passwordSlots }, nil
	default:
		return nil, configstore.ErrLoginLimited
	}
}

func (s *Server) DeviceSessionToken(ctx context.Context, session configstore.SSOSession, refresh string) (*oauth2.Token, error) {
	return s.sessionToken(ctx, session, refresh)
}
