package app

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	mcpauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/SamuelSupe/mcphub/internal/backend"
	"github.com/SamuelSupe/mcphub/internal/config"
	"github.com/SamuelSupe/mcphub/internal/hub"
)

func TestAppEnforcesScopesAndPublishesResourceMetadata(t *testing.T) {
	subscribed := make(chan string, 1)
	unsubscribed := make(chan string, 1)
	protectedToolName := "delete_" + strings.Repeat("x", 121)
	var protectedCalls atomic.Int32
	backendServer := mcp.NewServer(&mcp.Implementation{Name: "backend", Version: "1"}, &mcp.ServerOptions{
		SubscribeHandler: func(_ context.Context, req *mcp.SubscribeRequest) error {
			subscribed <- req.Params.URI
			return nil
		},
		UnsubscribeHandler: func(_ context.Context, req *mcp.UnsubscribeRequest) error {
			unsubscribed <- req.Params.URI
			return nil
		},
	})
	backendServer.AddTool(&mcp.Tool{
		Name:        "echo",
		InputSchema: map[string]any{"type": "object"},
	}, func(_ context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "allowed"}}}, nil
	})
	backendServer.AddTool(&mcp.Tool{
		Name:        protectedToolName,
		InputSchema: map[string]any{"type": "object"},
	}, func(_ context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		protectedCalls.Add(1)
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "protected"}}}, nil
	})
	backendServer.AddResource(&mcp.Resource{Name: "shared", URI: "memory://shared"}, func(_ context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: req.Params.URI, Text: "shared"}}}, nil
	})
	backendHTTP := httptest.NewServer(mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return backendServer },
		&mcp.StreamableHTTPOptions{Stateless: true, PropagateRequestCancellation: true},
	))
	t.Cleanup(func() {
		backendHTTP.CloseClientConnections()
		backendHTTP.Close()
	})

	cfg := &config.Config{
		Server: config.ServerConfig{
			Listen:              ":8080",
			PublicURL:           "https://hub.example.com/mcp",
			PageSize:            100,
			RequestTimeout:      config.Duration{Duration: 5 * time.Second},
			DrainTimeout:        config.Duration{Duration: time.Second},
			RefreshInterval:     config.Duration{Duration: time.Hour},
			CatalogTTL:          config.Duration{Duration: 30 * time.Second},
			MaxRequestBodyBytes: 4 << 20,
			AllowedOrigins:      []string{"https://console.example.com"},
		},
		Auth: config.AuthConfig{Issuer: "https://idp.example.com"},
		Backends: []config.BackendConfig{{
			ID:             "alpha",
			URL:            backendHTTP.URL,
			Required:       true,
			RequiredScopes: []string{"mcp:alpha"},
			ToolRules: []config.ToolRule{{
				Match:          "delete_*",
				RequiredScopes: []string{"mcp:alpha:dangerous"},
			}, {
				Match:          "delete_?*",
				RequiredScopes: []string{"mcp:alpha:audit"},
			}, {
				Match:          "future_*",
				RequiredScopes: []string{"mcp:alpha:dangerous"},
			}},
			RequestTimeout:    config.Duration{Duration: 5 * time.Second},
			AllowInsecureHTTP: true,
		}},
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx, cancel := context.WithCancel(context.Background())
	rt, err := newRuntime(ctx, cfg, logger, false)
	if err != nil {
		cancel()
		t.Fatalf("new runtime: %v", err)
	}
	application := &App{
		ctx:     ctx,
		cancel:  cancel,
		logger:  logger,
		auth:    staticVerifier{},
		runtime: rt,
	}
	t.Cleanup(application.Close)
	hubHTTP := httptest.NewServer(application)
	t.Cleanup(hubHTTP.Close)

	for _, path := range []string{
		"/.well-known/oauth-protected-resource",
		"/.well-known/oauth-protected-resource/mcp",
	} {
		resp := doRequest(t, http.MethodGet, hubHTTP.URL+path, "", "", nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("metadata %s status = %d", path, resp.StatusCode)
		}
		var metadata struct {
			Resource             string   `json:"resource"`
			AuthorizationServers []string `json:"authorization_servers"`
			ScopesSupported      []string `json:"scopes_supported"`
		}
		decodeResponse(t, resp, &metadata)
		if metadata.Resource != cfg.Server.PublicURL || len(metadata.AuthorizationServers) != 1 || metadata.AuthorizationServers[0] != cfg.Auth.Issuer {
			t.Fatalf("metadata = %#v", metadata)
		}
		if got, want := strings.Join(metadata.ScopesSupported, " "), "mcp:alpha mcp:alpha:audit mcp:alpha:dangerous"; got != want {
			t.Fatalf("metadata scopes = %v", metadata.ScopesSupported)
		}
	}

	ready := doRequest(t, http.MethodGet, hubHTTP.URL+"/readyz", "", "", nil)
	if ready.StatusCode != http.StatusOK {
		t.Fatalf("ready status = %d", ready.StatusCode)
	}
	_ = ready.Body.Close()

	callBody := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"alpha.echo","arguments":{}}}`)
	missing := doRequest(t, http.MethodPost, hubHTTP.URL+"/mcp", "", "application/json", callBody)
	if missing.StatusCode != http.StatusUnauthorized || !strings.Contains(missing.Header.Get("WWW-Authenticate"), "resource_metadata=") {
		t.Fatalf("missing token status/challenge = %d, %q", missing.StatusCode, missing.Header.Get("WWW-Authenticate"))
	}
	_ = missing.Body.Close()

	forbidden := doRequest(t, http.MethodPost, hubHTTP.URL+"/mcp", "denied", "application/json", callBody)
	challenge := forbidden.Header.Get("WWW-Authenticate")
	if forbidden.StatusCode != http.StatusForbidden || !strings.Contains(challenge, `error="insufficient_scope"`) || !strings.Contains(challenge, `scope="mcp:alpha"`) {
		t.Fatalf("forbidden status/challenge = %d, %q", forbidden.StatusCode, challenge)
	}
	_ = forbidden.Body.Close()

	deniedSession := connectAppClient(t, ctx, hubHTTP.URL+"/mcp", "denied")
	deniedTools, err := deniedSession.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("list tools without scope: %v", err)
	}
	if len(deniedTools.Tools) != 0 {
		t.Fatalf("tools without scope = %v", deniedTools.Tools)
	}
	_ = deniedSession.Close()

	allowedSession := connectAppClient(t, ctx, hubHTTP.URL+"/mcp", "allowed")
	t.Cleanup(func() { _ = allowedSession.Close() })
	allowedTools, err := allowedSession.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("list tools with scope: %v", err)
	}
	if len(allowedTools.Tools) != 1 || allowedTools.Tools[0].Name != "alpha.echo" {
		t.Fatalf("tools with scope = %v", allowedTools.Tools)
	}
	result, err := allowedSession.CallTool(ctx, &mcp.CallToolParams{Name: "alpha.echo", Arguments: map[string]any{}})
	if err != nil {
		t.Fatalf("call allowed tool: %v", err)
	}
	if text, ok := result.Content[0].(*mcp.TextContent); !ok || text.Text != "allowed" {
		t.Fatalf("allowed tool result = %#v", result.Content)
	}

	protectedListChanged := make(chan struct{}, 1)
	privilegedSession := connectAppClientWithOptions(t, ctx, hubHTTP.URL+"/mcp", "privileged", &mcp.ClientOptions{
		ToolListChangedHandler: func(context.Context, *mcp.ToolListChangedRequest) {
			select {
			case protectedListChanged <- struct{}{}:
			default:
			}
		},
	})
	t.Cleanup(func() { _ = privilegedSession.Close() })
	privilegedTools, err := privilegedSession.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("list tools with tool scope: %v", err)
	}
	if len(privilegedTools.Tools) != 2 {
		t.Fatalf("tools with tool scope = %v, want echo and protected tool", privilegedTools.Tools)
	}
	protectedExposedName := ""
	for _, tool := range privilegedTools.Tools {
		if tool.Name != "alpha.echo" {
			protectedExposedName = tool.Name
		}
	}
	if len(protectedExposedName) != 128 || !strings.HasPrefix(protectedExposedName, "alpha.delete_") {
		t.Fatalf("protected exposed tool name = %q", protectedExposedName)
	}
	protectedBody := []byte(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"` + protectedExposedName + `","arguments":{}}}`)
	toolForbidden := doRequest(t, http.MethodPost, hubHTTP.URL+"/mcp", "allowed", "application/json", protectedBody)
	toolChallenge := toolForbidden.Header.Get("WWW-Authenticate")
	if toolForbidden.StatusCode != http.StatusForbidden || !strings.Contains(toolChallenge, `scope="mcp:alpha:audit mcp:alpha:dangerous"`) {
		t.Fatalf("tool-scope forbidden status/challenge = %d, %q", toolForbidden.StatusCode, toolChallenge)
	}
	_ = toolForbidden.Body.Close()
	if protectedCalls.Load() != 0 {
		t.Fatalf("protected backend calls after tool-scope rejection = %d, want 0", protectedCalls.Load())
	}

	allScopesForbidden := doRequest(t, http.MethodPost, hubHTTP.URL+"/mcp", "denied", "application/json", protectedBody)
	allScopesChallenge := allScopesForbidden.Header.Get("WWW-Authenticate")
	if allScopesForbidden.StatusCode != http.StatusForbidden || !strings.Contains(allScopesChallenge, `scope="mcp:alpha mcp:alpha:audit mcp:alpha:dangerous"`) {
		t.Fatalf("combined-scope forbidden status/challenge = %d, %q", allScopesForbidden.StatusCode, allScopesChallenge)
	}
	_ = allScopesForbidden.Body.Close()
	if protectedCalls.Load() != 0 {
		t.Fatalf("protected backend calls after combined-scope rejection = %d, want 0", protectedCalls.Load())
	}

	protectedResult, err := privilegedSession.CallTool(ctx, &mcp.CallToolParams{Name: protectedExposedName, Arguments: map[string]any{}})
	if err != nil {
		t.Fatalf("call protected tool with scope: %v", err)
	}
	if text, ok := protectedResult.Content[0].(*mcp.TextContent); !ok || text.Text != "protected" || protectedCalls.Load() != 1 {
		t.Fatalf("protected tool result/calls = %#v/%d", protectedResult.Content, protectedCalls.Load())
	}

	backendServer.AddTool(&mcp.Tool{Name: "future_delete", InputSchema: map[string]any{"type": "object"}}, func(_ context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "future"}}}, nil
	})
	listChangeCtx, cancelListChange := context.WithTimeout(ctx, 5*time.Second)
	defer cancelListChange()
	select {
	case <-protectedListChanged:
	case <-listChangeCtx.Done():
		t.Fatal("timed out waiting for protected list-changed refresh")
	}
	privilegedTools, err = privilegedSession.ListTools(listChangeCtx, nil)
	if err != nil || len(privilegedTools.Tools) != 3 {
		t.Fatalf("tools after protected list change = %v, err=%v", privilegedTools.Tools, err)
	}
	postChangeAllowedSession := connectAppClient(t, ctx, hubHTTP.URL+"/mcp", "allowed")
	defer postChangeAllowedSession.Close()
	allowedTools, err = postChangeAllowedSession.ListTools(ctx, nil)
	if err != nil || len(allowedTools.Tools) != 1 || allowedTools.Tools[0].Name != "alpha.echo" {
		t.Fatalf("backend-only tools after protected list change = %v, err=%v", allowedTools.Tools, err)
	}
	resourcePage, err := allowedSession.ListResources(ctx, nil)
	if err != nil || len(resourcePage.Resources) != 1 {
		t.Fatalf("list resources with scope: result=%#v err=%v", resourcePage, err)
	}
	resourceURI := resourcePage.Resources[0].URI
	if err := allowedSession.Subscribe(ctx, &mcp.SubscribeParams{URI: resourceURI}); err != nil {
		t.Fatalf("subscribe resource: %v", err)
	}
	waitSubscriptionEvent(t, ctx, subscribed, "memory://shared")
	if err := allowedSession.Unsubscribe(ctx, &mcp.UnsubscribeParams{URI: resourceURI}); err != nil {
		t.Fatalf("unsubscribe resource: %v", err)
	}
	waitSubscriptionEvent(t, ctx, unsubscribed, "memory://shared")
	result, err = allowedSession.CallTool(ctx, &mcp.CallToolParams{Name: "alpha.echo", Arguments: map[string]any{}})
	if err != nil {
		t.Fatalf("call tool after unsubscribe: %v", err)
	}
}

func TestParentCancellationDoesNotInterruptRuntimeBeforeDrain(t *testing.T) {
	started := make(chan struct{})
	backendCanceled := make(chan struct{}, 1)
	release := make(chan struct{})
	released := false
	t.Cleanup(func() {
		if !released {
			close(release)
		}
	})

	backendServer := mcp.NewServer(&mcp.Implementation{Name: "backend", Version: "1"}, nil)
	backendServer.AddTool(&mcp.Tool{
		Name:        "slow",
		InputSchema: map[string]any{"type": "object"},
	}, func(ctx context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		close(started)
		select {
		case <-ctx.Done():
			backendCanceled <- struct{}{}
			return nil, ctx.Err()
		case <-release:
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "complete"}}}, nil
		}
	})
	backendHTTP := httptest.NewServer(mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return backendServer },
		&mcp.StreamableHTTPOptions{Stateless: true, PropagateRequestCancellation: true},
	))
	t.Cleanup(func() {
		backendHTTP.CloseClientConnections()
		backendHTTP.Close()
	})

	cfg := &config.Config{
		Server: config.ServerConfig{
			Listen:              ":8080",
			PublicURL:           "https://hub.example.com/mcp",
			PageSize:            100,
			RequestTimeout:      config.Duration{Duration: 5 * time.Second},
			DrainTimeout:        config.Duration{Duration: time.Second},
			RefreshInterval:     config.Duration{Duration: time.Hour},
			CatalogTTL:          config.Duration{Duration: 30 * time.Second},
			MaxRequestBodyBytes: 4 << 20,
		},
		Auth: config.AuthConfig{Issuer: "https://idp.example.com"},
		Backends: []config.BackendConfig{{
			ID:                "alpha",
			URL:               backendHTTP.URL,
			Required:          true,
			RequestTimeout:    config.Duration{Duration: 5 * time.Second},
			AllowInsecureHTTP: true,
		}},
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	parent, cancelParent := context.WithCancel(context.Background())
	application, err := New(parent, cfg, "", logger)
	if err != nil {
		cancelParent()
		t.Fatalf("new app: %v", err)
	}
	t.Cleanup(application.Close)

	client, _ := application.currentRuntime().manager.Client("alpha")
	callDone := make(chan error, 1)
	go func() {
		_, err := client.CallTool(context.Background(), nil, &mcp.CallToolParams{Name: "slow", Arguments: map[string]any{}})
		callDone <- err
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("backend call did not start")
	}

	cancelParent()
	select {
	case <-backendCanceled:
		t.Fatal("parent cancellation interrupted the backend before the drain")
	case <-time.After(200 * time.Millisecond):
	}
	close(release)
	released = true
	select {
	case err := <-callDone:
		if err != nil {
			t.Fatalf("backend call after shutdown signal: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("backend call did not finish after release")
	}
}

func TestRuntimeCloseCancelsActiveSubscriptionRequest(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := &config.Config{
		Server: config.ServerConfig{
			PublicURL:           "https://hub.example.com/mcp",
			RequestTimeout:      config.Duration{Duration: 5 * time.Second},
			MaxRequestBodyBytes: 4 << 20,
		},
		Auth: config.AuthConfig{Issuer: "https://idp.example.com"},
	}
	ctx, cancel := context.WithCancel(context.Background())
	manager := backend.NewManager(cfg, logger, nil, nil)
	currentHub := hub.New(cfg, manager, logger)
	entered := make(chan struct{})
	requestCanceled := make(chan struct{})
	rt := &runtime{
		cfg:     cfg,
		manager: manager,
		hub:     currentHub,
		ctx:     ctx,
		cancel:  cancel,
		origins: http.NewCrossOriginProtection(),
		mcpHandler: http.HandlerFunc(func(_ http.ResponseWriter, req *http.Request) {
			close(entered)
			<-req.Context().Done()
			close(requestCanceled)
		}),
	}
	rt.origins.AddTrustedOrigin("https://hub.example.com")
	application := &App{
		ctx:     ctx,
		cancel:  cancel,
		auth:    staticVerifier{},
		runtime: rt,
		logger:  logger,
	}
	t.Cleanup(rt.close)

	req := httptest.NewRequest(http.MethodPost, "https://hub.example.com/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"subscriptions/listen","params":{"notifications":{}}}`))
	req.Header.Set("Authorization", "Bearer allowed")
	req.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	serveDone := make(chan struct{})
	go func() {
		application.ServeHTTP(response, req)
		close(serveDone)
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("subscription request did not enter the runtime handler")
	}

	rt.close()
	select {
	case <-requestCanceled:
	case <-time.After(2 * time.Second):
		t.Fatal("runtime close did not cancel the active subscription request")
	}
	select {
	case <-serveDone:
	case <-time.After(2 * time.Second):
		t.Fatal("ServeHTTP did not return after runtime close")
	}
}

func TestUnauthenticatedSlowRequestBodyReturnsUnauthorized(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := &config.Config{
		Server: config.ServerConfig{
			PublicURL:           "https://hub.example.com/mcp",
			RequestTimeout:      config.Duration{Duration: 100 * time.Millisecond},
			MaxRequestBodyBytes: 4 << 20,
		},
		Auth: config.AuthConfig{Issuer: "https://idp.example.com"},
	}
	ctx, cancel := context.WithCancel(context.Background())
	manager := backend.NewManager(cfg, logger, nil, nil)
	currentHub := hub.New(cfg, manager, logger)
	rt := &runtime{
		cfg:        cfg,
		manager:    manager,
		hub:        currentHub,
		ctx:        ctx,
		cancel:     cancel,
		origins:    http.NewCrossOriginProtection(),
		mcpHandler: http.NotFoundHandler(),
	}
	rt.origins.AddTrustedOrigin("https://hub.example.com")
	application := &App{
		ctx:     ctx,
		cancel:  cancel,
		auth:    staticVerifier{},
		runtime: rt,
		logger:  logger,
	}
	t.Cleanup(application.Close)
	handlerReturned := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		application.ServeHTTP(w, req)
		close(handlerReturned)
	}))
	t.Cleanup(func() {
		server.CloseClientConnections()
		server.Close()
	})

	conn, err := net.Dial("tcp", server.Listener.Addr().String())
	if err != nil {
		t.Fatalf("dial test server: %v", err)
	}
	defer conn.Close()
	if _, err := io.WriteString(conn, "POST /mcp HTTP/1.1\r\nHost: hub.example.com\r\nAuthorization: Bearer invalid\r\nContent-Type: application/json\r\nContent-Length: 1\r\n\r\n"); err != nil {
		t.Fatalf("write slow request headers: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	response, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: http.MethodPost})
	if err != nil {
		t.Fatalf("read unauthorized response: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated slow request status = %d, want %d", response.StatusCode, http.StatusUnauthorized)
	}
	select {
	case <-handlerReturned:
	case <-time.After(2 * time.Second):
		t.Fatal("unauthenticated slow request handler did not return")
	}
}

func TestAppUsesStrictOriginAndCORSHeaderAllowlist(t *testing.T) {
	application := &App{
		ctx:    context.Background(),
		cancel: func() {},
		auth:   staticVerifier{},
		runtime: &runtime{
			ctx: context.Background(),
			cfg: &config.Config{Server: config.ServerConfig{
				PublicURL:      "https://hub.example.com/mcp",
				AllowedOrigins: []string{"https://console.example.com"},
			}},
			origins: http.NewCrossOriginProtection(),
		},
	}
	application.runtime.origins.AddTrustedOrigin("https://hub.example.com")
	application.runtime.origins.AddTrustedOrigin("https://console.example.com")

	request := httptest.NewRequest(http.MethodOptions, "https://hub.example.com/mcp", nil)
	request.Header.Set("Origin", "https://console.example.com")
	request.Header.Set("Access-Control-Request-Method", http.MethodPost)
	request.Header.Set("Access-Control-Request-Headers", "Authorization, X-Evil")
	response := httptest.NewRecorder()
	application.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("unapproved CORS header status = %d", response.Code)
	}

	request = httptest.NewRequest(http.MethodOptions, "https://hub.example.com/mcp", nil)
	request.Header.Set("Origin", "https://console.example.com")
	request.Header.Set("Access-Control-Request-Method", http.MethodPost)
	request.Header.Set("Access-Control-Request-Headers", "Authorization, Mcp-Protocol-Version, Mcp-Method, Mcp-Name, Mcp-Param-Project")
	response = httptest.NewRecorder()
	application.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent || response.Header().Get("Access-Control-Allow-Origin") != "https://console.example.com" {
		t.Fatalf("approved CORS preflight = %d, headers %v", response.Code, response.Header())
	}
	if !strings.Contains(response.Header().Get("Access-Control-Allow-Headers"), "Mcp-Param-Project") {
		t.Fatalf("approved CORS headers = %q", response.Header().Get("Access-Control-Allow-Headers"))
	}
	if got := response.Header().Get("Access-Control-Allow-Methods"); got != "POST, OPTIONS" {
		t.Fatalf("approved CORS methods = %q", got)
	}

	request = httptest.NewRequest(http.MethodPost, "https://hub.example.com/mcp", nil)
	request.Header.Set("Origin", "https://evil.example.com")
	response = httptest.NewRecorder()
	application.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("unapproved origin status = %d", response.Code)
	}

	request = httptest.NewRequest(http.MethodGet, "https://hub.example.com/healthz", nil)
	request.Header.Set("X-Request-Id", strings.Repeat("x", 129))
	response = httptest.NewRecorder()
	application.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("health response status = %d", response.Code)
	}
	if got := response.Header().Get("X-Request-Id"); got == strings.Repeat("x", 129) || !validRequestID(got) {
		t.Fatalf("sanitized request ID = %q", got)
	}
}

type staticVerifier struct{}

func (staticVerifier) Ready() bool { return true }

func (staticVerifier) Verify(_ context.Context, token string, _ *http.Request) (*mcpauth.TokenInfo, error) {
	info := &mcpauth.TokenInfo{UserID: "test-user", Expiration: time.Now().Add(time.Hour)}
	switch token {
	case "allowed":
		info.Scopes = []string{"mcp:alpha"}
		return info, nil
	case "privileged":
		info.Scopes = []string{"mcp:alpha", "mcp:alpha:audit", "mcp:alpha:dangerous"}
		return info, nil
	case "denied":
		return info, nil
	default:
		return nil, errors.Join(mcpauth.ErrInvalidToken, errors.New("test token rejected"))
	}
}

type bearerTransport struct {
	token string
	base  http.RoundTripper
}

func (t bearerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	copyRequest := req.Clone(req.Context())
	copyRequest.Header = req.Header.Clone()
	copyRequest.Header.Set("Authorization", "Bearer "+t.token)
	return t.base.RoundTrip(copyRequest)
}

func connectAppClient(t *testing.T, ctx context.Context, endpoint, token string) *mcp.ClientSession {
	return connectAppClientWithOptions(t, ctx, endpoint, token, nil)
}

func connectAppClientWithOptions(t *testing.T, ctx context.Context, endpoint, token string, options *mcp.ClientOptions) *mcp.ClientSession {
	t.Helper()
	client := mcp.NewClient(&mcp.Implementation{Name: "app-test", Version: "1"}, options)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint: endpoint,
		HTTPClient: &http.Client{Transport: bearerTransport{
			token: token,
			base:  http.DefaultTransport,
		}},
	}, nil)
	if err != nil {
		t.Fatalf("connect MCP client: %v", err)
	}
	return session
}

func doRequest(t *testing.T, method, url, token, contentType string, body []byte) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, url, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request %s: %v", url, err)
	}
	return resp
}

func decodeResponse(t *testing.T, resp *http.Response, target any) {
	t.Helper()
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(target); err != nil {
		t.Fatalf("decode response: %v", err)
	}
}

func waitSubscriptionEvent(t *testing.T, ctx context.Context, events <-chan string, want string) {
	t.Helper()
	select {
	case got := <-events:
		if got != want {
			t.Fatalf("subscription URI = %q, want %q", got, want)
		}
	case <-ctx.Done():
		t.Fatalf("timed out waiting for subscription URI %q", want)
	}
}
