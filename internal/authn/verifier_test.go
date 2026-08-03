package authn

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	mcpauth "github.com/modelcontextprotocol/go-sdk/auth"
)

func TestManagerVerifiesOIDCClaimsAndScopes(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate signing key: %v", err)
	}

	var issuer *httptest.Server
	issuer = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/.well-known/openid-configuration":
			writeTestJSON(t, w, map[string]any{
				"issuer":                                issuer.URL,
				"authorization_endpoint":                issuer.URL + "/authorize",
				"token_endpoint":                        issuer.URL + "/token",
				"jwks_uri":                              issuer.URL + "/jwks",
				"response_types_supported":              []string{"code"},
				"subject_types_supported":               []string{"public"},
				"id_token_signing_alg_values_supported": []string{"RS256"},
			})
		case "/jwks":
			writeTestJSON(t, w, map[string]any{"keys": []any{rsaJWK(&key.PublicKey, "test-key")}})
		default:
			http.NotFound(w, req)
		}
	}))
	defer issuer.Close()

	manager := &Manager{
		issuer:   issuer.URL,
		audience: "https://hub.example.com/mcp",
		logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		client:   issuer.Client(),
	}
	if err := manager.refresh(context.Background()); err != nil {
		t.Fatalf("refresh OIDC discovery: %v", err)
	}

	now := time.Now()
	validClaims := map[string]any{
		"iss":   issuer.URL,
		"aud":   "https://hub.example.com/mcp",
		"sub":   "user-123",
		"exp":   now.Add(5 * time.Minute).Unix(),
		"nbf":   now.Add(-time.Minute).Unix(),
		"scope": "mcp:alpha mcp:shared",
		"scp":   []string{"mcp:beta", "mcp:shared"},
	}
	validToken := signJWT(t, key, "test-key", validClaims)
	info, err := manager.Verify(context.Background(), validToken, nil)
	if err != nil {
		t.Fatalf("Verify(valid token): %v", err)
	}
	if info.UserID != "user-123" {
		t.Fatalf("subject = %q", info.UserID)
	}
	if want := []string{"mcp:alpha", "mcp:beta", "mcp:shared"}; !slices.Equal(info.Scopes, want) {
		t.Fatalf("scopes = %v, want %v", info.Scopes, want)
	}
	t.Run("audience array containing public URL", func(t *testing.T) {
		audienceArrayClaims := withClaim(validClaims, "aud", []string{"https://other.example.com/mcp", "https://hub.example.com/mcp"})
		if _, err := manager.Verify(context.Background(), signJWT(t, key, "test-key", audienceArrayClaims), nil); err != nil {
			t.Fatalf("Verify(audience array containing public URL): %v", err)
		}
	})

	otherKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate alternate key: %v", err)
	}
	tests := []struct {
		name   string
		key    *rsa.PrivateKey
		claims map[string]any
	}{
		{"wrong issuer", key, withClaim(validClaims, "iss", "https://wrong.example.com")},
		{"wrong audience", key, withClaim(validClaims, "aud", "https://other.example.com/mcp")},
		{"wrong multiple audiences", key, withClaim(validClaims, "aud", []string{"https://other.example.com/mcp", "https://another.example.com/mcp"})},
		{"expired", key, withClaim(validClaims, "exp", now.Add(-2*time.Minute).Unix())},
		{"not active", key, withClaim(validClaims, "nbf", now.Add(2*time.Minute).Unix())},
		{"missing subject", key, withClaim(validClaims, "sub", "")},
		{"wrong signature", otherKey, validClaims},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			token := signJWT(t, tt.key, "test-key", tt.claims)
			if _, err := manager.Verify(context.Background(), token, nil); err == nil || !isInvalidToken(err) {
				t.Fatalf("Verify() error = %v, want invalid token", err)
			}
		})
	}
}

func TestManagerRequiresExactGoogleIssuer(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate signing key: %v", err)
	}

	const (
		configuredIssuer = "https://accounts.google.com"
		audience         = "https://hub.example.com/mcp"
	)
	verifier := oidc.NewVerifier(configuredIssuer, &oidc.StaticKeySet{PublicKeys: []crypto.PublicKey{&key.PublicKey}}, &oidc.Config{
		ClientID:        audience,
		SkipExpiryCheck: true,
	})
	manager := &Manager{
		issuer:   configuredIssuer,
		audience: audience,
		logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	manager.verifier.Store(verifier)
	manager.ready.Store(true)

	claims := map[string]any{
		"iss": configuredIssuer,
		"aud": audience,
		"sub": "user-123",
		"exp": time.Now().Add(5 * time.Minute).Unix(),
	}
	t.Run("exact issuer accepted", func(t *testing.T) {
		if _, err := manager.Verify(context.Background(), signJWT(t, key, "test-key", claims), nil); err != nil {
			t.Fatalf("Verify(exact issuer): %v", err)
		}
	})

	t.Run("Google scheme-less issuer rejected by manager", func(t *testing.T) {
		token := signJWT(t, key, "test-key", withClaim(claims, "iss", "accounts.google.com"))
		if _, err := verifier.Verify(context.Background(), token); err != nil {
			t.Fatalf("upstream verifier rejected Google compatibility issuer: %v", err)
		}
		if _, err := manager.Verify(context.Background(), token, nil); err == nil || !isInvalidToken(err) {
			t.Fatalf("Verify(scheme-less Google issuer) error = %v, want invalid token", err)
		}
	})
}

func TestTokenClaimsRejectsAmbiguousScopeShapes(t *testing.T) {
	for _, claims := range []tokenClaims{
		{Scope: json.RawMessage(`["mcp:alpha"]`)},
		{Scope: json.RawMessage(`{"unexpected":true}`)},
		{Scope: json.RawMessage(`"mcp:alpha\tmcp:beta"`)},
		{SCP: json.RawMessage(`"mcp:alpha\nmcp:beta"`)},
		{SCP: json.RawMessage(`{"unexpected":true}`)},
	} {
		if _, err := claims.scopes(); err == nil {
			t.Fatalf("scopes accepted ambiguous claims: %#v", claims)
		}
	}
}

func TestOIDCDiscoveryResponseSizeIsBounded(t *testing.T) {
	issuer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, strings.Repeat("x", maximumOIDCResponseBytes+1))
	}))
	defer issuer.Close()

	manager := &Manager{
		issuer:   issuer.URL,
		audience: "https://hub.example.com/mcp",
		logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		client: &http.Client{
			Transport: &boundedResponseTransport{base: issuer.Client().Transport, maximum: maximumOIDCResponseBytes},
			Timeout:   5 * time.Second,
		},
	}
	if err := manager.refresh(context.Background()); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("refresh error = %v, want bounded response rejection", err)
	}
}

func TestOIDCRefreshRequiresReachableJWKS(t *testing.T) {
	var issuer *httptest.Server
	issuer = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/.well-known/openid-configuration":
			writeTestJSON(t, w, map[string]any{
				"issuer":                                issuer.URL,
				"authorization_endpoint":                issuer.URL + "/authorize",
				"token_endpoint":                        issuer.URL + "/token",
				"jwks_uri":                              issuer.URL + "/jwks",
				"response_types_supported":              []string{"code"},
				"subject_types_supported":               []string{"public"},
				"id_token_signing_alg_values_supported": []string{"RS256"},
			})
		case "/jwks":
			http.Error(w, "internal details must not be logged", http.StatusServiceUnavailable)
		default:
			http.NotFound(w, req)
		}
	}))
	defer issuer.Close()

	manager := &Manager{
		issuer:   issuer.URL,
		audience: "https://hub.example.com/mcp",
		logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		client: &http.Client{
			Transport: &boundedResponseTransport{base: issuer.Client().Transport, maximum: maximumOIDCResponseBytes},
			Timeout:   5 * time.Second,
		},
	}
	if err := manager.refresh(context.Background()); err == nil || manager.Ready() {
		t.Fatalf("refresh error = %v, ready = %v; want unavailable verifier", err, manager.Ready())
	}
}

func TestProbeJWKSRequiresValidPublicKey(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate test JWKS key: %v", err)
	}

	validKey := rsaJWK(&key.PublicKey, "valid-key")
	validKey["key_ops"] = []string{"verify"}
	useEncKey := rsaJWK(&key.PublicKey, "use-enc")
	useEncKey["use"] = "enc"
	keyOpsSignKey := rsaJWK(&key.PublicKey, "key-ops-sign")
	keyOpsSignKey["key_ops"] = []string{"sign"}
	rsaOAEPKey := rsaJWK(&key.PublicKey, "rsa-oaep")
	rsaOAEPKey["alg"] = "RSA-OAEP"
	malformedKey := map[string]any{"kty": "RSA", "n": "not-base64", "e": "AQAB"}

	tests := []struct {
		name              string
		keys              []map[string]any
		signingAlgorithms []string
		wantErr           bool
	}{
		{name: "missing key material", keys: []map[string]any{{}}, signingAlgorithms: []string{"RS256"}, wantErr: true},
		{name: "symmetric key", keys: []map[string]any{{"kty": "oct", "k": "AQID"}}, signingAlgorithms: []string{"RS256"}, wantErr: true},
		{name: "encryption use", keys: []map[string]any{useEncKey}, signingAlgorithms: []string{"RS256"}, wantErr: true},
		{name: "sign-only key operations", keys: []map[string]any{keyOpsSignKey}, signingAlgorithms: []string{"RS256"}, wantErr: true},
		{name: "RSA encryption algorithm", keys: []map[string]any{rsaOAEPKey}, signingAlgorithms: []string{"RS256"}, wantErr: true},
		{name: "invalid key does not mask valid signing key", keys: []map[string]any{malformedKey, validKey}, signingAlgorithms: []string{"RS256"}, wantErr: false},
		{name: "discovery algorithm mismatch", keys: []map[string]any{validKey}, signingAlgorithms: []string{"ES256"}, wantErr: true},
		{name: "discovery algorithms omitted defaults to RSA", keys: []map[string]any{validKey}, signingAlgorithms: nil, wantErr: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payload, err := json.Marshal(map[string]any{"keys": tt.keys})
			if err != nil {
				t.Fatalf("marshal JWKS: %v", err)
			}
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write(payload)
			}))
			defer server.Close()

			err = probeJWKS(context.Background(), server.URL, tt.signingAlgorithms, server.Client())
			if tt.wantErr && err == nil {
				t.Fatal("probeJWKS accepted a JWKS without a usable public signing key")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("probeJWKS rejected JWKS with a usable public signing key: %v", err)
			}
		})
	}
}

func isInvalidToken(err error) bool {
	return errors.Is(err, mcpauth.ErrInvalidToken)
}

func withClaim(source map[string]any, name string, value any) map[string]any {
	copyClaims := make(map[string]any, len(source))
	for key, current := range source {
		copyClaims[key] = current
	}
	copyClaims[name] = value
	return copyClaims
}

func signJWT(t *testing.T, key *rsa.PrivateKey, kid string, claims map[string]any) string {
	t.Helper()
	header, err := json.Marshal(map[string]any{"alg": "RS256", "kid": kid, "typ": "JWT"})
	if err != nil {
		t.Fatalf("marshal JWT header: %v", err)
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("marshal JWT claims: %v", err)
	}
	unsigned := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(payload)
	digest := sha256.Sum256([]byte(unsigned))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		t.Fatalf("sign JWT: %v", err)
	}
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(signature)
}

func rsaJWK(key *rsa.PublicKey, kid string) map[string]any {
	return map[string]any{
		"kty": "RSA",
		"use": "sig",
		"alg": "RS256",
		"kid": kid,
		"n":   base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
		"e":   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
	}
}

func writeTestJSON(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Errorf("encode test response: %v", err)
	}
}
