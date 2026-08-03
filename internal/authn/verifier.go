package authn

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/go-jose/go-jose/v4"
	mcpauth "github.com/modelcontextprotocol/go-sdk/auth"
)

const clockSkew = 30 * time.Second

const maximumOIDCResponseBytes = 1 << 20

type Manager struct {
	issuer   string
	audience string
	logger   *slog.Logger
	client   *http.Client
	verifier atomic.Pointer[oidc.IDTokenVerifier]
	ready    atomic.Bool
}

func NewManager(issuer, audience string, logger *slog.Logger) *Manager {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	return &Manager{
		issuer:   issuer,
		audience: audience,
		logger:   logger,
		client: &http.Client{
			Transport: &boundedResponseTransport{base: transport, maximum: maximumOIDCResponseBytes},
			Timeout:   10 * time.Second,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

func (m *Manager) Run(ctx context.Context) {
	delay := time.Second
	for {
		err := m.refresh(ctx)
		if err == nil {
			delay = 6 * time.Hour
		} else if m.ready.Load() {
			m.logger.Warn("OIDC discovery refresh failed; retaining last-known-good verifier", "error_type", fmt.Sprintf("%T", err))
			delay = 5 * time.Minute
		} else {
			m.logger.Error("OIDC discovery failed", "error_type", fmt.Sprintf("%T", err), "retry_in", delay)
			delay = min(delay*2, 30*time.Second)
		}

		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

type boundedResponseTransport struct {
	base    http.RoundTripper
	maximum int64
}

func (t *boundedResponseTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	if resp.ContentLength > t.maximum {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("OIDC response exceeds %d bytes", t.maximum)
	}
	resp.Body = &boundedResponseBody{body: resp.Body, remaining: t.maximum, maximum: t.maximum}
	return resp, nil
}

type boundedResponseBody struct {
	body      io.ReadCloser
	remaining int64
	maximum   int64
}

func (b *boundedResponseBody) Read(p []byte) (int, error) {
	if b.remaining > 0 {
		if int64(len(p)) > b.remaining {
			p = p[:b.remaining]
		}
		n, err := b.body.Read(p)
		b.remaining -= int64(n)
		return n, err
	}
	var extra [1]byte
	n, err := b.body.Read(extra[:])
	if n > 0 {
		return 0, fmt.Errorf("OIDC response exceeds %d bytes", b.maximum)
	}
	return 0, err
}

func (b *boundedResponseBody) Close() error {
	return b.body.Close()
}

func (m *Manager) refresh(ctx context.Context) error {
	requestCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	requestCtx = oidc.ClientContext(requestCtx, m.client)
	provider, err := oidc.NewProvider(requestCtx, m.issuer)
	if err != nil {
		return err
	}
	var metadata struct {
		JWKSURI           string   `json:"jwks_uri"`
		SigningAlgorithms []string `json:"id_token_signing_alg_values_supported"`
	}
	if err := provider.Claims(&metadata); err != nil {
		return fmt.Errorf("read OIDC provider metadata: %w", err)
	}
	if err := probeJWKS(requestCtx, metadata.JWKSURI, metadata.SigningAlgorithms, m.client); err != nil {
		return err
	}
	verifier := provider.VerifierContext(requestCtx, &oidc.Config{
		ClientID:        m.audience,
		SkipExpiryCheck: true,
	})
	m.verifier.Store(verifier)
	m.ready.Store(true)
	m.logger.Info("OIDC verifier ready", "issuer", m.issuer)
	return nil
}

func probeJWKS(ctx context.Context, rawURL string, signingAlgorithms []string, client *http.Client) error {
	u, err := url.Parse(rawURL)
	if err != nil || !u.IsAbs() || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" {
		return fmt.Errorf("OIDC jwks_uri must be an absolute HTTPS URL without user information or a fragment")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return fmt.Errorf("create JWKS request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("fetch JWKS: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("fetch JWKS: HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read JWKS: %w", err)
	}
	var keySet struct {
		Keys []json.RawMessage `json:"keys"`
	}
	if err := json.Unmarshal(body, &keySet); err != nil {
		return fmt.Errorf("decode JWKS: %w", err)
	}
	for _, rawKey := range keySet.Keys {
		if verificationKeyUsable(rawKey, signingAlgorithms) {
			return nil
		}
	}
	return fmt.Errorf("JWKS contains no usable public verification keys")
}

func verificationKeyUsable(raw json.RawMessage, signingAlgorithms []string) bool {
	var key jose.JSONWebKey
	if err := json.Unmarshal(raw, &key); err != nil || !key.Valid() || !key.IsPublic() {
		return false
	}
	var metadata struct {
		KeyOps []string `json:"key_ops"`
	}
	if err := json.Unmarshal(raw, &metadata); err != nil {
		return false
	}
	if key.Use != "" && key.Use != "sig" {
		return false
	}
	if len(metadata.KeyOps) > 0 && !slices.Contains(metadata.KeyOps, "verify") {
		return false
	}
	if len(signingAlgorithms) == 0 {
		signingAlgorithms = []string{"RS256"}
	}
	for _, algorithm := range signingAlgorithms {
		if key.Algorithm != "" && key.Algorithm != algorithm {
			continue
		}
		if verificationAlgorithmMatches(key.Key, algorithm) {
			return true
		}
	}
	return false
}

func verificationAlgorithmMatches(key any, algorithm string) bool {
	switch key.(type) {
	case *rsa.PublicKey:
		switch algorithm {
		case "RS256", "RS384", "RS512", "PS256", "PS384", "PS512":
			return true
		}
	case *ecdsa.PublicKey:
		ecKey := key.(*ecdsa.PublicKey)
		if ecKey.Curve == nil || ecKey.Curve.Params() == nil {
			return false
		}
		switch algorithm {
		case "ES256":
			return ecKey.Curve.Params().Name == "P-256"
		case "ES384":
			return ecKey.Curve.Params().Name == "P-384"
		case "ES512":
			return ecKey.Curve.Params().Name == "P-521"
		}
	case ed25519.PublicKey:
		return algorithm == "EdDSA"
	}
	return false
}

func (m *Manager) Ready() bool {
	return m.ready.Load()
}

func (m *Manager) Verify(ctx context.Context, rawToken string, _ *http.Request) (*mcpauth.TokenInfo, error) {
	verifier := m.verifier.Load()
	if verifier == nil {
		return nil, fmt.Errorf("OIDC verifier is unavailable")
	}
	token, err := verifier.Verify(ctx, rawToken)
	if err != nil {
		return nil, fmt.Errorf("%w: token verification failed", mcpauth.ErrInvalidToken)
	}
	var claims tokenClaims
	if err := token.Claims(&claims); err != nil {
		return nil, fmt.Errorf("%w: invalid claims", mcpauth.ErrInvalidToken)
	}
	if token.Issuer != m.issuer {
		return nil, fmt.Errorf("%w: token issuer mismatch", mcpauth.ErrInvalidToken)
	}
	if claims.Subject == "" || claims.ExpiresAt == 0 {
		return nil, fmt.Errorf("%w: required claims are missing", mcpauth.ErrInvalidToken)
	}
	now := time.Now()
	expiresAt := time.Unix(claims.ExpiresAt, 0)
	if now.After(expiresAt.Add(clockSkew)) {
		return nil, fmt.Errorf("%w: token expired", mcpauth.ErrInvalidToken)
	}
	if claims.NotBefore != 0 && now.Add(clockSkew).Before(time.Unix(claims.NotBefore, 0)) {
		return nil, fmt.Errorf("%w: token is not active", mcpauth.ErrInvalidToken)
	}
	scopes, err := claims.scopes()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", mcpauth.ErrInvalidToken, err)
	}
	return &mcpauth.TokenInfo{
		Scopes:     scopes,
		Expiration: expiresAt,
		UserID:     claims.Subject,
		Extra:      map[string]any{"issuer": m.issuer},
	}, nil
}

type tokenClaims struct {
	Subject   string          `json:"sub"`
	ExpiresAt int64           `json:"exp"`
	NotBefore int64           `json:"nbf"`
	Scope     json.RawMessage `json:"scope"`
	SCP       json.RawMessage `json:"scp"`
}

func (c tokenClaims) scopes() ([]string, error) {
	set := make(map[string]struct{})
	addString := func(value, claim string) error {
		for _, char := range value {
			if unicode.IsSpace(char) && char != ' ' {
				return fmt.Errorf("%s must use spaces as delimiters", claim)
			}
		}
		for scope := range strings.FieldsSeq(value) {
			set[scope] = struct{}{}
		}
		return nil
	}
	if len(c.Scope) > 0 && string(c.Scope) != "null" {
		var single string
		if err := json.Unmarshal(c.Scope, &single); err != nil {
			return nil, errors.New("scope must be a space-delimited string")
		}
		if err := addString(single, "scope"); err != nil {
			return nil, err
		}
	}
	if len(c.SCP) > 0 && string(c.SCP) != "null" {
		var single string
		if err := json.Unmarshal(c.SCP, &single); err == nil {
			if err := addString(single, "scp"); err != nil {
				return nil, err
			}
		} else {
			var list []string
			if err := json.Unmarshal(c.SCP, &list); err != nil {
				return nil, errors.New("scp must be a string or string array")
			}
			for _, scope := range list {
				if scope == "" || strings.ContainsAny(scope, " \t\r\n") {
					return nil, errors.New("scp contains an invalid value")
				}
				set[scope] = struct{}{}
			}
		}
	}
	result := make([]string, 0, len(set))
	for scope := range set {
		result = append(result, scope)
	}
	slices.Sort(result)
	return result, nil
}
