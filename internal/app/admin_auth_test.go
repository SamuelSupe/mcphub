package app

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"golang.org/x/oauth2"

	"github.com/SamuelSupe/mcphub/v2/internal/authn"
	"github.com/SamuelSupe/mcphub/v2/internal/client"
)

type adminGrant struct{ scope, resource, challenge, redirect, nonce, subject string }
type adminLoginFixture struct {
	app            *App
	server, issuer *httptest.Server
	client         *http.Client
	signer         jose.Signer
	mu             sync.Mutex
	codes, refresh map[string]adminGrant
	refreshCalls   atomic.Int32
	refreshFailure atomic.Bool
	wrongAudience  atomic.Bool
	denyScope      atomic.Bool
	stepUpFailure  atomic.Value
	loginSubject   atomic.Value
}

func newAdminLoginFixture(t *testing.T) *adminLoginFixture {
	t.Helper()
	f := &adminLoginFixture{app: newAdminTestApp(t), codes: map[string]adminGrant{}, refresh: map[string]adminGrant{}}
	f.stepUpFailure.Store("")
	f.loginSubject.Store("administrator-123")
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f.signer, err = jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key}, (&jose.SignerOptions{}).WithHeader("kid", "admin-test"))
	if err != nil {
		t.Fatal(err)
	}
	f.issuer = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration", "/.well-known/oauth-authorization-server":
			writeJSON(w, 200, map[string]any{"issuer": f.issuer.URL, "authorization_endpoint": f.issuer.URL + "/authorize", "token_endpoint": f.issuer.URL + "/token", "jwks_uri": f.issuer.URL + "/jwks", "code_challenge_methods_supported": []string{"S256"}, "response_types_supported": []string{"code"}, "subject_types_supported": []string{"public"}, "id_token_signing_alg_values_supported": []string{"RS256"}, "token_endpoint_auth_methods_supported": []string{"none"}, "scopes_supported": []string{"mcphub:admin", "mcphub:approve", "mcphub:security", "openid", "offline_access"}, "authorization_response_iss_parameter_supported": true})
		case "/jwks":
			writeJSON(w, 200, map[string]any{"keys": []any{jose.JSONWebKey{Key: &key.PublicKey, KeyID: "admin-test", Algorithm: "RS256", Use: "sig"}}})
		case "/authorize":
			q := r.URL.Query()
			if q.Get("client_id") != "admin-cli" || q.Get("code_challenge_method") != "S256" || q.Get("resource") != f.server.URL {
				http.Error(w, "invalid authorization", 400)
				return
			}
			code := rand.Text()
			f.mu.Lock()
			f.codes[code] = adminGrant{scope: q.Get("scope"), resource: q.Get("resource"), challenge: q.Get("code_challenge"), redirect: q.Get("redirect_uri"), nonce: q.Get("nonce"), subject: f.loginSubject.Load().(string)}
			f.mu.Unlock()
			u, _ := url.Parse(q.Get("redirect_uri"))
			u.RawQuery = url.Values{"code": {code}, "state": {q.Get("state")}, "iss": {f.issuer.URL}}.Encode()
			http.Redirect(w, r, u.String(), 302)
		case "/token":
			f.token(w, r)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.issuer.Close)
	f.server = httptest.NewUnstartedServer(nil)
	f.server.StartTLS()
	t.Cleanup(f.server.Close)
	cfg := f.app.currentConfig()
	cfg.Auth.Issuer = f.issuer.URL
	cfg.Admin.Mode = "remote"
	cfg.Admin.PublicURL = f.server.URL
	cfg.Admin.ClientID = "admin-cli"
	cfg.Admin.RequiredScopes = []string{"mcphub:admin"}
	f.client = f.issuer.Client()
	f.client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	old := http.DefaultTransport
	http.DefaultTransport = f.client.Transport
	manager := authn.NewManager(f.issuer.URL, f.server.URL, f.app.logger)
	f.app.adminAuth = newAdminAuthorization(cfg.Admin, f.issuer.URL, manager)
	http.DefaultTransport = old
	go manager.Run(f.app.ctx)
	f.server.Config.Handler = f.app.adminHandler()
	deadline := time.Now().Add(5 * time.Second)
	for !manager.Ready() {
		if time.Now().After(deadline) {
			t.Fatal("admin verifier did not become ready")
		}
		time.Sleep(10 * time.Millisecond)
	}
	return f
}

func (f *adminLoginFixture) signed(scope, audience string) string {
	return f.signedSubject(scope, audience, "administrator-123")
}

func (f *adminLoginFixture) signedSubject(scope, audience, subject string) string {
	token, err := jwt.Signed(f.signer).Claims(map[string]any{"iss": f.issuer.URL, "aud": audience, "sub": subject, "scope": scope, "exp": time.Now().Add(time.Hour).Unix()}).Serialize()
	if err != nil {
		panic(err)
	}
	return token
}

func (f *adminLoginFixture) token(w http.ResponseWriter, r *http.Request) {
	if r.ParseForm() != nil || r.Form.Get("client_id") != "admin-cli" || r.Form.Get("resource") != f.server.URL {
		http.Error(w, "invalid token request", 400)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	var grant adminGrant
	var ok bool
	switch r.Form.Get("grant_type") {
	case "authorization_code":
		grant, ok = f.codes[r.Form.Get("code")]
		delete(f.codes, r.Form.Get("code"))
		ok = ok && grant.redirect == r.Form.Get("redirect_uri") && grant.challenge == oauth2.S256ChallengeFromVerifier(r.Form.Get("code_verifier"))
	case "refresh_token":
		f.refreshCalls.Add(1)
		if f.refreshFailure.Load() {
			writeJSON(w, 503, map[string]string{"error": "temporarily_unavailable"})
			return
		}
		grant, ok = f.refresh[r.Form.Get("refresh_token")]
		delete(f.refresh, r.Form.Get("refresh_token"))
	}
	if !ok || grant.resource != r.Form.Get("resource") {
		writeJSON(w, 400, map[string]string{"error": "invalid_grant"})
		return
	}
	scope, audience := grant.scope, grant.resource
	if f.denyScope.Load() {
		scope = "mcp:read"
	}
	if f.wrongAudience.Load() {
		audience = f.app.currentConfig().Server.PublicURL
	}
	refresh := rand.Text()
	f.refresh[refresh] = grant
	expires := 3600
	if r.Form.Get("grant_type") == "authorization_code" {
		expires = 2
	}
	response := map[string]any{"access_token": f.signedSubject(scope, audience, grant.subject), "token_type": "Bearer", "refresh_token": refresh, "expires_in": expires, "scope": scope}
	if grant.nonce != "" {
		claims := map[string]any{"iss": f.issuer.URL, "sub": grant.subject, "aud": "admin-cli", "iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix(), "auth_time": time.Now().Unix(), "nonce": grant.nonce, "acr": "urn:mcphub:test:mfa"}
		switch f.stepUpFailure.Load().(string) {
		case "acr":
			claims["acr"] = "password"
		case "old":
			claims["auth_time"] = time.Now().Add(-time.Hour).Unix()
		case "nonce":
			claims["nonce"] = "wrong"
		case "subject":
			claims["sub"] = "another-user"
		case "audience":
			claims["aud"] = "another-client"
		}
		raw, err := jwt.Signed(f.signer).Claims(claims).Serialize()
		if err != nil {
			panic("invalid test ID token")
		}
		response["id_token"] = raw
	}
	writeJSON(w, 200, response)
}

func (f *adminLoginFixture) request(t *testing.T, c *http.Client, method, path, token, csrf string, body []byte) *http.Response {
	t.Helper()
	req, _ := http.NewRequest(method, f.server.URL+path, bytes.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if csrf != "" {
		req.Header.Set("X-MCPHub-CSRF", csrf)
		req.Header.Set("Origin", f.server.URL)
	}
	if len(body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func TestRemoteAdminAuthorizationAndCLI(t *testing.T) {
	f := newAdminLoginFixture(t)
	for _, route := range []struct{ method, path string }{{"GET", "/api/v1/tool-policies?endpoint=alpha"}, {"POST", "/api/v1/access-check"}, {"GET", "/api/v1/client-grants"}, {"GET", "/api/v1/requests"}} {
		for _, scope := range []string{"mcp:read", "mcphub:approve"} {
			resp := f.request(t, f.client, route.method, route.path, f.signed(scope, f.server.URL), "", []byte(`{}`))
			if resp.StatusCode != http.StatusForbidden {
				t.Fatalf("%s exposed administrator policies to %s: %d", route.path, scope, resp.StatusCode)
			}
		}
	}
	for _, tc := range []struct {
		token  string
		status int
	}{
		{"", 401}, {f.signed("mcphub:admin", f.app.currentConfig().Server.PublicURL), 401}, {f.signed("mcp:read", f.server.URL), 403}, {f.signed("mcphub:admin", f.server.URL), 200},
	} {
		resp := f.request(t, f.client, "GET", "/api/v1/backends", tc.token, "", nil)
		if resp.StatusCode != tc.status {
			t.Fatalf("authorization status %d want %d", resp.StatusCode, tc.status)
		}
	}
	store := &client.Store{Dir: filepath.Join(t.TempDir(), "profiles")}
	login := func() error {
		return client.Login(t.Context(), store, client.LoginOptions{Admin: true, ServerURL: f.server.URL, ClientID: "admin-cli", Profile: "ops", HTTPClient: f.client, OpenBrowser: func(raw string) error {
			go func() {
				resp, err := f.client.Get(raw)
				if err != nil {
					t.Error(err)
					return
				}
				target := resp.Header.Get("Location")
				resp.Body.Close()
				resp, err = f.client.Get(target)
				if err != nil {
					t.Error(err)
					return
				}
				resp.Body.Close()
			}()
			return nil
		}})
	}
	if err := login(); err != nil {
		t.Fatal(err)
	}
	if err := client.Connect(t.Context(), store, "ops", client.ConnectOptions{}); err == nil {
		t.Fatal("admin profile accepted by MCP connector")
	}
	body := []byte(`{"id":"remote","url":"https://unused.example/mcp","enabled":false,"required_scopes":["mcp:read"]}`)
	var output bytes.Buffer
	if err := client.AdminRequest(t.Context(), store, "ops", client.AdminRequestOptions{Method: "POST", Path: "/backends", Body: body, Output: &output, HTTPClient: f.client}); err != nil {
		t.Fatal(err)
	}
	if f.refreshCalls.Load() != 1 {
		t.Fatalf("CLI refresh count: %d", f.refreshCalls.Load())
	}
	var created backendView
	if err := json.Unmarshal(output.Bytes(), &created); err != nil || created.Enabled {
		t.Fatalf("create: %s, %v", output.Bytes(), err)
	}
	events, err := f.app.store.Events(t.Context(), 10)
	if err != nil || len(events) != 1 || events[0].Actor != "administrator-123" {
		t.Fatalf("audit identity: %#v %v", events, err)
	}
	f.wrongAudience.Store(true)
	if err := login(); err == nil {
		t.Fatal("wrong audience login succeeded")
	}
	f.wrongAudience.Store(false)
	f.denyScope.Store(true)
	if err := login(); err == nil {
		t.Fatal("ordinary user login succeeded")
	}
	f.denyScope.Store(false)
	if err := client.AdminRequest(t.Context(), store, "ops", client.AdminRequestOptions{Method: "GET", Path: "/overview", HTTPClient: f.client}); err != nil {
		t.Fatalf("failed login replaced good profile: %v", err)
	}
	if err := store.Logout(t.Context(), "ops"); err != nil {
		t.Fatal(err)
	}
	if err := client.AdminRequest(t.Context(), store, "ops", client.AdminRequestOptions{Method: "GET", Path: "/overview", HTTPClient: f.client}); err == nil {
		t.Fatal("logout did not clear administrator credentials")
	}
}

func TestRemoteAdminBrowserSessionRefreshAndCSRF(t *testing.T) {
	f := newAdminLoginFixture(t)
	c := *f.client
	c.Jar, _ = cookiejar.New(nil)
	resp := f.request(t, &c, "GET", "/auth/login", "", "", nil)
	if resp.StatusCode != 302 {
		t.Fatal("login redirect missing")
	}
	authURL := resp.Header.Get("Location")
	resp, err := c.Get(authURL)
	if err != nil {
		t.Fatal(err)
	}
	callback := resp.Header.Get("Location")
	resp.Body.Close()
	u, _ := url.Parse(callback)
	q := u.Query()
	q.Set("state", "wrong")
	u.RawQuery = q.Encode()
	resp, err = c.Get(u.String())
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 400 {
		t.Fatal("bad state accepted")
	}
	resp.Body.Close()
	resp, err = c.Get(callback)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 303 || resp.Header.Get("Location") != "/" {
		t.Fatalf("callback: %d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	for _, cookie := range resp.Cookies() {
		if cookie.Name == adminSessionCookie && (!cookie.Secure || !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode) {
			t.Fatal("insecure session cookie")
		}
	}
	resp.Body.Close()
	resp = f.request(t, &c, "GET", "/auth/session", "", "", nil)
	var session struct {
		Authenticated bool
		CSRF          string
		Subject       string
	}
	if err := json.NewDecoder(resp.Body).Decode(&session); err != nil || !session.Authenticated || session.Subject != "administrator-123" {
		t.Fatalf("session: %#v %v", session, err)
	}
	csrf := session.CSRF
	if resp := f.request(t, &c, "POST", "/api/v1/backends", "", "", []byte(`{}`)); resp.StatusCode != 403 {
		t.Fatal("cookie mutation accepted without CSRF")
	}
	var live *adminSession
	f.app.adminAuth.mu.Lock()
	for _, s := range f.app.adminAuth.sessions {
		live = s
	}
	f.app.adminAuth.mu.Unlock()
	live.mu.Lock()
	live.token.Expiry = time.Now().Add(-time.Minute)
	oldRefresh := live.token.RefreshToken
	live.mu.Unlock()
	f.refreshFailure.Store(true)
	if resp := f.request(t, &c, "GET", "/api/v1/overview", "", "", nil); resp.StatusCode != 503 {
		t.Fatal("transient refresh failure not reported")
	}
	live.mu.Lock()
	if live.token == nil || live.token.RefreshToken != oldRefresh {
		t.Fatal("transient refresh failure destroyed session")
	}
	live.mu.Unlock()
	f.refreshFailure.Store(false)
	var wg sync.WaitGroup
	for range 5 {
		wg.Go(func() {
			resp := f.request(t, &c, "GET", "/api/v1/overview", "", "", nil)
			if resp.StatusCode != 200 {
				t.Errorf("concurrent refreshed request: %d", resp.StatusCode)
			}
		})
	}
	wg.Wait()
	if f.refreshCalls.Load() != 2 {
		t.Fatalf("refresh rotation was not serialized: %d", f.refreshCalls.Load())
	}
	live.mu.Lock()
	if live.token.RefreshToken == oldRefresh {
		t.Fatal("rotated token not saved")
	}
	live.mu.Unlock()
	live.mu.Lock()
	live.token.Expiry = time.Now().Add(-time.Minute)
	live.token.RefreshToken = "revoked-refresh-grant"
	live.mu.Unlock()
	if resp := f.request(t, &c, "POST", "/auth/logout", "", csrf, nil); resp.StatusCode != 204 {
		t.Fatalf("logout: %d", resp.StatusCode)
	}
	if resp := f.request(t, &c, "GET", "/api/v1/overview", "", "", nil); resp.StatusCode != 401 {
		t.Fatal("logged out session still authorized")
	}
	if f.refreshCalls.Load() != 2 {
		t.Fatal("logout attempted refresh instead of clearing the session")
	}
	for _, reason := range []string{"issuer", "missing issuer", "denied"} {
		t.Run(reason, func(t *testing.T) {
			start := f.request(t, &c, "GET", "/auth/login", "", "", nil)
			authorized, err := c.Get(start.Header.Get("Location"))
			if err != nil {
				t.Fatal(err)
			}
			u, _ := url.Parse(authorized.Header.Get("Location"))
			authorized.Body.Close()
			q := u.Query()
			switch reason {
			case "issuer":
				q.Set("iss", "https://other.example")
			case "missing issuer":
				q.Del("iss")
			case "denied":
				q.Set("error", "access_denied")
			}
			u.RawQuery = q.Encode()
			result, err := c.Get(u.String())
			if err != nil {
				t.Fatal(err)
			}
			defer result.Body.Close()
			if result.StatusCode != 303 || result.Header.Get("Location") != "/?login_error=denied" {
				t.Fatal("invalid authorization callback accepted")
			}
			if resp := f.request(t, &c, "GET", "/api/v1/overview", "", "", nil); resp.StatusCode != 401 {
				t.Fatal("invalid callback created a session")
			}
		})
	}
}
