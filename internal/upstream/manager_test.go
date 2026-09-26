package upstream

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SamuelSupe/mcphub/v2/internal/config"
	"github.com/SamuelSupe/mcphub/v2/internal/configstore"
	"golang.org/x/oauth2"
)

// CI runs against real KV v2 so CAS, deletion, and AppRole behavior stay covered.
func testManager(t *testing.T) *Manager {
	t.Helper()
	address := os.Getenv("MCPHUB_TEST_VAULT_ADDR")
	if address == "" {
		t.Skip("MCPHUB_TEST_VAULT_ADDR is not set")
	}
	s, err := configstore.Open(t.Context(), filepath.Join(t.TempDir(), "config.db"), make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	m, err := New(&config.VaultConfig{Address: address, TokenEnv: "MCPHUB_TEST_VAULT_TOKEN", AllowInsecureHTTP: true, Prefix: "test/" + rand.Text()}, s)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	return m
}

func TestVaultKVAndAppRole(t *testing.T) {
	m := testManager(t)
	ctx := t.Context()
	v := m.Vault
	mount := "test-" + strings.ToLower(rand.Text())
	root := os.Getenv("MCPHUB_TEST_VAULT_TOKEN")
	if err := v.request(ctx, "POST", "sys/auth/"+mount, root, map[string]string{"type": "approle"}, nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = v.request(context.Background(), "DELETE", "sys/auth/"+mount, root, nil, nil) })
	policy := fmt.Sprintf(`path "%s/data/%s/*" { capabilities = ["create", "update", "read"] }
path "%s/metadata/%s/*" { capabilities = ["delete"] }`, v.cfg.Mount, v.cfg.Prefix, v.cfg.Mount, v.cfg.Prefix)
	if err := v.request(ctx, "PUT", "sys/policies/acl/"+mount, root, map[string]string{"policy": policy}, nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = v.request(context.Background(), "DELETE", "sys/policies/acl/"+mount, root, nil, nil) })
	if err := v.request(ctx, "POST", "auth/"+mount+"/role/hub", root, map[string]any{"token_policies": []string{mount}, "token_ttl": "5m"}, nil); err != nil {
		t.Fatal(err)
	}
	var role struct {
		Data struct {
			ID string `json:"role_id"`
		} `json:"data"`
	}
	var secret struct {
		Data struct {
			ID string `json:"secret_id"`
		} `json:"data"`
	}
	if err := v.request(ctx, "GET", "auth/"+mount+"/role/hub/role-id", root, nil, &role); err != nil {
		t.Fatal(err)
	}
	if err := v.request(ctx, "POST", "auth/"+mount+"/role/hub/secret-id", root, map[string]string{}, &secret); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MCPHUB_TEST_ROLE", role.Data.ID)
	t.Setenv("MCPHUB_TEST_SECRET", secret.Data.ID)
	cfg := v.cfg
	cfg.TokenEnv, cfg.RoleIDEnv, cfg.SecretIDEnv, cfg.AuthMount = "", "MCPHUB_TEST_ROLE", "MCPHUB_TEST_SECRET", mount
	v, err := NewVault(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close()
	if err := v.Check(ctx); err != nil {
		t.Fatal(err)
	}
	path := cfg.Prefix + "/shared"
	t.Cleanup(func() { _ = v.Delete(context.Background(), path) })
	if err := v.Write(ctx, path, map[string]string{"token": "test-only-value"}, 0); err != nil {
		t.Fatal(err)
	}
	var values map[string]string
	version, err := v.Read(ctx, path, &values)
	if err != nil || version != 1 || values["token"] != "test-only-value" {
		t.Fatal("KV v2 read did not round trip", err)
	}
	if err := v.Write(ctx, path, values, 0); err == nil {
		t.Fatal("KV CAS allowed overwrite")
	}
	if err := v.Write(ctx, path, map[string]string{"token": "rotated-test-value"}, version); err != nil {
		t.Fatal(err)
	}
	if err := v.Delete(ctx, path); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Read(ctx, path, &values); !errors.Is(err, ErrConnect) {
		t.Fatal("deleted credential remained readable", err)
	}
}

type testTransport func(*http.Request) (*http.Response, error)

func (f testTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestRefreshRotationAndRevocation(t *testing.T) {
	m := testManager(t)
	var refreshes atomic.Int32
	var failure atomic.Int32
	var issuer *httptest.Server
	issuer = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			_ = json.NewEncoder(w).Encode(map[string]any{"issuer": issuer.URL, "authorization_endpoint": issuer.URL + "/authorize", "token_endpoint": issuer.URL + "/token", "jwks_uri": issuer.URL + "/keys", "code_challenge_methods_supported": []string{"S256"}, "token_endpoint_auth_methods_supported": []string{"none"}})
		case "/token":
			if r.ParseForm() != nil || r.Form.Get("resource") != "https://endpoint.example/mcp" || r.Form.Get("client_id") != "hub" || r.Form.Get("grant_type") != "refresh_token" {
				t.Error("refresh lost resource or client binding")
				w.WriteHeader(400)
				return
			}
			if failure.Load() == 1 {
				w.WriteHeader(503)
				_, _ = w.Write([]byte(`{"error":"temporarily_unavailable"}`))
				return
			}
			if failure.Load() == 2 {
				w.WriteHeader(400)
				_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
				return
			}
			n := refreshes.Add(1)
			if r.Form.Get("refresh_token") != fmt.Sprintf("refresh-%d", n-1) {
				t.Error("refresh token consumed twice")
				w.WriteHeader(400)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": fmt.Sprintf("access-%d", n), "refresh_token": fmt.Sprintf("refresh-%d", n), "token_type": "Bearer", "expires_in": 3600, "scope": "read"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer issuer.Close()
	m.HTTP = issuer.Client()
	e := Endpoint{ID: "personal", UID: "uid-one", URL: "https://endpoint.example/mcp", Credentials: &config.CredentialConfig{Mode: "personal", Header: "Authorization", Scheme: "Bearer", OAuth: &config.PersonalOAuth{Issuer: issuer.URL, ClientID: "hub", Scopes: []string{"read"}}}}
	b, err := m.Connect(t.Context(), e, configstore.CredentialBinding{Issuer: "https://login.example", Subject: "alice"}, Secret{Token: &oauth2.Token{AccessToken: "access-0", RefreshToken: "refresh-0", Expiry: time.Now().Add(-time.Second)}, Scopes: []string{"read"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Vault.Delete(context.Background(), m.path(b.ID)) })
	authorize := m.Authorize(e, b)
	failure.Store(1)
	if _, err := authorize(t.Context()); !errors.Is(err, ErrUnavailable) {
		t.Fatal("transient failure was not preserved", err)
	}
	var saved Secret
	if _, err := m.Vault.Read(t.Context(), m.path(b.ID), &saved); err != nil || saved.Invalid || saved.Token.RefreshToken != "refresh-0" {
		t.Fatal("temporary failure damaged credentials", err)
	}
	failure.Store(0)
	base := m.Vault.client.Transport
	var rejectWrite atomic.Bool
	rejectWrite.Store(true)
	m.Vault.client.Transport = testTransport(func(r *http.Request) (*http.Response, error) {
		if r.Method == "POST" && strings.Contains(r.URL.Path, "/data/") && rejectWrite.CompareAndSwap(true, false) {
			return nil, errors.New("test write outage")
		}
		return base.RoundTrip(r)
	})
	if _, err := authorize(t.Context()); !errors.Is(err, ErrUnavailable) {
		t.Fatal("failed rotation persistence admitted a call", err)
	}
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			h, err := authorize(t.Context())
			if err != nil || h.Get("Authorization") != "Bearer access-1" {
				t.Error("concurrent rotation failed", err)
			}
		})
	}
	wg.Wait()
	if refreshes.Load() != 1 {
		t.Fatal("concurrent requests consumed refresh token repeatedly")
	}
	version, err := m.Vault.Read(t.Context(), m.path(b.ID), &saved)
	if err != nil || saved.Token.RefreshToken != "refresh-1" {
		t.Fatal("rotated refresh token was not saved", err)
	}
	saved.Token.Expiry = time.Now().Add(-time.Second)
	if err := m.Vault.Write(t.Context(), m.path(b.ID), saved, version); err != nil {
		t.Fatal(err)
	}
	failure.Store(2)
	if _, err := authorize(t.Context()); !errors.Is(err, ErrReconnect) {
		t.Fatal("invalid refresh was not rejected", err)
	}
	if _, err := m.Vault.Read(t.Context(), m.path(b.ID), &saved); err != nil || !saved.Invalid {
		t.Fatal("invalid refresh was not retained", err)
	}
	m.Vault.client.Transport = testTransport(func(r *http.Request) (*http.Response, error) {
		if r.Method == "DELETE" {
			return nil, errors.New("test cleanup outage")
		}
		return base.RoundTrip(r)
	})
	if err := m.Disconnect(t.Context(), e, b.Issuer, b.Subject, b.Revision); !errors.Is(err, ErrUnavailable) {
		t.Fatal("cleanup outage not reported", err)
	}
	if _, err := authorize(t.Context()); !errors.Is(err, ErrConnect) {
		t.Fatal("disconnected credential remained usable", err)
	}
	if _, err := m.Vault.Read(t.Context(), m.path(b.ID), &saved); err != nil {
		t.Fatal("outage test lost retained secret", err)
	}
	m.Vault.client.Transport = base
	if err := m.Maintain(t.Context()); err != nil {
		t.Fatal("queued cleanup failed", err)
	}
	if _, err := m.Vault.Read(t.Context(), m.path(b.ID), &saved); !errors.Is(err, ErrConnect) {
		t.Fatal("queued cleanup retained credential", err)
	}
	if _, err := m.Connect(t.Context(), e, b, saved); !errors.Is(err, ErrConflict) {
		t.Fatal("delayed callback resurrected a disconnected account", err)
	}
}
