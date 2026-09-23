package client

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/oauth2"

	"github.com/SamuelSupe/mcphub/internal/app"
	"github.com/SamuelSupe/mcphub/internal/config"
)

type authCode struct{ challenge, redirect, resource, scope string }

type loginFixture struct {
	t                         *testing.T
	issuer, hub               *httptest.Server
	backend                   *mcp.Server
	store                     *Store
	client                    *http.Client
	mu                        sync.Mutex
	codes                     map[string]authCode
	refresh                   map[string]string
	signer                    jose.Signer
	refreshCalls              atomic.Int32
	toolCalls                 atomic.Int32
	refreshFailure            atomic.Int32
	wrongAudience             atomic.Bool
	noRefresh                 atomic.Bool
	rejectAccess              atomic.Bool
	slowStarted, slowCanceled chan struct{}
	subscribed, unsubscribed  chan string
}

func newLoginFixture(t *testing.T) *loginFixture {
	t.Helper()
	f := &loginFixture{t: t, store: &Store{Dir: filepath.Join(t.TempDir(), "credentials")}, codes: make(map[string]authCode), refresh: make(map[string]string), slowStarted: make(chan struct{}, 8), slowCanceled: make(chan struct{}, 8), subscribed: make(chan string, 8), unsubscribed: make(chan string, 8)}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f.signer, err = jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key}, (&jose.SignerOptions{}).WithHeader("kid", "login-test"))
	if err != nil {
		t.Fatal(err)
	}
	f.issuer = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/oauth-authorization-server", "/.well-known/openid-configuration":
			writeJSON(t, w, map[string]any{"issuer": f.issuer.URL, "authorization_endpoint": f.issuer.URL + "/authorize", "token_endpoint": f.issuer.URL + "/token", "jwks_uri": f.issuer.URL + "/jwks", "code_challenge_methods_supported": []string{"S256"}, "response_types_supported": []string{"code"}, "subject_types_supported": []string{"public"}, "token_endpoint_auth_methods_supported": []string{"none"}, "id_token_signing_alg_values_supported": []string{"RS256"}, "scopes_supported": []string{"offline_access", "mcp:alpha"}, "authorization_response_iss_parameter_supported": true})
		case "/jwks":
			writeJSON(t, w, map[string]any{"keys": []any{jose.JSONWebKey{Key: &key.PublicKey, KeyID: "login-test", Algorithm: "RS256", Use: "sig"}}})
		case "/authorize":
			q := r.URL.Query()
			if q.Get("client_id") != "mcphub-cli" || q.Get("response_type") != "code" || q.Get("code_challenge_method") != "S256" || q.Get("resource") != f.hub.URL+"/mcp" {
				http.Error(w, "invalid authorization request", 400)
				return
			}
			code := rand.Text()
			f.mu.Lock()
			f.codes[code] = authCode{challenge: q.Get("code_challenge"), redirect: q.Get("redirect_uri"), resource: q.Get("resource"), scope: q.Get("scope")}
			f.mu.Unlock()
			u, err := url.Parse(q.Get("redirect_uri"))
			if err != nil {
				http.Error(w, "invalid redirect", 400)
				return
			}
			values := u.Query()
			values.Set("code", code)
			values.Set("state", q.Get("state"))
			values.Set("iss", f.issuer.URL)
			u.RawQuery = values.Encode()
			http.Redirect(w, r, u.String(), http.StatusFound)
		case "/token":
			f.serveToken(w, r)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.issuer.Close)
	f.client = f.issuer.Client()
	oldTransport := http.DefaultTransport
	http.DefaultTransport = f.client.Transport
	t.Cleanup(func() { http.DefaultTransport = oldTransport })
	f.backend = mcp.NewServer(&mcp.Implementation{Name: "login-backend", Version: "1"}, &mcp.ServerOptions{
		SubscribeHandler:   func(_ context.Context, r *mcp.SubscribeRequest) error { f.subscribed <- r.Params.URI; return nil },
		UnsubscribeHandler: func(_ context.Context, r *mcp.UnsubscribeRequest) error { f.unsubscribed <- r.Params.URI; return nil },
	})
	f.backend.AddTool(&mcp.Tool{Name: "echo", InputSchema: map[string]any{"type": "object"}}, func(ctx context.Context, r *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		f.toolCalls.Add(1)
		if token := r.Params.GetProgressToken(); token != nil {
			_ = r.Session.NotifyProgress(ctx, &mcp.ProgressNotificationParams{ProgressToken: token, Progress: 1, Total: 1})
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "authenticated echo"}}}, nil
	})
	f.backend.AddTool(&mcp.Tool{Name: "slow", InputSchema: map[string]any{"type": "object"}}, func(ctx context.Context, r *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		f.slowStarted <- struct{}{}
		<-ctx.Done()
		f.slowCanceled <- struct{}{}
		return nil, ctx.Err()
	})
	f.backend.AddTool(&mcp.Tool{Name: "admin", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		t.Error("unauthorized tool executed")
		return &mcp.CallToolResult{}, nil
	})
	f.backend.AddResource(&mcp.Resource{Name: "sample", URI: "memory://sample"}, func(_ context.Context, r *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: r.Params.URI, Text: "sample data"}}}, nil
	})
	f.backend.AddPrompt(&mcp.Prompt{Name: "greeting"}, func(context.Context, *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		return &mcp.GetPromptResult{Messages: []*mcp.PromptMessage{{Role: "user", Content: &mcp.TextContent{Text: "hello"}}}}, nil
	})
	backendHTTP := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return f.backend }, &mcp.StreamableHTTPOptions{Stateless: true, PropagateRequestCancellation: true}))
	t.Cleanup(backendHTTP.Close)
	f.hub = httptest.NewUnstartedServer(nil)
	f.hub.StartTLS()
	t.Cleanup(f.hub.Close)
	cfg := &config.Config{Server: config.ServerConfig{Listen: "127.0.0.1:0", PublicURL: f.hub.URL + "/mcp", PageSize: 1, RequestTimeout: config.Duration{Duration: 10 * time.Second}, DrainTimeout: config.Duration{Duration: time.Second}, RefreshInterval: config.Duration{Duration: time.Hour}, CatalogTTL: config.Duration{Duration: time.Second}, MaxRequestBodyBytes: 4 << 20}, Auth: config.AuthConfig{Issuer: f.issuer.URL}, Backends: []config.BackendConfig{{ID: "alpha", URL: backendHTTP.URL, Required: true, RequiredScopes: []string{"mcp:alpha"}, ToolRules: []config.ToolRule{{Match: "admin", RequiredScopes: []string{"mcp:admin"}}}, AllowInsecureHTTP: true, RequestTimeout: config.Duration{Duration: 10 * time.Second}}}}
	application, err := app.New(context.Background(), cfg, "", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(application.Close)
	f.hub.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if f.rejectAccess.Load() && r.Header.Get("Authorization") != "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		application.ServeHTTP(w, r)
	})
	deadline := time.Now().Add(10 * time.Second)
	for {
		resp, err := f.client.Get(f.hub.URL + "/readyz")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("MCPHub did not become ready")
		}
		time.Sleep(10 * time.Millisecond)
	}
	return f
}

func writeJSON(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Error(err)
	}
}

func (f *loginFixture) serveToken(w http.ResponseWriter, r *http.Request) {
	if r.ParseForm() != nil {
		http.Error(w, "invalid form", 400)
		return
	}
	if r.Form.Get("client_id") != "mcphub-cli" || r.Form.Get("client_secret") != "" || r.Header.Get("Authorization") != "" || r.Form.Get("resource") != f.hub.URL+"/mcp" {
		http.Error(w, "invalid client or resource", 400)
		return
	}
	var scope string
	f.mu.Lock()
	switch r.Form.Get("grant_type") {
	case "authorization_code":
		code, ok := f.codes[r.Form.Get("code")]
		delete(f.codes, r.Form.Get("code"))
		if !ok || oauth2.S256ChallengeFromVerifier(r.Form.Get("code_verifier")) != code.challenge || r.Form.Get("redirect_uri") != code.redirect {
			f.mu.Unlock()
			w.WriteHeader(400)
			writeJSON(f.t, w, map[string]any{"error": "invalid_grant"})
			return
		}
		scope = code.scope
	case "refresh_token":
		f.refreshCalls.Add(1)
		if status := f.refreshFailure.Load(); status != 0 {
			f.mu.Unlock()
			w.WriteHeader(int(status))
			writeJSON(f.t, w, map[string]any{"error": "temporarily_unavailable"})
			return
		}
		var ok bool
		scope, ok = f.refresh[r.Form.Get("refresh_token")]
		delete(f.refresh, r.Form.Get("refresh_token"))
		if !ok {
			f.mu.Unlock()
			w.WriteHeader(400)
			writeJSON(f.t, w, map[string]any{"error": "invalid_grant"})
			return
		}
	default:
		f.mu.Unlock()
		http.Error(w, "unsupported grant", 400)
		return
	}
	refresh := ""
	if !f.noRefresh.Load() {
		refresh = rand.Text()
		f.refresh[refresh] = scope
	}
	f.mu.Unlock()
	audience := f.hub.URL + "/mcp"
	if f.wrongAudience.Load() {
		audience = "https://different.example/mcp"
	}
	token, err := jwt.Signed(f.signer).Claims(map[string]any{"iss": f.issuer.URL, "aud": audience, "sub": "test-user", "exp": time.Now().Add(time.Hour).Unix(), "scope": scope, "jti": rand.Text()}).Serialize()
	if err != nil {
		f.t.Error(err)
		http.Error(w, "signing failed", 500)
		return
	}
	writeJSON(f.t, w, map[string]any{"access_token": token, "token_type": "Bearer", "refresh_token": refresh, "expires_in": 3600, "scope": scope})
}

func (f *loginFixture) login(ctx context.Context, mutate func(url.Values)) error {
	fetchCallback := func(raw string) error {
		c := *f.client
		c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		resp, err := c.Get(raw)
		if err != nil {
			return err
		}
		resp.Body.Close()
		u, err := url.Parse(resp.Header.Get("Location"))
		if err != nil {
			return err
		}
		q := u.Query()
		if mutate != nil {
			mutate(q)
		}
		u.RawQuery = q.Encode()
		resp, err = f.client.Get(u.String())
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return err
		}
		if len(body) == 0 {
			return errors.New("browser received an empty callback response")
		}
		return nil
	}
	// A real browser runs independently of Login; callback shutdown must allow
	// it to finish reading the response after the code has reached the CLI.
	browserDone := make(chan error, 1)
	opener := func(raw string) error { go func() { browserDone <- fetchCallback(raw) }(); return nil }
	err := Login(ctx, f.store, LoginOptions{Profile: "work", ServerURL: f.hub.URL + "/mcp", ClientID: "mcphub-cli", Scopes: []string{"mcp:alpha"}, HTTPClient: f.client, OpenBrowser: opener})
	select {
	case browserErr := <-browserDone:
		return errors.Join(err, browserErr)
	case <-ctx.Done():
		return errors.Join(err, ctx.Err())
	}
}

func (f *loginFixture) editToken(fn func(*profile)) {
	f.t.Helper()
	if err := f.store.locked(context.Background(), "work", func() error {
		p, err := f.store.load("work")
		if err != nil {
			return err
		}
		fn(p)
		return f.store.save("work", p)
	}); err != nil {
		f.t.Fatal(err)
	}
}

func (f *loginFixture) connect(ctx context.Context, options *mcp.ClientOptions) (*mcp.ClientSession, <-chan error) {
	f.t.Helper()
	local, process := net.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- Connect(ctx, f.store, "work", ConnectOptions{Input: process, Output: process, HTTPClient: f.client})
	}()
	client := mcp.NewClient(&mcp.Implementation{Name: "connector-test", Version: "1"}, options)
	session, err := client.Connect(ctx, &mcp.IOTransport{Reader: local, Writer: local}, nil)
	if err != nil {
		f.t.Fatal(err)
	}
	f.t.Cleanup(func() { session.Close(); local.Close(); process.Close() })
	return session, done
}

func TestBrowserLoginThroughMCPHubAndConnector(t *testing.T) {
	f := newLoginFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := f.login(ctx, nil); err != nil {
		t.Fatal(err)
	}
	status, err := f.store.Status(ctx, "work")
	if err != nil || !status.LoggedIn || !status.CanRefresh {
		t.Fatalf("status=%+v, error=%v", status, err)
	}
	for _, path := range []string{f.store.Dir, filepath.Join(f.store.Dir, "work.json")} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm()&0077 != 0 {
			t.Fatalf("unsafe permissions for %s: %v", path, err)
		}
	}
	progress := make(chan *mcp.ProgressNotificationParams, 4)
	updates := make(chan string, 4)
	session, done := f.connect(ctx, &mcp.ClientOptions{
		ProgressNotificationHandler: func(_ context.Context, r *mcp.ProgressNotificationClientRequest) { progress <- r.Params },
		ResourceUpdatedHandler:      func(_ context.Context, r *mcp.ResourceUpdatedNotificationRequest) { updates <- r.Params.URI },
	})
	listed, err := session.ListTools(ctx, nil)
	if err != nil || len(listed.Tools) != 1 || listed.NextCursor == "" {
		t.Fatalf("paged tools=%+v error=%v", listed, err)
	}
	page, err := session.ListTools(ctx, &mcp.ListToolsParams{Cursor: listed.NextCursor})
	if err != nil || len(page.Tools) != 1 {
		t.Fatalf("second page=%+v error=%v", page, err)
	}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "alpha.echo", Meta: mcp.Meta{"progressToken": "client-progress"}})
	if err != nil || result.Content[0].(*mcp.TextContent).Text != "authenticated echo" {
		t.Fatalf("call=%+v error=%v", result, err)
	}
	select {
	case p := <-progress:
		if p.ProgressToken != "client-progress" {
			t.Fatal("progress token changed")
		}
	case <-ctx.Done():
		t.Fatal("missing progress")
	}
	if _, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "alpha.admin"}); err == nil || !strings.Contains(err.Error(), "mcp:admin") {
		t.Fatalf("scope failure=%v", err)
	}
	resources, err := session.ListResources(ctx, nil)
	if err != nil || len(resources.Resources) != 1 {
		t.Fatalf("resources=%+v error=%v", resources, err)
	}
	uri := resources.Resources[0].URI
	if read, err := session.ReadResource(ctx, &mcp.ReadResourceParams{URI: uri}); err != nil || read.Contents[0].Text != "sample data" {
		t.Fatalf("read=%+v error=%v", read, err)
	}
	if prompt, err := session.GetPrompt(ctx, &mcp.GetPromptParams{Name: "alpha.greeting"}); err != nil || len(prompt.Messages) != 1 {
		t.Fatalf("prompt=%+v error=%v", prompt, err)
	}
	if err := session.Subscribe(ctx, &mcp.SubscribeParams{URI: uri}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-f.subscribed:
	case <-ctx.Done():
		t.Fatal("subscription did not reach backend")
	}
	if err := f.backend.ResourceUpdated(ctx, &mcp.ResourceUpdatedNotificationParams{URI: "memory://sample"}); err != nil {
		t.Fatal(err)
	}
	select {
	case updated := <-updates:
		if updated != uri {
			t.Fatal("resource URI changed in transit")
		}
	case <-ctx.Done():
		t.Fatal("resource update did not reach client")
	}
	if err := session.Unsubscribe(ctx, &mcp.UnsubscribeParams{URI: uri}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-f.unsubscribed:
	case <-ctx.Done():
		t.Fatal("subscription was not cleaned up")
	}
	callCtx, stopCall := context.WithCancel(ctx)
	callDone := make(chan error, 1)
	go func() { _, err := session.CallTool(callCtx, &mcp.CallToolParams{Name: "alpha.slow"}); callDone <- err }()
	select {
	case <-f.slowStarted:
	case <-ctx.Done():
		t.Fatal("slow call did not start")
	}
	if _, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "alpha.echo"}); err != nil {
		t.Fatalf("slow request blocked another call: %v", err)
	}
	stopCall()
	select {
	case <-f.slowCanceled:
	case <-ctx.Done():
		t.Fatal("cancellation did not close the upstream request")
	}
	select {
	case err := <-callDone:
		if err == nil {
			t.Fatal("canceled call succeeded")
		}
	case <-ctx.Done():
		t.Fatal("call did not cancel")
	}
	if _, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "alpha.echo"}); err != nil {
		t.Fatalf("connection unusable after cancellation: %v", err)
	}
	if err := f.store.Logout(ctx, "work"); err != nil {
		t.Fatal(err)
	}
	if _, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "alpha.echo"}); err == nil {
		t.Fatal("logged-out connector still authorized")
	}
	select {
	case err := <-done:
		if !errors.Is(err, ErrLoginRequired) {
			t.Fatalf("connector exit=%v", err)
		}
	case <-ctx.Done():
		t.Fatal("connector did not exit after logout")
	}
}

func TestLoginFailurePreservesCredentials(t *testing.T) {
	f := newLoginFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := f.login(ctx, nil); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(f.store.Dir, "work.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name          string
		mutate        func(url.Values)
		wrongAudience bool
	}{
		{"denied", func(q url.Values) { q.Set("error", "access_denied"); q.Del("code") }, false},
		{"wrong issuer", func(q url.Values) { q.Set("iss", "https://untrusted.example") }, false},
		{"wrong state", func(q url.Values) { q.Set("state", "wrong") }, false},
		{"wrong audience", nil, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			f.wrongAudience.Store(test.wrongAudience)
			attemptCtx, stop := context.WithTimeout(ctx, 700*time.Millisecond)
			defer stop()
			if err := f.login(attemptCtx, test.mutate); err == nil {
				t.Fatal("bad authorization succeeded")
			}
			after, err := os.ReadFile(filepath.Join(f.store.Dir, "work.json"))
			if err != nil || !bytes.Equal(before, after) {
				t.Fatal("failed login replaced existing credentials")
			}
		})
	}
}

func TestRefreshRotationIsolationAndFailure(t *testing.T) {
	f := newLoginFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := f.login(ctx, nil); err != nil {
		t.Fatal(err)
	}
	first, err := bindCredentials(ctx, f.store, "work", f.client)
	if err != nil {
		t.Fatal(err)
	}
	var sessions []*mcp.ClientSession
	for range 3 {
		session, _ := f.connect(ctx, nil)
		sessions = append(sessions, session)
	}
	f.editToken(func(p *profile) { p.Token.Expiry = time.Now().Add(-time.Hour) })
	var wg sync.WaitGroup
	results := make(chan error, len(sessions))
	for _, session := range sessions {
		wg.Go(func() {
			_, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "alpha.echo"})
			results <- err
		})
	}
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := f.refreshCalls.Load(); got != 1 {
		t.Fatalf("concurrent refreshes=%d", got)
	}
	f.editToken(func(p *profile) { p.Token.Expiry = time.Now().Add(-time.Hour) })
	if _, err := first.token(ctx, ""); err != nil {
		t.Fatalf("rotated refresh token not persisted: %v", err)
	}
	f.refreshFailure.Store(503)
	f.editToken(func(p *profile) { p.Token.Expiry = time.Now().Add(-time.Hour) })
	before, _ := os.ReadFile(filepath.Join(f.store.Dir, "work.json"))
	if _, err := first.token(ctx, ""); err == nil {
		t.Fatal("refresh succeeded during outage")
	}
	after, _ := os.ReadFile(filepath.Join(f.store.Dir, "work.json"))
	if !bytes.Equal(before, after) {
		t.Fatal("outage changed credentials")
	}
	f.refreshFailure.Store(0)
	if _, err := first.token(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if err := f.login(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := first.token(ctx, ""); !errors.Is(err, ErrProfileChanged) {
		t.Fatalf("old connector accepted new login: %v", err)
	}
	if _, err := bindCredentials(ctx, f.store, "different", f.client); !errors.Is(err, ErrLoginRequired) {
		t.Fatalf("profile isolation failed: %v", err)
	}
	f.editToken(func(p *profile) { p.Token.Expiry = time.Now().Add(-time.Hour); p.Token.RefreshToken = "revoked" })
	current, _ := bindCredentials(ctx, f.store, "work", f.client)
	if _, err := current.token(ctx, ""); !errors.Is(err, ErrLoginRequired) {
		t.Fatalf("invalid grant=%v", err)
	}
	status, _ := f.store.Status(ctx, "work")
	if status.LoggedIn {
		t.Fatal("invalid grant credentials retained")
	}
}

func TestConnectorLegacyHandshakeAndSingle401Retry(t *testing.T) {
	f := newLoginFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := f.login(ctx, nil); err != nil {
		t.Fatal(err)
	}
	local, process := net.Pipe()
	defer local.Close()
	done := make(chan error, 1)
	go func() {
		done <- Connect(ctx, f.store, "work", ConnectOptions{Input: process, Output: process, HTTPClient: f.client})
	}()
	conn, err := (&mcp.IOTransport{Reader: local, Writer: local}).Connect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	request, _ := jsonrpc.DecodeMessage([]byte(`{"jsonrpc":"2.0","id":"init","method":"initialize","params":{"protocolVersion":"2025-06-18","clientInfo":{"name":"legacy","version":"1"},"capabilities":{}}}`))
	if err := conn.Write(ctx, request); err != nil {
		t.Fatal(err)
	}
	message, err := conn.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	response, ok := message.(*jsonrpc.Response)
	if !ok || response.Error != nil || response.ID.Raw() != "init" {
		t.Fatalf("legacy initialize=%+v", message)
	}
	notify, _ := jsonrpc.DecodeMessage([]byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}`))
	if err := conn.Write(ctx, notify); err != nil {
		t.Fatal(err)
	}
	f.editToken(func(p *profile) { p.Token.AccessToken = "rejected-token" })
	call, _ := jsonrpc.DecodeMessage([]byte(`{"jsonrpc":"2.0","id":42,"method":"tools/call","params":{"name":"alpha.echo"}}`))
	if err := conn.Write(ctx, call); err != nil {
		t.Fatal(err)
	}
	message, err = conn.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	response = message.(*jsonrpc.Response)
	if response.Error != nil {
		t.Fatal(response.Error)
	}
	if f.refreshCalls.Load() != 1 || f.toolCalls.Load() != 1 {
		t.Fatalf("refresh=%d tool executions=%d", f.refreshCalls.Load(), f.toolCalls.Load())
	}
	f.rejectAccess.Store(true)
	if err := conn.Write(ctx, call); err != nil {
		t.Fatal(err)
	}
	message, err = conn.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if message.(*jsonrpc.Response).Error == nil {
		t.Fatal("permanent 401 accepted")
	}
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("401 loop did not terminate")
	}
	if f.refreshCalls.Load() != 2 || f.toolCalls.Load() != 1 {
		t.Fatal("401 retried too many times or executed rejected tool")
	}
}

func TestProfileLockAcrossProcesses(t *testing.T) {
	if directory := os.Getenv("MCPHUB_TEST_LOCK_DIR"); directory != "" {
		store := &Store{Dir: directory}
		if err := store.locked(context.Background(), "work", func() error {
			p, err := store.load("work")
			if err != nil {
				return err
			}
			p.Scopes = append(p.Scopes, "child")
			return store.save("work", p)
		}); err != nil {
			t.Fatal(err)
		}
		return
	}
	store := &Store{Dir: filepath.Join(t.TempDir(), "profiles")}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := store.locked(ctx, "work", func() error {
		return store.save("work", &profile{Version: 1, Session: "session", ServerURL: "https://hub.example/mcp", Issuer: "https://idp.example", TokenURL: "https://idp.example/token", ClientID: "cli"})
	})
	if err != nil {
		t.Fatal(err)
	}
	var commands []*exec.Cmd
	for range 4 {
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestProfileLockAcrossProcesses$")
		cmd.Env = append(os.Environ(), "MCPHUB_TEST_LOCK_DIR="+store.Dir)
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		commands = append(commands, cmd)
	}
	for _, cmd := range commands {
		if err := cmd.Wait(); err != nil {
			t.Fatal(err)
		}
	}
	status, err := store.Status(ctx, "work")
	if err != nil || len(status.Scopes) != 4 {
		t.Fatalf("concurrent profile updates lost: %+v %v", status, err)
	}
}

func TestLoginWithoutRefreshToken(t *testing.T) {
	f := newLoginFixture(t)
	f.noRefresh.Store(true)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := f.login(ctx, nil); err != nil {
		t.Fatal(err)
	}
	status, err := f.store.Status(ctx, "work")
	if err != nil || !status.LoggedIn || status.CanRefresh {
		t.Fatalf("status=%+v error=%v", status, err)
	}
	bound, err := bindCredentials(ctx, f.store, "work", f.client)
	if err != nil {
		t.Fatal(err)
	}
	f.editToken(func(p *profile) { p.Token.Expiry = time.Now().Add(5 * time.Second) })
	if _, err := bound.token(ctx, ""); err != nil {
		t.Fatalf("unexpired token rejected: %v", err)
	}
	f.editToken(func(p *profile) { p.Token.Expiry = time.Now().Add(-time.Second) })
	if _, err := bound.token(ctx, ""); !errors.Is(err, ErrLoginRequired) {
		t.Fatalf("expired token=%v", err)
	}
	if f.refreshCalls.Load() != 0 {
		t.Fatal("attempted refresh without a refresh token")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return fn(r) }

func TestConnectorDoesNotReplayNetworkFailure(t *testing.T) {
	f := newLoginFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := f.login(ctx, nil); err != nil {
		t.Fatal(err)
	}
	base := f.client.Transport
	var attempts atomic.Int32
	copy := *f.client
	copy.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.String() == f.hub.URL+"/mcp" && r.Header.Get("Mcp-Method") == "tools/call" {
			attempts.Add(1)
			return nil, errors.New("lost connection after write: must-not-leak-this-value")
		}
		return base.RoundTrip(r)
	})
	f.client = &copy
	session, _ := f.connect(ctx, nil)
	_, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "alpha.echo"})
	if err == nil || strings.Contains(err.Error(), "must-not-leak-this-value") {
		t.Fatalf("unsafe network failure: %v", err)
	}
	if attempts.Load() != 1 {
		t.Fatalf("operation was retried %d times", attempts.Load())
	}
	if _, err := session.ListTools(ctx, nil); err != nil {
		t.Fatalf("recoverable network failure broke connection: %v", err)
	}
}
