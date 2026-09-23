package client

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"golang.org/x/oauth2"
)

func prepareToken(token *oauth2.Token) error {
	if token == nil || token.AccessToken == "" || strings.ContainsAny(token.AccessToken, " \r\n\t") || !strings.EqualFold(token.Type(), "Bearer") {
		return errors.New("identity service did not issue a Bearer access token")
	}
	// JWT expiry is only a refresh scheduling hint; MCPHub verifies the token.
	if token.Expiry.IsZero() {
		parts := strings.Split(token.AccessToken, ".")
		if len(parts) == 3 {
			data, _ := base64.RawURLEncoding.DecodeString(parts[1])
			var claims struct {
				Exp int64 `json:"exp"`
			}
			if json.Unmarshal(data, &claims) == nil && claims.Exp > 0 {
				token.Expiry = time.Unix(claims.Exp, 0)
			}
		}
	}
	if token.Expiry.IsZero() || !time.Now().Before(token.Expiry) {
		return errors.New("identity service issued an expired access token or no usable expiry")
	}
	return nil
}

type credentials struct {
	store                   *Store
	name, session, endpoint string
	client                  *http.Client
}

func bindCredentials(ctx context.Context, store *Store, name string, c *http.Client) (*credentials, error) {
	bound := &credentials{store: store, name: name, client: httpClient(c, 30*time.Second)}
	err := store.locked(ctx, name, func() error {
		p, err := store.load(name)
		if errors.Is(err, os.ErrNotExist) {
			return ErrLoginRequired
		}
		if err != nil {
			return err
		}
		if p.Token == nil {
			return ErrLoginRequired
		}
		bound.session, bound.endpoint = p.Session, p.ServerURL
		return nil
	})
	return bound, err
}

func (c *credentials) token(ctx context.Context, rejected string) (string, error) {
	var access string
	err := c.store.locked(ctx, c.name, func() error {
		p, err := c.store.load(c.name)
		if errors.Is(err, os.ErrNotExist) {
			return ErrLoginRequired
		}
		if err != nil {
			return err
		}
		if p.Token == nil {
			return ErrLoginRequired
		}
		if p.Session != c.session || p.ServerURL != c.endpoint {
			return ErrProfileChanged
		}
		force := rejected != "" && p.Token.AccessToken == rejected
		if !force && time.Now().Add(30*time.Second).Before(p.Token.Expiry) {
			access = p.Token.AccessToken
			return nil
		}
		if p.Token.RefreshToken == "" {
			if !force && time.Now().Before(p.Token.Expiry) {
				access = p.Token.AccessToken
				return nil
			}
			return ErrLoginRequired
		}
		token, err := refreshToken(ctx, p, c.client)
		if errors.Is(err, ErrLoginRequired) {
			p.Token = nil
			if saveErr := c.store.save(c.name, p); saveErr != nil {
				return saveErr
			}
			return err
		}
		if err != nil {
			return err
		}
		p.Token = token
		if err := c.store.save(c.name, p); err != nil {
			return err
		}
		access = token.AccessToken
		return nil
	})
	return access, err
}

func refreshToken(ctx context.Context, p *profile, c *http.Client) (*oauth2.Token, error) {
	form := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {p.Token.RefreshToken}, "client_id": {p.ClientID}, "resource": {p.ServerURL}}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, p.TokenURL, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		return nil, errors.New("cannot refresh credentials; identity service is unavailable")
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil || len(data) > 1<<20 {
		return nil, errors.New("invalid token refresh response")
	}
	var value struct {
		AccessToken  string      `json:"access_token"`
		RefreshToken string      `json:"refresh_token"`
		TokenType    string      `json:"token_type"`
		ExpiresIn    json.Number `json:"expires_in"`
		Error        string      `json:"error"`
	}
	if json.Unmarshal(data, &value) != nil {
		return nil, errors.New("invalid token refresh response")
	}
	if value.Error == "invalid_grant" || value.Error == "invalid_client" {
		return nil, ErrLoginRequired
	}
	if resp.StatusCode != http.StatusOK || value.Error != "" {
		return nil, errors.New("token refresh failed; existing credentials were preserved")
	}
	token := &oauth2.Token{AccessToken: value.AccessToken, RefreshToken: value.RefreshToken, TokenType: value.TokenType}
	if seconds, err := strconv.ParseInt(string(value.ExpiresIn), 10, 64); err == nil && seconds > 0 && seconds <= 315360000 {
		token.Expiry = time.Now().Add(time.Duration(seconds) * time.Second)
	}
	if token.RefreshToken == "" {
		token.RefreshToken = p.Token.RefreshToken
	}
	if err := prepareToken(token); err != nil {
		return nil, err
	}
	return token, nil
}
