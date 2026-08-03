package hub

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/SamuelSupe/mcphub/internal/backend"
	"github.com/SamuelSupe/mcphub/internal/config"
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
	backendConfig := config.BackendConfig{ToolRules: []config.ToolRule{{
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

func (schema countingSchema) MarshalJSON() ([]byte, error) {
	*schema.calls = *schema.calls + 1
	return []byte(`{"type":"object"}`), nil
}
