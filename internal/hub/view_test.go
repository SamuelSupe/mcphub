package hub

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	mcpauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/SamuelSupe/mcphub/v2/internal/backend"
	"github.com/SamuelSupe/mcphub/v2/internal/config"
)

func TestApplyToolsDropsStalePublicToolAfterInvalidSchema(t *testing.T) {
	cfg := &config.Config{Server: config.ServerConfig{
		PageSize:        10,
		RequestTimeout:  config.Duration{Duration: time.Second},
		CatalogTTL:      config.Duration{Duration: time.Second},
		RefreshInterval: config.Duration{Duration: time.Second},
	}}
	logger := slog.Default()
	h := New(cfg, backend.NewManager(cfg, logger, nil, nil), logger)
	v := newView(h, nil, nil)
	t.Cleanup(func() {
		v.close()
		h.manager.Close()
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	connect := func() (*mcp.ClientSession, *mcp.ServerSession) {
		clientTransport, serverTransport := mcp.NewInMemoryTransports()
		serverSession, err := v.server.Connect(ctx, serverTransport, nil)
		if err != nil {
			t.Fatalf("connect in-memory server: %v", err)
		}
		client := mcp.NewClient(&mcp.Implementation{Name: "view-test", Version: "1"}, nil)
		clientSession, err := client.Connect(ctx, clientTransport, nil)
		if err != nil {
			_ = serverSession.Close()
			t.Fatalf("connect in-memory client: %v", err)
		}
		t.Cleanup(func() { _ = clientSession.Close() })
		t.Cleanup(func() { _ = serverSession.Close() })
		return clientSession, serverSession
	}

	const exposedName = "alpha.echo"
	valid := &mcp.Tool{Name: exposedName, InputSchema: map[string]any{"type": "object"}}
	validFingerprint, err := fingerprint(valid)
	if err != nil {
		t.Fatalf("fingerprint valid tool: %v", err)
	}
	v.applyTools(map[string]toolDefinition{
		exposedName: {
			backendID:   "alpha",
			original:    "echo",
			tool:        valid,
			fingerprint: validFingerprint,
		},
	})
	clientSession, serverSession := connect()
	initial, err := clientSession.ListTools(ctx, &mcp.ListToolsParams{})
	if err != nil {
		t.Fatalf("list tools after valid install: %v", err)
	}
	if len(initial.Tools) != 1 || initial.Tools[0].Name != exposedName {
		t.Fatalf("initial public tools = %#v, want %q", initial.Tools, exposedName)
	}
	_ = clientSession.Close()
	_ = serverSession.Close()

	invalid := &mcp.Tool{Name: exposedName, InputSchema: map[string]any{"type": "string"}}
	invalidFingerprint, err := fingerprint(invalid)
	if err != nil {
		t.Fatalf("fingerprint invalid tool: %v", err)
	}
	v.applyTools(map[string]toolDefinition{
		exposedName: {
			backendID:   "alpha",
			original:    "echo",
			tool:        invalid,
			fingerprint: invalidFingerprint,
		},
	})
	clientSession, serverSession = connect()
	updated, err := clientSession.ListTools(ctx, &mcp.ListToolsParams{})
	if err != nil {
		t.Fatalf("list tools after invalid update: %v", err)
	}
	if len(updated.Tools) != 0 {
		t.Fatalf("public tools after invalid update = %#v, want no stale definition", updated.Tools)
	}
}

func TestToolDefinitionsCacheTracksCatalogGeneration(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := &Hub{
		logger:               logger,
		toolDefinitionCaches: make(map[string]*toolDefinitionCache),
	}
	calls := 0
	tool := &mcp.Tool{
		Name:        "delete_record",
		InputSchema: countingSchema{calls: &calls},
	}
	backendConfig := config.BackendConfig{PublishedTools: []string{"delete_record"}, ToolRules: []config.ToolRule{{
		Match:          "delete_*",
		RequiredScopes: []string{"mcp:dangerous"},
	}}}
	firstCatalog := &backend.Catalog{Tools: []*mcp.Tool{tool}}
	definitions := h.toolDefinitions("alpha", backendConfig, firstCatalog, false)
	if definition, ok := definitions["alpha.delete_record"]; !ok || len(definition.requiredScopes) != 1 || definition.requiredScopes[0] != "mcp:dangerous" {
		t.Fatalf("first catalog definitions = %#v", definitions)
	}
	firstCalls := calls
	if firstCalls == 0 {
		t.Fatal("first catalog was not fingerprinted")
	}

	h.toolDefinitions("alpha", backendConfig, firstCatalog, true)
	if calls != firstCalls {
		t.Fatalf("same catalog generation fingerprint calls = %d, want %d", calls, firstCalls)
	}
	h.toolDefinitions("alpha", backendConfig, &backend.Catalog{Tools: []*mcp.Tool{tool}}, false)
	if calls <= firstCalls {
		t.Fatalf("new catalog generation fingerprint calls = %d, want greater than %d", calls, firstCalls)
	}
}

type countingSchema struct {
	calls *int
}

func TestViewCacheRetainsActiveRequestsAndSessions(t *testing.T) {
	cfg := &config.Config{Server: config.ServerConfig{PageSize: 10, RequestTimeout: config.Duration{Duration: 5 * time.Second}}}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := New(cfg, backend.NewManager(cfg, logger, nil, nil), logger)
	t.Cleanup(h.Close)
	verify := func(_ context.Context, raw string, _ *http.Request) (*mcpauth.TokenInfo, error) {
		return &mcpauth.TokenInfo{Scopes: []string{raw}, Expiration: time.Now().Add(time.Hour)}, nil
	}
	var leases []*view
	acquire := mcpauth.RequireBearerToken(verify, nil)(http.HandlerFunc(func(_ http.ResponseWriter, req *http.Request) {
		v := h.acquireView(req)
		if v == nil {
			t.Fatal("view rejected before reaching capacity")
		}
		leases = append(leases, v)
	}))
	for i := range maxCachedViews {
		req := httptest.NewRequest("POST", "https://hub.example/mcp", nil)
		req.Header.Set("Authorization", fmt.Sprintf("Bearer scope-%d", i))
		acquire.ServeHTTP(httptest.NewRecorder(), req)
	}
	t.Cleanup(func() {
		for _, v := range leases[1:] {
			h.releaseView(v)
		}
	})
	handler := mcpauth.RequireBearerToken(verify, nil)(h.StreamableHTTPHandler(&mcp.StreamableHTTPOptions{Stateless: true}))
	initialize := func(want int) {
		t.Helper()
		req := httptest.NewRequest("POST", "https://hub.example/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"visitor","version":"1"}}}`))
		req.Header.Set("Authorization", "Bearer new-scope")
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		if response.Code != want {
			t.Fatalf("view admission: HTTP %d, want %d: %s", response.Code, want, response.Body.String())
		}
		if want == http.StatusServiceUnavailable && response.Header().Get("Retry-After") == "" {
			t.Fatal("capacity rejection omitted retry guidance")
		}
	}
	initialize(http.StatusServiceUnavailable)
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	server, err := leases[0].server.Connect(t.Context(), serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	client, err := mcp.NewClient(&mcp.Implementation{Name: "active", Version: "1"}, nil).Connect(t.Context(), clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	h.releaseView(leases[0])
	initialize(http.StatusServiceUnavailable)
	if err := client.Ping(t.Context(), nil); err != nil {
		t.Fatalf("cache pressure closed an active session: %v", err)
	}
	client.Close()
	server.Close()
	initialize(http.StatusOK)
}

func (schema countingSchema) MarshalJSON() ([]byte, error) {
	*schema.calls = *schema.calls + 1
	return []byte(`{"type":"object"}`), nil
}
