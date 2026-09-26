package sso

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/SamuelSupe/mcphub/v2/internal/authn"
	"github.com/SamuelSupe/mcphub/v2/internal/configstore"
	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

func (s *Server) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	for _, values := range q {
		if len(values) != 1 {
			http.Error(w, "duplicate authorization parameter", 400)
			return
		}
	}
	challenge, err := base64.RawURLEncoding.DecodeString(q.Get("code_challenge"))
	if len(r.URL.RawQuery) > 8192 || !s.allowedClient(q.Get("client_id"), q.Get("redirect_uri"), q.Get("resource")) || q.Get("response_type") != "code" || q.Get("code_challenge_method") != "S256" || err != nil || len(challenge) != 32 || q.Get("state") == "" {
		http.Error(w, "invalid OAuth client, resource, redirect URI or PKCE", 400)
		return
	}
	state, browser, verifier, nonce := rand.Text(), rand.Text(), oauth2.GenerateVerifier(), rand.Text()
	p := login{expires: time.Now().Add(5 * time.Minute), browser: browser, verifier: verifier, nonce: nonce, downstream: authorizationCode{ClientID: q.Get("client_id"), Redirect: q.Get("redirect_uri"), Resource: q.Get("resource"), State: q.Get("state"), Challenge: q.Get("code_challenge"), Nonce: q.Get("nonce"), Scopes: strings.Fields(q.Get("scope"))}}
	upstream := s.cfg.Auth.SSO.Upstream
	endpoint := oauth2.Endpoint{AuthURL: upstream.AuthorizationURL, TokenURL: upstream.TokenURL, AuthStyle: oauth2.AuthStyleInParams}
	if upstream.TokenAuthMethod == "client_secret_basic" {
		endpoint.AuthStyle = oauth2.AuthStyleInHeader
	}
	scopes := slices.Clone(upstream.Scopes)
	if upstream.Protocol == "oidc" {
		metadata, err := authn.LoginMetadata(r.Context(), upstream.Issuer, s.client, false)
		if err != nil {
			http.Error(w, "SSO discovery unavailable", 503)
			return
		}
		p.issuerRequired = metadata.AuthorizationResponseIssParameterSupported
		endpoint.AuthURL, endpoint.TokenURL = metadata.AuthorizationEndpoint, metadata.TokenEndpoint
		p.idVerifier, err = authn.LoginIDVerifier(r.Context(), upstream.Issuer, upstream.ClientID, s.client)
		if err != nil {
			http.Error(w, "SSO identity verifier unavailable", 503)
			return
		}
		if !slices.Contains(scopes, "openid") {
			scopes = append(scopes, "openid")
		}
	}
	p.oauth = oauth2.Config{ClientID: upstream.ClientID, ClientSecret: os.Getenv(upstream.ClientSecretEnv), RedirectURL: s.cfg.Auth.Issuer + "/callback", Endpoint: endpoint, Scopes: scopes}
	options := []oauth2.AuthCodeOption{oauth2.S256ChallengeOption(verifier)}
	if upstream.Protocol == "oidc" {
		options = append(options, oauth2.SetAuthURLParam("nonce", nonce))
		for _, name := range []string{"prompt", "max_age", "acr_values"} {
			if q.Get(name) != "" {
				options = append(options, oauth2.SetAuthURLParam(name, q.Get(name)))
			}
		}
	} else if q.Get("acr_values") != "" || q.Get("max_age") != "" {
		http.Error(w, "upstream OAuth2 cannot prove OIDC step-up authentication", 400)
		return
	}
	s.mu.Lock()
	s.cleanupLocked()
	if len(s.pending) >= 1024 {
		s.mu.Unlock()
		http.Error(w, "too many pending logins", 429)
		return
	}
	s.pending[state] = p
	s.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: "__Host-mcphub-sso-" + state, Value: browser, Path: "/", HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode, MaxAge: 300})
	http.Redirect(w, r, p.oauth.AuthCodeURL(state, options...), http.StatusFound)
}

func (s *Server) callback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	state := q.Get("state")
	for _, values := range q {
		if len(values) != 1 {
			http.Error(w, "duplicate callback parameter", 400)
			return
		}
	}
	s.mu.Lock()
	p, ok := s.pending[state]
	delete(s.pending, state)
	s.mu.Unlock()
	cookie, err := r.Cookie("__Host-mcphub-sso-" + state)
	if !ok || !time.Now().Before(p.expires) || err != nil || subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(p.browser)) != 1 {
		http.Error(w, "SSO callback validation failed", 400)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "__Host-mcphub-sso-" + state, Value: "", Path: "/", HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode, MaxAge: -1})
	issuer := s.cfg.Auth.SSO.Upstream.Issuer
	if (q.Get("iss") != "" && q.Get("iss") != issuer) || (p.issuerRequired && q.Get("iss") == "") {
		http.Error(w, "SSO response issuer mismatch", 400)
		return
	}
	if q.Get("error") != "" {
		s.redirectResult(w, r, p.downstream, "", "access_denied")
		return
	}
	if q.Get("code") == "" {
		http.Error(w, "authorization code required", 400)
		return
	}
	ctx := oidc.ClientContext(r.Context(), s.client)
	token, err := p.oauth.Exchange(ctx, q.Get("code"), oauth2.VerifierOption(p.verifier))
	if err != nil {
		http.Error(w, "SSO code exchange failed", 502)
		return
	}
	claims, err := s.upstreamClaims(r, p, token)
	if err != nil {
		http.Error(w, "SSO identity verification failed", 401)
		return
	}
	upstream := s.cfg.Auth.SSO.Upstream
	subjectPath := upstream.SubjectClaim
	if upstream.Protocol == "oidc" {
		subjectPath = "sub"
	}
	subject, _ := claimValue(claims, subjectPath).(string)
	if subject == "" {
		http.Error(w, "SSO response has no stable subject", 401)
		return
	}
	name, _ := claimValue(claims, upstream.NameClaim).(string)
	if upstream.TenantClaim != "" && claimValue(claims, upstream.TenantClaim) != upstream.TenantValue {
		http.Error(w, "SSO tenant is not allowed", 403)
		return
	}
	groups, err := membershipClaim(claims, upstream.GroupsClaim)
	if err != nil {
		http.Error(w, "invalid group claim", 401)
		return
	}
	departments, err := membershipClaim(claims, upstream.DepartmentsClaim)
	if err != nil {
		http.Error(w, "invalid department claim", 401)
		return
	}
	user, err := s.store.SyncIdentity(configstore.WithActor(r.Context(), "sso-login"), upstream.Namespace(), subject, name, groups, departments, s.cfg.Auth.SSO.DirectoryTokenEnv != "", slices.Contains(s.cfg.Auth.SSO.BootstrapSubjects, subject))
	if err != nil {
		http.Error(w, "SSO user synchronization failed", 503)
		return
	}
	identity, err := s.tokenIdentity(r.Context(), user.ID)
	if err != nil {
		http.Error(w, "Your identity was recorded. Access is pending or disabled; ask the MCPHub administrator to authorize your account. 身份已记录，请联系 MCPHub 管理员授权。", 403)
		return
	}
	code := p.downstream
	code.UserID, code.Scopes, code.Expires = user.ID, s.grantedScopes(identity, code.Scopes), time.Now().Add(time.Minute)
	if upstream.Protocol == "oidc" {
		code.ACR, _ = claims["acr"].(string)
		if value, ok := claims["auth_time"].(json.Number); ok {
			code.AuthTime, _ = value.Int64()
		}
	}
	raw := rand.Text() + rand.Text()
	s.mu.Lock()
	s.cleanupLocked()
	if len(s.codes) >= 1024 {
		s.mu.Unlock()
		http.Error(w, "too many pending authorization codes", 429)
		return
	}
	s.codes[configstore.SecretHash(raw)] = code
	s.mu.Unlock()
	s.redirectResult(w, r, code, raw, "")
}

func (s *Server) redirectResult(w http.ResponseWriter, r *http.Request, p authorizationCode, code, err string) {
	target, _ := url.Parse(p.Redirect)
	q := target.Query()
	q.Set("state", p.State)
	q.Set("iss", s.cfg.Auth.Issuer)
	if err != "" {
		q.Set("error", err)
	} else {
		q.Set("code", code)
	}
	target.RawQuery = q.Encode()
	http.Redirect(w, r, target.String(), http.StatusSeeOther)
}

func (s *Server) upstreamClaims(r *http.Request, p login, token *oauth2.Token) (map[string]any, error) {
	var claims map[string]any
	if p.idVerifier != nil {
		raw, _ := token.Extra("id_token").(string)
		id, err := p.idVerifier.Verify(oidc.ClientContext(r.Context(), s.client), raw)
		if err != nil {
			return nil, err
		}
		if id.Nonce != p.nonce || id.Subject == "" || (id.AccessTokenHash != "" && id.VerifyAccessToken(token.AccessToken) != nil) {
			return nil, errors.New("ID token binding mismatch")
		}
		var data json.RawMessage
		if err = id.Claims(&data); err != nil {
			return nil, err
		}
		decoder := json.NewDecoder(strings.NewReader(string(data)))
		decoder.UseNumber()
		err = decoder.Decode(&claims)
		return claims, err
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, s.cfg.Auth.SSO.Upstream.UserInfoURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token.AccessToken)
	response, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return nil, errors.New("userinfo rejected")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, (1<<20)+1))
	if err != nil || len(data) > 1<<20 {
		return nil, errors.New("userinfo response too large")
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.UseNumber()
	if err = decoder.Decode(&claims); err != nil {
		return nil, err
	}
	if provider := s.cfg.Auth.SSO.Upstream; provider.SuccessClaim != "" {
		value := claimValue(claims, provider.SuccessClaim)
		if value == nil || fmt.Sprint(value) != provider.SuccessValue {
			return nil, errors.New("userinfo did not report success")
		}
	}
	return claims, nil
}

func claimValue(claims map[string]any, path string) any {
	if path == "" {
		return nil
	}
	var value any = claims
	for _, key := range strings.Split(path, ".") {
		object, ok := value.(map[string]any)
		if !ok {
			return nil
		}
		value = object[key]
	}
	return value
}

func membershipClaim(claims map[string]any, path string) ([]string, error) {
	if path == "" {
		return nil, nil
	}
	value := claimValue(claims, path)
	if value == nil {
		return nil, nil
	}
	array, ok := value.([]any)
	if !ok {
		return nil, errors.New("membership must be an array")
	}
	if len(array) > 256 {
		return nil, errors.New("too many memberships")
	}
	result := make([]string, 0, len(array))
	for _, v := range array {
		id, ok := v.(string)
		if !ok || id == "" || len(id) > 256 {
			return nil, errors.New("invalid membership")
		}
		result = append(result, id)
	}
	slices.Sort(result)
	return slices.Compact(result), nil
}
