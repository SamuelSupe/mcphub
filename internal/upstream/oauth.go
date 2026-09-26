package upstream

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/SamuelSupe/mcphub/v2/internal/authn"
	"github.com/SamuelSupe/mcphub/v2/internal/configstore"
	"golang.org/x/oauth2"
)

type Login struct {
	Endpoint                 Endpoint
	Binding                  configstore.CredentialBinding
	Browser, Verifier, Nonce string
	IssuerRequired           bool
	OAuth                    *oauth2.Config
	Expires                  time.Time
}

func (m *Manager) oauthConfig(ctx context.Context, e Endpoint, redirect string) (*oauth2.Config, bool, error) {
	c := e.Credentials.OAuth
	if c == nil {
		return nil, false, ErrConnect
	}
	meta, err := authn.LoginMetadata(ctx, c.Issuer, m.HTTP, c.ClientSecretPath == "")
	if err != nil {
		return nil, false, ErrUnavailable
	}
	secret := ""
	style := oauth2.AuthStyleInParams
	if c.ClientSecretPath != "" {
		var values map[string]string
		if _, err := m.Vault.Read(ctx, c.ClientSecretPath, &values); err != nil {
			return nil, false, err
		}
		secret = values["client_secret"]
		if secret == "" {
			return nil, false, ErrUnavailable
		}
		if len(meta.TokenEndpointAuthMethodsSupported) == 0 || slices.Contains(meta.TokenEndpointAuthMethodsSupported, "client_secret_basic") {
			style = oauth2.AuthStyleInHeader
		} else if !slices.Contains(meta.TokenEndpointAuthMethodsSupported, "client_secret_post") {
			return nil, false, ErrUnavailable
		}
	}
	scopes := slices.Clone(c.Scopes)
	if !slices.Contains(scopes, "openid") {
		scopes = append(scopes, "openid")
	}
	if slices.Contains(meta.ScopesSupported, "offline_access") && !slices.Contains(scopes, "offline_access") {
		scopes = append(scopes, "offline_access")
	}
	return &oauth2.Config{ClientID: c.ClientID, ClientSecret: secret, RedirectURL: redirect, Scopes: scopes, Endpoint: oauth2.Endpoint{AuthURL: meta.AuthorizationEndpoint, TokenURL: meta.TokenEndpoint, AuthStyle: style}}, meta.AuthorizationResponseIssParameterSupported, nil
}

func (m *Manager) StartLogin(ctx context.Context, e Endpoint, issuer, subject, browser, redirect string) (string, error) {
	b, err := m.Store.CredentialBinding(ctx, issuer, subject, e.UID)
	if err != nil {
		return "", ErrUnavailable
	}
	oauth, required, err := m.oauthConfig(ctx, e, redirect)
	if err != nil {
		return "", err
	}
	l := Login{Endpoint: e, Binding: b, Browser: browser, Verifier: oauth2.GenerateVerifier(), Nonce: rand.Text(), IssuerRequired: required, OAuth: oauth, Expires: time.Now().Add(5 * time.Minute)}
	state := rand.Text()
	m.loginMu.Lock()
	defer m.loginMu.Unlock()
	for key, pending := range m.logins {
		if !time.Now().Before(pending.Expires) {
			delete(m.logins, key)
		}
	}
	if len(m.logins) >= 256 {
		return "", ErrUnavailable
	}
	m.logins[state] = l
	return oauth.AuthCodeURL(state, oauth2.S256ChallengeOption(l.Verifier), oauth2.SetAuthURLParam("nonce", l.Nonce), oauth2.SetAuthURLParam("resource", e.URL)), nil
}

func (m *Manager) TakeLogin(state, issuer, subject, browser string) (Login, error) {
	m.loginMu.Lock()
	defer m.loginMu.Unlock()
	l, ok := m.logins[state]
	if !ok || l.Browser != browser || l.Binding.Issuer != issuer || l.Binding.Subject != subject || !time.Now().Before(l.Expires) {
		return Login{}, ErrConflict
	}
	delete(m.logins, state)
	return l, nil
}

func (m *Manager) Exchange(ctx context.Context, l Login, code, responseIssuer string) (configstore.CredentialBinding, Secret, error) {
	b := l.Binding
	if (l.IssuerRequired && responseIssuer == "") || (responseIssuer != "" && responseIssuer != l.Endpoint.Credentials.OAuth.Issuer) || code == "" {
		return b, Secret{}, ErrReconnect
	}
	token, err := l.OAuth.Exchange(context.WithValue(ctx, oauth2.HTTPClient, m.HTTP), code, oauth2.VerifierOption(l.Verifier), oauth2.SetAuthURLParam("resource", l.Endpoint.URL))
	if err != nil || token == nil || token.AccessToken == "" || !strings.EqualFold(token.Type(), "Bearer") || token.Expiry.IsZero() || !time.Now().Before(token.Expiry) {
		return b, Secret{}, ErrReconnect
	}
	verifier, err := authn.LoginIDVerifier(ctx, l.Endpoint.Credentials.OAuth.Issuer, l.OAuth.ClientID, m.HTTP)
	if err != nil {
		return b, Secret{}, ErrUnavailable
	}
	raw, _ := token.Extra("id_token").(string)
	id, err := verifier.Verify(ctx, raw)
	if err != nil || id.Nonce != l.Nonce || id.Subject == "" {
		return b, Secret{}, ErrReconnect
	}
	if id.AccessTokenHash != "" && id.VerifyAccessToken(token.AccessToken) != nil {
		return b, Secret{}, ErrReconnect
	}
	var claims struct {
		Name  string `json:"preferred_username"`
		Email string `json:"email"`
	}
	if id.Claims(&claims) != nil {
		return b, Secret{}, ErrReconnect
	}
	b.AccountID, b.Account = id.Subject, claims.Name
	if b.Account == "" {
		b.Account = claims.Email
	}
	if b.Account == "" {
		b.Account = id.Subject
	}
	if b.Account == "" || len(b.Account) > 256 {
		b.Account = "Connected account"
	}
	scopes := l.OAuth.Scopes
	if raw, ok := token.Extra("scope").(string); ok {
		scopes = strings.Fields(raw)
	}
	if !containsScopes(scopes, l.Endpoint.Credentials.OAuth.Scopes) {
		return b, Secret{}, ErrReconnect
	}
	return b, Secret{Token: token, Scopes: scopes}, nil
}

func (m *Manager) refresh(ctx context.Context, e Endpoint, previous Secret) (Secret, error) {
	cfg, _, err := m.oauthConfig(ctx, e, "")
	if err != nil {
		return Secret{}, err
	}
	form := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {previous.Token.RefreshToken}, "resource": {e.URL}}
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
	resp, err := m.HTTP.Do(req)
	if err != nil {
		return Secret{}, ErrUnavailable
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	var value struct {
		AccessToken  string      `json:"access_token"`
		RefreshToken string      `json:"refresh_token"`
		TokenType    string      `json:"token_type"`
		ExpiresIn    json.Number `json:"expires_in"`
		Scope        *string     `json:"scope"`
		Error        string      `json:"error"`
	}
	if err != nil || len(data) > 1<<20 || json.Unmarshal(data, &value) != nil {
		return Secret{}, ErrUnavailable
	}
	if value.Error == "invalid_grant" || value.Error == "invalid_client" {
		return Secret{}, ErrReconnect
	}
	if resp.StatusCode != http.StatusOK || value.Error != "" {
		return Secret{}, ErrUnavailable
	}
	seconds, err := strconv.ParseInt(string(value.ExpiresIn), 10, 64)
	if err != nil || seconds <= 0 || seconds > 315360000 || value.AccessToken == "" || !strings.EqualFold(value.TokenType, "Bearer") {
		return Secret{}, ErrReconnect
	}
	if value.RefreshToken == "" {
		value.RefreshToken = previous.Token.RefreshToken
	}
	scopes := slices.Clone(previous.Scopes)
	if value.Scope != nil {
		scopes = strings.Fields(*value.Scope)
	}
	// Save even a narrowed grant before returning an authorization error, since
	// the previous refresh token may already have been consumed by the provider.
	return Secret{Token: &oauth2.Token{AccessToken: value.AccessToken, RefreshToken: value.RefreshToken, TokenType: "Bearer", Expiry: time.Now().Add(time.Duration(seconds) * time.Second)}, Scopes: scopes, Invalid: !containsScopes(scopes, e.Credentials.OAuth.Scopes)}, nil
}
