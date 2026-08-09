package hub

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/SamuelSupe/mcphub/internal/backend"
	"github.com/SamuelSupe/mcphub/internal/config"
	"github.com/SamuelSupe/mcphub/internal/httptool"
)

func TestHTTPToolHandlerKeepsCapturedManagerAcrossReplacement(t *testing.T) {
	var oldCalls, newCalls atomic.Int32
	oldUpstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		oldCalls.Add(1)
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, "old")
	}))
	newUpstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		newCalls.Add(1)
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, "new")
	}))
	t.Cleanup(oldUpstream.Close)
	t.Cleanup(newUpstream.Close)
	roots := x509.NewCertPool()
	roots.AddCert(oldUpstream.Certificate())
	roots.AddCert(newUpstream.Certificate())
	previousTransport := http.DefaultTransport
	transport := previousTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	http.DefaultTransport = transport
	t.Cleanup(func() { http.DefaultTransport = previousTransport })

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := &config.Config{Server: config.ServerConfig{
		PageSize:       10,
		RequestTimeout: config.Duration{Duration: time.Second},
	}}
	backendManager := backend.NewManager(cfg, logger, nil, nil)
	t.Cleanup(backendManager.Close)

	managerFor := func(baseURL string) *httptool.Manager {
		manager, err := httptool.NewManager(context.Background(), []httptool.GroupConfig{{
			ID:                   "payments",
			BaseURL:              baseURL,
			Enabled:              true,
			RequestTimeout:       time.Second,
			MaxResponseBodyBytes: httptool.DefaultMaxResponseBytes,
			Tools: []httptool.ToolConfig{{
				Name:    "lookup",
				Enabled: true,
				Method:  http.MethodGet,
				Path:    "/lookup",
			}},
		}}, logger)
		if err != nil {
			t.Fatalf("NewManager(%q): %v", baseURL, err)
		}
		return manager
	}
	oldManager := managerFor(oldUpstream.URL)
	newManager := managerFor(newUpstream.URL)
	h := NewWithHTTPTools(cfg, backendManager, oldManager, logger)
	t.Cleanup(h.Close)
	v := newViewWithHTTP(h, nil, nil, nil, nil)
	t.Cleanup(v.close)

	call := func(handler mcp.ToolHandler) string {
		t.Helper()
		result, err := handler(context.Background(), &mcp.CallToolRequest{
			Params: &mcp.CallToolParamsRaw{Arguments: []byte(`{}`)},
		})
		if err != nil {
			t.Fatalf("HTTP tool handler: %v", err)
		}
		if result == nil || len(result.Content) != 1 {
			t.Fatalf("HTTP tool result = %#v", result)
		}
		text, ok := result.Content[0].(*mcp.TextContent)
		if !ok {
			t.Fatalf("HTTP tool result content = %T, want text", result.Content[0])
		}
		return text.Text
	}

	oldDefinition, ok := h.httpToolDefinitions("payments")["payments.lookup"]
	if !ok {
		t.Fatal("old HTTP tool definition missing")
	}
	oldHandler := v.toolHandler(oldDefinition)
	if got := call(oldHandler); got != "old" {
		t.Fatalf("initial old handler result = %q, want old", got)
	}

	h.ReplaceHTTPTools(newManager)
	if got := call(oldHandler); got != "old" {
		t.Fatalf("captured old handler result after replacement = %q, want old", got)
	}
	if oldCalls.Load() != 2 || newCalls.Load() != 0 {
		t.Fatalf("upstream calls after old handler = old %d, new %d; want 2, 0", oldCalls.Load(), newCalls.Load())
	}

	newDefinition, ok := h.httpToolDefinitions("payments")["payments.lookup"]
	if !ok {
		t.Fatal("new HTTP tool definition missing")
	}
	if got := call(v.toolHandler(newDefinition)); got != "new" {
		t.Fatalf("new handler result = %q, want new", got)
	}
	if oldCalls.Load() != 2 || newCalls.Load() != 1 {
		t.Fatalf("upstream calls after new handler = old %d, new %d; want 2, 1", oldCalls.Load(), newCalls.Load())
	}
}
