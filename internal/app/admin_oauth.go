package app

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	mcpauth "github.com/modelcontextprotocol/go-sdk/auth"
	"golang.org/x/oauth2"

	"github.com/SamuelSupe/mcphub/v2/internal/authn"
)

func (a *adminAuthorization) login(w http.ResponseWriter, req *http.Request) {
	target, err := a.startLogin(w, req, "", nil)
	if err != nil {
		http.Error(w, "Login unavailable. Check identity service configuration.", 503)
		return
	}
	http.Redirect(w, req, target, http.StatusFound)
}

func (a *adminAuthorization) startLogin(w http.ResponseWriter, req *http.Request, approvalID string, session *adminSession) (string, error) {
	meta, err := authn.LoginMetadata(req.Context(), a.issuer, a.client, a.cfg.ClientSecretEnv == "")
	if err != nil {
		return "", err
	}
	style := oauth2.AuthStyleInParams
	secret := os.Getenv(a.cfg.ClientSecretEnv)
	if secret != "" {
		if len(meta.TokenEndpointAuthMethodsSupported) == 0 || slices.Contains(meta.TokenEndpointAuthMethodsSupported, "client_secret_basic") {
			style = oauth2.AuthStyleInHeader
		} else if !slices.Contains(meta.TokenEndpointAuthMethodsSupported, "client_secret_post") {
			return "", fmt.Errorf("unsupported OAuth client authentication method")
		}
	}
	scopes := slices.Clone(a.cfg.RequiredScopes)
	if !a.userPortal && req.URL.Query().Get("role") == "approver" || approvalID != "" {
		scopes = slices.Clone(a.cfg.Approvals.Scopes())
	}
	if !a.userPortal && req.URL.Query().Get("role") == "security" {
		scopes = slices.Clone(a.cfg.Approvals.PolicyChanges.Scopes())
	}
	requiredScopes := slices.Clone(scopes)
	if approvalID != "" && session != nil {
		session.mu.Lock()
		if session.oauth != nil {
			for _, scope := range session.oauth.Scopes {
				if !slices.Contains(scopes, scope) {
					scopes = append(scopes, scope)
				}
			}
		}
		session.mu.Unlock()
	}
	for _, scope := range []string{"openid", "offline_access"} {
		if slices.Contains(meta.ScopesSupported, scope) && !slices.Contains(scopes, scope) {
			scopes = append(scopes, scope)
		}
	}
	cfg := &oauth2.Config{ClientID: a.cfg.ClientID, ClientSecret: secret, Endpoint: oauth2.Endpoint{AuthURL: meta.AuthorizationEndpoint, TokenURL: meta.TokenEndpoint, AuthStyle: style}, RedirectURL: a.cfg.PublicURL + a.homePath() + "auth/callback", Scopes: scopes}
	state, browser, verifier := rand.Text(), rand.Text(), oauth2.GenerateVerifier()
	pending := adminLogin{browser: browser, verifier: verifier, oauth: cfg, requiredScopes: requiredScopes, issuerRequired: meta.AuthorizationResponseIssParameterSupported, expires: time.Now().Add(5 * time.Minute)}
	options := []oauth2.AuthCodeOption{oauth2.S256ChallengeOption(verifier), oauth2.SetAuthURLParam("resource", a.resourceURL())}
	if approvalID != "" {
		if session == nil || len(a.cfg.Approvals.StepUpACRValues) == 0 || !slices.Contains(meta.ScopesSupported, "openid") {
			return "", fmt.Errorf("configure OIDC and step_up_acr_values before requiring verification")
		}
		idVerifier, err := authn.LoginIDVerifier(req.Context(), a.issuer, a.cfg.ClientID, a.client)
		if err != nil {
			return "", err
		}
		pending.approvalID, pending.session, pending.subject = approvalID, session, session.subject
		pending.started, pending.nonce = time.Now(), rand.Text()
		pending.idVerifier = idVerifier
		options = append(options, oauth2.SetAuthURLParam("nonce", pending.nonce), oauth2.SetAuthURLParam("max_age", "0"), oauth2.SetAuthURLParam("prompt", "login"), oauth2.SetAuthURLParam("acr_values", strings.Join(a.cfg.Approvals.StepUpACRValues, " ")))
	}
	a.mu.Lock()
	a.prune()
	if len(a.logins) >= 256 || len(a.sessions) >= 1024 {
		a.mu.Unlock()
		return "", fmt.Errorf("too many active logins")
	}
	a.logins[state] = pending
	a.mu.Unlock()
	setAdminCookie(w, a.loginCookie(), browser, 300)
	return cfg.AuthCodeURL(state, options...), nil
}

func (a *adminAuthorization) callback(w http.ResponseWriter, req *http.Request) {
	if len(req.URL.RawQuery) > 8192 {
		http.Error(w, "Invalid callback", 400)
		return
	}
	q, err := url.ParseQuery(req.URL.RawQuery)
	cookie, cookieErr := req.Cookie(a.loginCookie())
	if err != nil || cookieErr != nil || len(q["state"]) != 1 {
		http.Error(w, "Invalid login state", 400)
		return
	}
	a.mu.Lock()
	pending, ok := a.logins[q.Get("state")]
	if ok && pending.browser == cookie.Value {
		delete(a.logins, q.Get("state"))
	}
	a.mu.Unlock()
	if !ok || pending.browser != cookie.Value || !time.Now().Before(pending.expires) {
		http.Error(w, "Invalid or expired login state", 400)
		return
	}
	setAdminCookie(w, a.loginCookie(), "", -1)
	iss := q.Get("iss")
	if len(q["iss"]) > 1 || (pending.issuerRequired && iss == "") || (iss != "" && iss != a.issuer) || q.Get("error") != "" || len(q["code"]) != 1 || q.Get("code") == "" {
		http.Redirect(w, req, a.homePath()+"?login_error=denied", http.StatusSeeOther)
		return
	}
	ctx := context.WithValue(req.Context(), oauth2.HTTPClient, a.client)
	token, err := pending.oauth.Exchange(ctx, q.Get("code"), oauth2.VerifierOption(pending.verifier), oauth2.SetAuthURLParam("resource", a.resourceURL()))
	if err != nil || token == nil || token.AccessToken == "" || !strings.EqualFold(token.Type(), "Bearer") {
		http.Redirect(w, req, a.homePath()+"?login_error=exchange", 303)
		return
	}
	info, status := a.verify(req.Context(), token.AccessToken, req)
	if status != http.StatusOK {
		reason := "invalid_token"
		if status == http.StatusForbidden {
			reason = "forbidden"
		}
		http.Redirect(w, req, a.homePath()+"?login_error="+reason, 303)
		return
	}
	// Schedule refresh using verified JWT expiry, never browser-supplied claims.
	token.Expiry = info.Expiration
	if pending.approvalID != "" {
		a.finishApprovalVerification(w, req, pending, token, info)
		return
	}
	session := &adminSession{token: token, oauth: pending.oauth, csrf: rand.Text(), subject: info.UserID, expires: time.Now().Add(8 * time.Hour)}
	id := rand.Text()
	a.mu.Lock()
	a.prune()
	if len(a.sessions) >= 1024 {
		a.mu.Unlock()
		http.Error(w, "Too many administrator sessions", 503)
		return
	}
	if old, err := req.Cookie(a.sessionCookie()); err == nil {
		delete(a.sessions, old.Value)
	}
	a.sessions[id] = session
	a.mu.Unlock()
	setAdminCookie(w, a.sessionCookie(), id, 8*60*60)
	http.Redirect(w, req, a.homePath(), http.StatusSeeOther)
}

// Called with a.mu held. Session expiry is fixed at login and never extended by
// background refresh, so a stolen browser cookie has a bounded lifetime.
func (a *adminAuthorization) prune() {
	now := time.Now()
	for id, login := range a.logins {
		if !now.Before(login.expires) {
			delete(a.logins, id)
		}
	}
	for id, session := range a.sessions {
		if !now.Before(session.expires) {
			delete(a.sessions, id)
		}
	}
}

func (a *adminAuthorization) sessionInfo(req *http.Request, session *adminSession) (*mcpauth.TokenInfo, int) {
	session.mu.Lock()
	defer session.mu.Unlock()
	if session.closed || session.token == nil || !time.Now().Before(session.expires) {
		return nil, http.StatusUnauthorized
	}
	if session.token.RefreshToken != "" && !time.Now().Add(30*time.Second).Before(session.token.Expiry) {
		token, status := a.refresh(req.Context(), session)
		if status != http.StatusOK {
			if status == http.StatusUnauthorized {
				session.closed = true
				session.token = nil
			}
			return nil, status
		}
		// Retain a rotated refresh token even if later JWT verification fails.
		session.token = token
	}
	info, status := a.verify(req.Context(), session.token.AccessToken, req)
	if status == http.StatusOK {
		if info.UserID != session.subject {
			session.closed = true
			session.token = nil
			return nil, http.StatusUnauthorized
		}
		session.token.Expiry = info.Expiration
	}
	return info, status
}

func (a *adminAuthorization) refresh(ctx context.Context, session *adminSession) (*oauth2.Token, int) {
	cfg := session.oauth
	form := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {session.token.RefreshToken}, "resource": {a.resourceURL()}}
	if cfg.Endpoint.AuthStyle == oauth2.AuthStyleInParams {
		form.Set("client_id", cfg.ClientID)
		if cfg.ClientSecret != "" {
			form.Set("client_secret", cfg.ClientSecret)
		}
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, cfg.Endpoint.TokenURL, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	if cfg.Endpoint.AuthStyle == oauth2.AuthStyleInHeader {
		req.SetBasicAuth(url.QueryEscape(cfg.ClientID), url.QueryEscape(cfg.ClientSecret))
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return nil, http.StatusServiceUnavailable
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	var value struct {
		AccessToken  string      `json:"access_token"`
		RefreshToken string      `json:"refresh_token"`
		TokenType    string      `json:"token_type"`
		ExpiresIn    json.Number `json:"expires_in"`
		Error        string      `json:"error"`
	}
	if err != nil || len(data) > 1<<20 || json.Unmarshal(data, &value) != nil {
		return nil, http.StatusServiceUnavailable
	}
	if value.Error == "invalid_grant" || value.Error == "invalid_client" {
		return nil, http.StatusUnauthorized
	}
	if resp.StatusCode != http.StatusOK || value.Error != "" || value.AccessToken == "" || !strings.EqualFold(value.TokenType, "Bearer") {
		return nil, http.StatusServiceUnavailable
	}
	token := &oauth2.Token{AccessToken: value.AccessToken, RefreshToken: value.RefreshToken, TokenType: value.TokenType}
	if token.RefreshToken == "" {
		token.RefreshToken = session.token.RefreshToken
	}
	if seconds, err := strconv.ParseInt(string(value.ExpiresIn), 10, 64); err == nil && seconds > 0 && seconds <= 315360000 {
		token.Expiry = time.Now().Add(time.Duration(seconds) * time.Second)
	}
	return token, http.StatusOK
}
