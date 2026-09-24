package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/SamuelSupe/mcphub/internal/config"
	"github.com/SamuelSupe/mcphub/internal/ratelimit"
)

func TestEndpointRateLimitsThroughAdminAndMCP(t *testing.T) {
	var calls atomic.Int32
	upstream := mcp.NewServer(&mcp.Implementation{Name: "rate-test", Version: "1"}, nil)
	upstream.AddTool(&mcp.Tool{Name: "echo", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		calls.Add(1)
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "executed"}}}, nil
	})
	backend := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return upstream }, &mcp.StreamableHTTPOptions{Stateless: true}))
	defer backend.Close()
	application := newAdminTestApp(t)
	input := backendInput{ID: "alpha", URL: backend.URL, AllowInsecureHTTP: true, RequiredScopes: []string{"mcp:alpha"}, RateLimit: ratelimit.Config{RequestsPerSecond: .001, Burst: 1}}
	body, _ := json.Marshal(input)
	created := serveAdminJSON(t, application, "POST", "/api/v1/backends", body, "")
	if created.Code != 201 {
		t.Fatalf("create: %d %s", created.Code, created.Body.String())
	}
	record, err := application.store.Get(t.Context(), "alpha")
	if err != nil || record.Config.RateLimit != input.RateLimit {
		t.Fatalf("persisted policy: %+v %v", record, err)
	}
	if response := rateLimitedMCPRequest(application, t.Context(), "denied", "tools/call", "alpha.echo"); response.Code != 403 {
		t.Fatalf("unauthorized caller: %d", response.Code)
	}
	if response := rateLimitedMCPRequest(application, t.Context(), "allowed", "tools/call", "alpha.echo"); response.Code != 200 || !strings.Contains(response.Body.String(), "executed") {
		t.Fatalf("initial call: %d %s", response.Code, response.Body.String())
	}
	updated := serveAdminJSON(t, application, "PUT", "/api/v1/backends/alpha", body, created.Header().Get("ETag"))
	if updated.Code != 200 {
		t.Fatalf("update: %s", updated.Body.String())
	}
	response := rateLimitedMCPRequest(application, t.Context(), "privileged", "tools/call", "alpha.echo")
	if response.Code != 429 || response.Header().Get("Retry-After") == "" || calls.Load() != 1 {
		t.Fatalf("shared budget or reload bypass: %d %s, calls=%d", response.Code, response.Body.String(), calls.Load())
	}
	var rejected struct {
		ID    int
		Error struct{ Data ratelimit.Rejection }
	}
	if err := json.Unmarshal(response.Body.Bytes(), &rejected); err != nil || rejected.ID != 17 || rejected.Error.Data.Endpoint != "alpha" {
		t.Fatalf("rejection lost request identity: %s", response.Body.String())
	}
	if response := rateLimitedMCPRequest(application, t.Context(), "allowed", "tools/list", ""); response.Code != 200 || !strings.Contains(response.Body.String(), "alpha.echo") {
		t.Fatalf("exhausted endpoint blocked catalog access: %d %s", response.Code, response.Body.String())
	}
	// Creating another endpoint rebuilds the runtime; its default remains unlimited.
	unlimited := backendInput{ID: "beta", URL: backend.URL, AllowInsecureHTTP: true}
	unlimitedBody, _ := json.Marshal(unlimited)
	if response := serveAdminJSON(t, application, "POST", "/api/v1/backends", unlimitedBody, ""); response.Code != 201 {
		t.Fatal(response.Body.String())
	}
	for range 3 {
		if response := rateLimitedMCPRequest(application, t.Context(), "allowed", "tools/call", "beta.echo"); response.Code != 200 {
			t.Fatal(response.Body.String())
		}
	}
	if response := rateLimitedMCPRequest(application, t.Context(), "allowed", "resources/unsubscribe", "mcphub://alpha/r/c2FtcGxl"); response.Code == 429 {
		t.Fatal("rate limit prevented subscription cleanup")
	}
	input.RateLimit = ratelimit.Config{Burst: 2}
	invalid, _ := json.Marshal(input)
	if response := serveAdminJSON(t, application, "PUT", "/api/v1/backends/alpha", invalid, updated.Header().Get("ETag")); response.Code != 400 {
		t.Fatalf("invalid policy: %d", response.Code)
	}
	input.RateLimit = ratelimit.Config{}
	body, _ = json.Marshal(input)
	if response := serveAdminJSON(t, application, "PUT", "/api/v1/backends/alpha", body, updated.Header().Get("ETag")); response.Code != 200 {
		t.Fatal(response.Body.String())
	}
	if response := rateLimitedMCPRequest(application, t.Context(), "allowed", "tools/call", "alpha.echo"); response.Code != 200 {
		t.Fatalf("disabled limit: %s", response.Body.String())
	}
}

func TestHTTPToolGroupRateLimitSurvivesChildEdits(t *testing.T) {
	var calls atomic.Int32
	api := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls.Add(1); fmt.Fprint(w, `{"ok":true}`) }))
	defer api.Close()
	previousTransport := http.DefaultTransport
	http.DefaultTransport = api.Client().Transport
	t.Cleanup(func() { http.DefaultTransport = previousTransport })
	application := newAdminTestApp(t)
	group := fmt.Appendf(nil, `{"id":"api","base_url":%q,"rate_limit":{"requests_per_second":0.001,"burst":1}}`, api.URL)
	if response := serveAdminJSON(t, application, "POST", "/api/v1/tool-groups", group, ""); response.Code != 201 {
		t.Fatal(response.Body.String())
	}
	tool := []byte(`{"name":"get","method":"GET","path":"/get"}`)
	if response := serveAdminJSON(t, application, "POST", "/api/v1/tool-groups/api/tools", tool, ""); response.Code != 201 {
		t.Fatal(response.Body.String())
	}
	if response := rateLimitedMCPRequest(application, t.Context(), "allowed", "tools/call", "api.get"); response.Code != 200 || calls.Load() != 1 {
		t.Fatalf("HTTP tool call: %s", response.Body.String())
	}
	if response := serveAdminJSON(t, application, "POST", "/api/v1/tool-groups/api/tools", []byte(`{"name":"other","method":"GET","path":"/other"}`), ""); response.Code != 201 {
		t.Fatal(response.Body.String())
	}
	if response := rateLimitedMCPRequest(application, t.Context(), "allowed", "tools/call", "api.other"); response.Code != 429 || calls.Load() != 1 {
		t.Fatalf("group budget reset on child edit: %s", response.Body.String())
	}
	stored, err := application.store.GetToolGroup(t.Context(), "api")
	if err != nil || stored.Config.RateLimit.Burst != 1 {
		t.Fatalf("stored group policy: %+v %v", stored, err)
	}
}

func TestEndpointConcurrencyReleasedOnCancellation(t *testing.T) {
	application := newAdminTestApp(t)
	cfg := *application.currentConfig()
	cfg.Server.RequestTimeout = config.Duration{Duration: 5 * time.Second}
	if err := application.replaceRuntimeLocked(&cfg); err != nil {
		t.Fatal(err)
	}
	input := []byte(`{"id":"alpha","url":"http://127.0.0.1:1/mcp","allow_insecure_http":true,"request_timeout":"10ms","rate_limit":{"max_concurrent":1}}`)
	if response := serveAdminJSON(t, application, "POST", "/api/v1/backends", input, ""); response.Code != 201 {
		t.Fatal(response.Body.String())
	}
	// Keep the response stream open independently of backend discovery, to verify
	// admission slots track the HTTP request lifetime and its cancellation.
	started := make(chan struct{}, 1)
	application.currentRuntime().mcpHandler = http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Header.Get("Mcp-Name") == "alpha.slow" {
			started <- struct{}{}
			<-req.Context().Done()
		}
		w.WriteHeader(200)
	})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan struct{})
	go func() { rateLimitedMCPRequest(application, ctx, "allowed", "tools/call", "alpha.slow"); close(done) }()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("call did not start")
	}
	if response := rateLimitedMCPRequest(application, t.Context(), "allowed", "tools/call", "alpha.echo"); response.Code != 429 {
		t.Fatal("concurrency overflow was accepted")
	}
	if response := rateLimitedMCPRequest(application, t.Context(), "allowed", "resources/unsubscribe", "mcphub://alpha/r/c2FtcGxl"); response.Code != 200 {
		t.Fatal("cleanup blocked by concurrency")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("cancellation did not release request")
	}
	if response := rateLimitedMCPRequest(application, t.Context(), "allowed", "tools/call", "alpha.echo"); response.Code != 200 {
		t.Fatal("cancellation leaked concurrency slot")
	}
}

func rateLimitedMCPRequest(application *App, ctx context.Context, token, method, name string) *httptest.ResponseRecorder {
	params := map[string]any{"name": name, "arguments": map[string]any{}, "_meta": map[string]any{
		"io.modelcontextprotocol/protocolVersion":    "2026-07-28",
		"io.modelcontextprotocol/clientInfo":         map[string]string{"name": "rate-test", "version": "1"},
		"io.modelcontextprotocol/clientCapabilities": map[string]any{},
	}}
	if method == "resources/unsubscribe" {
		params = map[string]any{"uri": name}
	}
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 17, "method": method, "params": params})
	req := httptest.NewRequest("POST", "/mcp", bytes.NewReader(body)).WithContext(ctx)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Mcp-Protocol-Version", "2026-07-28")
	req.Header.Set("Mcp-Method", method)
	req.Header.Set("Mcp-Name", name)
	response := httptest.NewRecorder()
	application.ServeHTTP(response, req)
	return response
}
