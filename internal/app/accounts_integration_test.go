package app

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SamuelSupe/mcphub/v2/internal/config"
	"github.com/SamuelSupe/mcphub/v2/internal/configstore"
	"github.com/SamuelSupe/mcphub/v2/internal/upstream"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type accountFixture struct {
	*adminLoginFixture
	endpoint                                  config.BackendConfig
	hub                                       *httptest.Server
	servers                                   map[string]*mcp.Server
	writes                                    atomic.Int32
	slow, cancelled, subscribed, unsubscribed chan string
}

func newAccountFixture(t *testing.T, oauth bool) *accountFixture {
	t.Helper()
	if os.Getenv("MCPHUB_TEST_VAULT_ADDR") == "" {
		t.Skip("MCPHUB_TEST_VAULT_ADDR is not set")
	}
	f := &accountFixture{adminLoginFixture: newAdminLoginFixture(t), servers: map[string]*mcp.Server{}, slow: make(chan string, 8), cancelled: make(chan string, 8), subscribed: make(chan string, 8), unsubscribed: make(chan string, 8)}
	previousTransport := http.DefaultTransport
	http.DefaultTransport = f.client.Transport
	t.Cleanup(func() { f.app.Close(); http.DefaultTransport = previousTransport })
	f.app.auth = f.app.adminAuth.verifier
	for _, owner := range []string{"discovery", "alice", "bob", "upstream-alice"} {
		s := mcp.NewServer(&mcp.Implementation{Name: "personal", Version: "1"}, &mcp.ServerOptions{
			SubscribeHandler:   func(_ context.Context, _ *mcp.SubscribeRequest) error { f.subscribed <- owner; return nil },
			UnsubscribeHandler: func(_ context.Context, _ *mcp.UnsubscribeRequest) error { f.unsubscribed <- owner; return nil },
		})
		for _, name := range []string{"read", "write", "link", "slow"} {
			s.AddTool(&mcp.Tool{Name: name, InputSchema: map[string]any{"type": "object"}}, func(ctx context.Context, r *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				if name == "write" {
					f.writes.Add(1)
				}
				if name == "slow" {
					f.slow <- owner
					<-ctx.Done()
					f.cancelled <- owner
					return nil, ctx.Err()
				}
				if name == "link" {
					return &mcp.CallToolResult{Content: []mcp.Content{&mcp.ResourceLink{URI: "private://issued-" + owner, Name: "private"}}}, nil
				}
				return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: owner}}}, nil
			})
		}
		s.AddResource(&mcp.Resource{Name: "Personal resource", URI: "memory://own"}, func(_ context.Context, r *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
			return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: r.Params.URI, Text: owner}}}, nil
		})
		s.AddResourceTemplate(&mcp.ResourceTemplate{Name: "Issued", URITemplate: "private://{id}"}, func(_ context.Context, r *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
			return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: r.Params.URI, Text: owner}}}, nil
		})
		s.AddPrompt(&mcp.Prompt{Name: "own"}, func(context.Context, *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
			return &mcp.GetPromptResult{Messages: []*mcp.PromptMessage{{Role: "user", Content: &mcp.TextContent{Text: owner}}}}, nil
		})
		f.servers[owner] = s
	}
	backend := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		owner := ""
		if r.Header.Get("Authorization") == "Bearer discovery" {
			owner = "discovery"
		} else {
			info, err := f.app.adminAuth.verifier.Verify(r.Context(), strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), r)
			if err == nil {
				owner = info.UserID
			}
		}
		return f.servers[owner]
	}, &mcp.StreamableHTTPOptions{Stateless: true, PropagateRequestCancellation: true})
	admin := f.app.adminHandler()
	f.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/client-auth/") {
			f.app.serveClientAuthorization(w, r, f.app.currentRuntime())
			return
		}
		if r.URL.Path == "/" {
			backend.ServeHTTP(w, r)
			return
		}
		admin.ServeHTTP(w, r)
	})
	cfg := *f.app.currentConfig()
	cfg.Server.RequestTimeout.Duration = 5 * time.Second
	cfg.Server.RefreshInterval.Duration = time.Hour
	cfg.ClientAuthorization = config.ClientAuthorizationConfig{Enabled: true, ClientID: "admin-cli"}
	cfg.Vault = &config.VaultConfig{Address: os.Getenv("MCPHUB_TEST_VAULT_ADDR"), TokenEnv: "MCPHUB_TEST_VAULT_TOKEN", Prefix: "test/" + rand.Text(), AllowInsecureHTTP: true}
	m, err := upstream.New(cfg.Vault, f.app.store)
	if err != nil {
		t.Fatal(err)
	}
	f.app.credentials = m
	discovery := cfg.Vault.Prefix + "/discovery"
	if err := m.Vault.Write(t.Context(), discovery, map[string]string{"token": "discovery"}, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Vault.Delete(context.Background(), discovery) })
	c := &config.CredentialConfig{Mode: "personal", Field: "token", Header: "Authorization", Scheme: "Bearer", DiscoveryPath: discovery}
	if oauth {
		c.OAuth = &config.PersonalOAuth{Issuer: f.issuer.URL, ClientID: "admin-cli", Scopes: []string{"mcp:alpha"}}
	}
	r, err := f.app.store.Create(t.Context(), configstore.Record{Enabled: true, Config: config.BackendConfig{ID: "alpha", URL: f.server.URL, Required: true, RequireClientGrant: true, Credentials: c, RequiredScopes: []string{"mcp:alpha"}, PublishedTools: []string{"read", "write", "link", "slow"}, ToolRules: []config.ToolRule{{Match: "*", Effect: "read"}, {Match: "write", Effect: "write"}}, RequestTimeout: config.Duration{Duration: 5 * time.Second}}})
	if err != nil {
		t.Fatal(err)
	}
	f.endpoint = r.Config
	if err := f.app.replaceRuntimeLocked(configWithRecords(&cfg, []configstore.Record{r})); err != nil {
		t.Fatal(err)
	}
	f.app.userAuth = newAdminAuthorization(config.AdminConfig{PublicURL: f.server.URL, ClientID: "admin-cli", RequiredScopes: []string{"mcp:alpha"}}, f.issuer.URL, f.app.adminAuth.verifier)
	f.app.userAuth.userPortal, f.app.userAuth.resource = true, f.server.URL
	f.hub = httptest.NewServer(f.app)
	t.Cleanup(f.hub.Close)
	t.Cleanup(func() {
		for _, owner := range []string{"alice", "bob"} {
			b, _ := m.Store.CredentialBinding(context.Background(), f.issuer.URL, owner, f.endpoint.EndpointUID)
			if b.Connected {
				_ = m.Disconnect(context.Background(), accountEndpoint(f.endpoint), f.issuer.URL, owner, b.Revision)
			}
		}
	})
	return f
}

func (f *accountFixture) browser(t *testing.T, owner string) (*http.Client, string) {
	t.Helper()
	f.loginSubject.Store(owner)
	c := *f.client
	c.Jar, _ = cookiejar.New(nil)
	resp := f.request(t, &c, "GET", "/client-auth/auth/login", "", "", nil)
	for range 2 {
		target := resp.Header.Get("Location")
		resp.Body.Close()
		var err error
		resp, err = c.Get(target)
		if err != nil {
			t.Fatal(err)
		}
	}
	if resp.StatusCode != 303 {
		t.Fatalf("portal login: %d", resp.StatusCode)
	}
	resp.Body.Close()
	resp = f.request(t, &c, "GET", "/client-auth/auth/session", "", "", nil)
	var session struct{ CSRF string }
	if json.NewDecoder(resp.Body).Decode(&session) != nil || session.CSRF == "" {
		t.Fatal("portal session missing")
	}
	return &c, session.CSRF
}

func (f *accountFixture) connectClient(t *testing.T, owner string, options *mcp.ClientOptions) (*mcp.ClientSession, configstore.ClientGrant) {
	t.Helper()
	g := configstore.ClientGrant{GrantBinding: configstore.GrantBinding{ClientID: "ci_" + rand.Text()}, Issuer: f.issuer.URL, Subject: owner, Resource: f.app.currentConfig().Server.PublicURL, EndpointID: "alpha", ClientName: "Editor", AllowedScopes: []string{"mcp:alpha"}, AllowedTools: []string{"read", "write", "link", "slow"}, AllowWriteRequests: true, Capabilities: configstore.GrantCapabilities{Tools: true, Prompts: true, Resources: true, Subscriptions: true}}
	if err := f.app.currentRuntime().hub.PrepareClientGrant(&g, g.AllowedScopes); err != nil {
		t.Fatal(err)
	}
	var err error
	g.EndpointUID, g.EndpointPolicy, err = f.app.store.ClientEndpointPolicy(t.Context(), "alpha")
	if err != nil {
		t.Fatal(err)
	}
	g, exchange, _, err := f.app.store.CreateClientGrant(t.Context(), g, "", time.Hour, 8*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.app.store.DecideClientGrant(t.Context(), g.GrantID, g.Issuer, g.Subject, true); err != nil {
		t.Fatal(err)
	}
	g, credential, err := f.app.store.ExchangeClientGrant(t.Context(), g.GrantID, g.Issuer, g.Subject, exchange)
	if err != nil {
		t.Fatal(err)
	}
	c := mcp.NewClient(&mcp.Implementation{Name: "personal-test", Version: "1"}, options)
	s, err := c.Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: f.hub.URL + "/mcp", HTTPClient: &http.Client{Transport: bearerTransport{token: f.signedSubject("mcp:alpha", f.server.URL, owner), grant: credential, base: http.DefaultTransport}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, g
}

func TestPersonalCredentialsIsolateMCPAndRevoke(t *testing.T) {
	f := newAccountFixture(t, false)
	ctx := t.Context()
	missing, _ := f.connectClient(t, "alice", nil)
	result, err := missing.CallTool(ctx, &mcp.CallToolParams{Name: "alpha.write", Arguments: map[string]any{}})
	if err != nil || !result.IsError {
		t.Fatal("missing personal account admitted write", err)
	}
	data, _ := json.Marshal(result.StructuredContent)
	if !bytes.Contains(data, []byte(`"action":"connect_account"`)) || f.writes.Load() != 0 {
		t.Fatal("missing account did not provide safe connection action")
	}
	for _, owner := range []string{"alice", "bob"} {
		browser, csrf := f.browser(t, owner)
		body, _ := json.Marshal(map[string]any{"token": f.signedSubject("mcp:alpha", f.server.URL, owner), "account": owner, "revision": 0})
		resp := f.request(t, browser, "POST", "/client-auth/api/accounts/alpha/token", "", "", body)
		if resp.StatusCode != 403 {
			t.Fatal("token connection accepted without CSRF")
		}
		resp = f.request(t, browser, "POST", "/client-auth/api/accounts/alpha/token", "", csrf, body)
		if resp.StatusCode != 200 {
			t.Fatalf("connect personal token: %d", resp.StatusCode)
		}
	}
	notifications := make(chan string, 4)
	alice, aliceGrant := f.connectClient(t, "alice", &mcp.ClientOptions{ResourceUpdatedHandler: func(context.Context, *mcp.ResourceUpdatedNotificationRequest) { notifications <- "alice" }})
	bob, bobGrant := f.connectClient(t, "bob", &mcp.ClientOptions{ResourceUpdatedHandler: func(context.Context, *mcp.ResourceUpdatedNotificationRequest) { notifications <- "bob" }})
	for owner, client := range map[string]*mcp.ClientSession{"alice": alice, "bob": bob} {
		result, err := client.CallTool(ctx, &mcp.CallToolParams{Name: "alpha.read", Arguments: map[string]any{}})
		if err != nil || result.IsError || result.Content[0].(*mcp.TextContent).Text != owner {
			t.Fatal("tool used another user's credential", err)
		}
		resources, err := client.ListResources(ctx, nil)
		if err != nil || len(resources.Resources) != 1 {
			t.Fatal("personal resources missing", err)
		}
		read, err := client.ReadResource(ctx, &mcp.ReadResourceParams{URI: resources.Resources[0].URI})
		if err != nil || read.Contents[0].Text != owner {
			t.Fatal("resource identity leak", err)
		}
		prompt, err := client.GetPrompt(ctx, &mcp.GetPromptParams{Name: "alpha.own"})
		if err != nil || prompt.Messages[0].Content.(*mcp.TextContent).Text != owner {
			t.Fatal("prompt identity leak", err)
		}
		if err := client.Subscribe(ctx, &mcp.SubscribeParams{URI: resources.Resources[0].URI}); err != nil {
			t.Fatal(err)
		}
		select {
		case got := <-f.subscribed:
			if got != owner {
				t.Fatal("subscription used another account")
			}
		case <-time.After(2 * time.Second):
			t.Fatal("subscription not forwarded")
		}
	}
	if err := f.servers["alice"].ResourceUpdated(ctx, &mcp.ResourceUpdatedNotificationParams{URI: "memory://own"}); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-notifications:
		if got != "alice" {
			t.Fatal("personal notification leaked")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("personal notification lost")
	}
	select {
	case <-notifications:
		t.Fatal("duplicate or cross-user notification")
	case <-time.After(50 * time.Millisecond):
	}
	result, err = alice.CallTool(ctx, &mcp.CallToolParams{Name: "alpha.write", Arguments: map[string]any{}})
	if err != nil || !result.IsError || f.writes.Load() != 0 {
		t.Fatal("personal credentials bypassed write approval", err)
	}
	data, _ = json.Marshal(result.StructuredContent)
	var pending struct {
		ID string `json:"approval_id"`
	}
	if json.Unmarshal(data, &pending) != nil || pending.ID == "" {
		t.Fatal("write did not enter approval flow")
	}
	if err := f.app.store.DecideApproval(ctx, pending.ID, "approved", "independent-reviewer", configstore.ApprovalDetail{Reason: "Reviewed the requested personal account operation."}); err != nil {
		t.Fatal(err)
	}
	result, err = alice.CallTool(ctx, &mcp.CallToolParams{Name: "mcphub_resume_approval", Arguments: map[string]any{"approval_id": pending.ID}})
	if err != nil || result.IsError || result.Content[0].(*mcp.TextContent).Text != "alice" || f.writes.Load() != 1 {
		t.Fatal("approved write did not use personal credential", err)
	}
	resources, err := bob.ListResources(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := bob.Unsubscribe(ctx, &mcp.UnsubscribeParams{URI: resources.Resources[0].URI}); err != nil {
		t.Fatal(err)
	}
	select {
	case owner := <-f.unsubscribed:
		if owner != "bob" {
			t.Fatal("unsubscribed wrong account")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("personal unsubscribe did not reach endpoint")
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = alice.CallTool(ctx, &mcp.CallToolParams{Name: "alpha.slow", Arguments: map[string]any{}})
	}()
	select {
	case <-f.slow:
	case <-time.After(2 * time.Second):
		t.Fatal("slow call did not start")
	}
	b, _ := f.app.store.CredentialBinding(ctx, f.issuer.URL, "alice", f.endpoint.EndpointUID)
	if err := f.app.credentials.Disconnect(ctx, accountEndpoint(f.endpoint), f.issuer.URL, "alice", b.Revision); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("revocation did not cancel admitted request")
	}
	select {
	case owner := <-f.cancelled:
		if owner != "alice" {
			t.Fatal("cancelled wrong user's call")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancellation did not reach endpoint")
	}
	if err := f.app.store.ValidateClientGrant(ctx, aliceGrant); err == nil {
		t.Fatal("revoked credential left old grant active")
	}
	if err := f.app.store.ValidateClientGrant(ctx, bobGrant); err != nil {
		t.Fatal("revocation affected another user", err)
	}
	result, err = bob.CallTool(ctx, &mcp.CallToolParams{Name: "alpha.read", Arguments: map[string]any{}})
	if err != nil || result.IsError {
		t.Fatal("other user stopped working", err)
	}
}

func TestPersonalOIDCBrowserBindingAndValidation(t *testing.T) {
	f := newAccountFixture(t, true)
	browser, csrf := f.browser(t, "alice")
	start := func() *url.URL {
		t.Helper()
		resp := f.request(t, browser, "POST", "/client-auth/api/accounts/alpha/login", "", csrf, nil)
		var data struct {
			URL string `json:"authorization_url"`
		}
		if resp.StatusCode != 200 || json.NewDecoder(resp.Body).Decode(&data) != nil {
			t.Fatal("cannot start personal OAuth")
		}
		f.loginSubject.Store("upstream-alice")
		resp, err := browser.Get(data.URL)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		u, err := url.Parse(resp.Header.Get("Location"))
		if err != nil || u.Query().Get("code") == "" {
			t.Fatal("authorization code missing", err)
		}
		return u
	}
	finish := func(u *url.URL) string {
		t.Helper()
		resp, err := browser.Get(u.String())
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		return resp.Header.Get("Location")
	}
	u := start()
	other := *f.client
	other.Jar, _ = cookiejar.New(nil)
	if resp, err := other.Get(u.String()); err != nil || resp.StatusCode != 401 {
		t.Fatal("callback accepted without owner browser", err)
	} else {
		resp.Body.Close()
	}
	if got := finish(u); got != "/client-auth/?connection=connected" {
		t.Fatalf("OAuth callback: %s", got)
	}
	b, err := f.app.store.CredentialBinding(t.Context(), f.issuer.URL, "alice", f.endpoint.EndpointUID)
	if err != nil || !b.Connected || b.AccountID != "upstream-alice" || !b.Renewable {
		t.Fatal("OIDC identity binding missing", err)
	}
	if got := finish(u); got != "/client-auth/?connection=expired" {
		t.Fatal("authorization callback replay accepted")
	}
	for _, mode := range []string{"issuer", "state", "audience", "nonce", "endpoint-audience", "scope", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			u := start()
			q := u.Query()
			switch mode {
			case "issuer":
				q.Set("iss", "https://wrong.example")
			case "state":
				q.Set("state", "invalid")
			case "audience", "nonce":
				f.stepUpFailure.Store(mode)
				defer f.stepUpFailure.Store("")
			case "endpoint-audience":
				f.wrongAudience.Store(true)
				defer f.wrongAudience.Store(false)
			case "scope":
				f.denyScope.Store(true)
				defer f.denyScope.Store(false)
			case "cancel":
				q.Set("error", "access_denied")
				q.Del("code")
			}
			u.RawQuery = q.Encode()
			if got := finish(u); got == "/client-auth/?connection=connected" || got == "/client-auth/?connection=reconnected" {
				t.Fatal("invalid authorization replaced account")
			}
			current, err := f.app.store.CredentialBinding(t.Context(), b.Issuer, b.Subject, b.EndpointUID)
			if err != nil || current.ID != b.ID || current.Revision != b.Revision {
				t.Fatal("failed connection destroyed existing account", err)
			}
		})
	}
	if got := finish(start()); got != "/client-auth/?connection=reconnected" {
		t.Fatalf("account replacement callback: %s", got)
	}
	replacement, err := f.app.store.CredentialBinding(t.Context(), b.Issuer, b.Subject, b.EndpointUID)
	if err != nil || replacement.ID == b.ID || replacement.Revision != b.Revision+1 {
		t.Fatal("account replacement did not advance credential binding", err)
	}
	b = replacement
	resp := f.request(t, browser, "GET", "/client-auth/api/accounts", "", "", nil)
	var accounts struct{ Accounts []accountView }
	if json.NewDecoder(resp.Body).Decode(&accounts) != nil || len(accounts.Accounts) != 1 || !accounts.Accounts[0].Connected {
		t.Fatal("connected account not shown")
	}
	data, _ := json.Marshal(map[string]int64{"revision": b.Revision})
	resp = f.request(t, browser, "POST", fmt.Sprintf("/client-auth/api/accounts/%s/disconnect", b.EndpointUID), "", csrf, data)
	if resp.StatusCode != 200 {
		t.Fatal("browser disconnection failed")
	}
}
