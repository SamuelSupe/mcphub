package backend

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestCatalogDiscoveryStartsAllSupportedListsConcurrently(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "catalog-test", Version: "1"}, nil)
	server.AddTool(&mcp.Tool{Name: "tool", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{}, nil
	})
	server.AddPrompt(&mcp.Prompt{Name: "prompt"}, func(context.Context, *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		return &mcp.GetPromptResult{}, nil
	})
	read := func(_ context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: req.Params.URI}}}, nil
	}
	server.AddResource(&mcp.Resource{Name: "resource", URI: "memory://resource"}, read)
	server.AddResourceTemplate(&mcp.ResourceTemplate{Name: "template", URITemplate: "memory://items/{id}"}, read)

	started := make(chan string, 4)
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	server.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			switch method {
			case "tools/list", "prompts/list", "resources/list", "resources/templates/list":
				started <- method
				select {
				case <-release:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
			return next(ctx, method, req)
		}
	})

	httpServer := httptest.NewServer(mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return server },
		&mcp.StreamableHTTPOptions{Stateless: true},
	))
	t.Cleanup(httpServer.Close)
	client := mcp.NewClient(&mcp.Implementation{Name: "catalog-client", Version: "1"}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: httpServer.URL, MaxRetries: -1}, nil)
	if err != nil {
		t.Fatalf("connect catalog client: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })

	type outcome struct {
		catalog *Catalog
		err     error
	}
	done := make(chan outcome, 1)
	go func() {
		catalog, err := discoverCatalog(ctx, session, time.Hour)
		done <- outcome{catalog: catalog, err: err}
	}()

	methods := make(map[string]struct{}, 4)
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	for len(methods) < 4 {
		select {
		case method := <-started:
			methods[method] = struct{}{}
		case <-timer.C:
			unblock()
			result := <-done
			t.Fatalf("catalog discovery did not start all lists concurrently; started %v, result error %v", methods, result.err)
		}
	}
	unblock()
	result := <-done
	if result.err != nil {
		t.Fatalf("discover catalog: %v", result.err)
	}
	if len(result.catalog.Tools) != 1 || len(result.catalog.Prompts) != 1 || len(result.catalog.Resources) != 1 || len(result.catalog.ResourceTemplates) != 1 {
		t.Fatalf("discovered catalog = tools:%d prompts:%d resources:%d templates:%d", len(result.catalog.Tools), len(result.catalog.Prompts), len(result.catalog.Resources), len(result.catalog.ResourceTemplates))
	}
}
