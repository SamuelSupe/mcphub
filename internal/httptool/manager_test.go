package httptool

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/SamuelSupe/mcphub/internal/config"
)

func TestManagerCallBuildsHTTPRequestAndRendersJSONAndText(t *testing.T) {
	const preciseInput = "9007199254740993"
	type observedRequest struct {
		method      string
		path        string
		filter      string
		tags        []string
		groupHeader string
		trace       string
		accept      string
		contentType string
		body        map[string]any
	}
	requests := make(chan observedRequest, 2)
	server := newManagerTestTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, _ := io.ReadAll(req.Body)
		var decoded map[string]any
		if len(body) > 0 {
			_ = json.Unmarshal(body, &decoded)
		}
		requests <- observedRequest{
			method: req.Method, path: req.URL.Path, filter: req.URL.Query().Get("filter"),
			tags: req.URL.Query()["tag"], groupHeader: req.Header.Get("X-Group"),
			trace: req.Header.Get("X-Trace"), accept: req.Header.Get("Accept"),
			contentType: req.Header.Get("Content-Type"), body: decoded,
		}
		switch req.URL.Path {
		case "/items/" + preciseInput:
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			_, _ = io.WriteString(w, `{"ok":true,"nested":{"value":3},"large":9007199254740993}`)
		case "/plain":
			w.Header().Set("Content-Type", "text/plain")
			_, _ = io.WriteString(w, "plain response")
		default:
			http.NotFound(w, req)
		}
	}))

	group := GroupConfig{
		ID: "api", BaseURL: server.URL, Enabled: true, RequestTimeout: time.Second,
		MaxResponseBodyBytes: DefaultMaxResponseBytes,
		Headers:              map[string]string{"X-Group": "configured"},
		Tools: []ToolConfig{
			{
				Name: "create_item", Enabled: true, Method: http.MethodPost, Path: "/items/{id}",
				Parameters: []Parameter{
					{Name: "id", Argument: "id", In: "path", Required: true, Schema: map[string]any{"type": "string"}},
					{Name: "filter", Argument: "filter", In: "query", Schema: map[string]any{"type": "string"}},
					{Name: "tag", Argument: "tags", In: "query", Schema: map[string]any{"type": "array", "items": map[string]any{"type": "string"}}},
					{Name: "X-Trace", Argument: "trace", In: "header", Required: true, Schema: map[string]any{"type": "string"}},
				},
				BodySchema: map[string]any{"type": "object"},
			},
			{Name: "plain", Enabled: true, Method: http.MethodGet, Path: "/plain"},
		},
	}
	manager, err := NewManager(context.Background(), []GroupConfig{group}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("NewManager() error: %v", err)
	}

	result, err := manager.Call(context.Background(), "API", "CREATE_ITEM", json.RawMessage(fmt.Sprintf(`{"id":%s,"filter":%s,"tags":["x","y"],"trace":%s,"body":{"hello":"world"}}`, preciseInput, preciseInput, preciseInput)))
	if err != nil {
		t.Fatalf("Call() error: %v", err)
	}
	if result.IsError {
		t.Fatalf("JSON result unexpectedly errored: %#v", result)
	}
	if got := result.Content[0].(*mcp.TextContent).Text; got != `{"large":9007199254740993,"nested":{"value":3},"ok":true}` {
		t.Fatalf("JSON text = %q", got)
	}
	structured, ok := result.StructuredContent.(map[string]any)
	if !ok || structured["ok"] != true {
		t.Fatalf("structured JSON = %#v", result.StructuredContent)
	}
	if encoded, err := json.Marshal(result.StructuredContent); err != nil || string(encoded) != `{"large":9007199254740993,"nested":{"value":3},"ok":true}` {
		t.Fatalf("structured JSON number preservation = %s, err=%v", encoded, err)
	}
	observed := <-requests
	if observed.method != http.MethodPost || observed.path != "/items/"+preciseInput || observed.filter != preciseInput {
		t.Fatalf("request routing = %#v", observed)
	}
	if len(observed.tags) != 2 || observed.tags[0] != "x" || observed.tags[1] != "y" {
		t.Fatalf("query array = %#v", observed.tags)
	}
	if observed.groupHeader != "configured" || observed.trace != preciseInput {
		t.Fatalf("request headers = %#v", observed)
	}
	if observed.accept != "application/json, text/plain;q=0.9" || observed.contentType != "application/json" {
		t.Fatalf("content negotiation headers = %#v", observed)
	}
	if observed.body["hello"] != "world" {
		t.Fatalf("JSON request body = %#v", observed.body)
	}

	result, err = manager.Call(context.Background(), "api", "plain", nil)
	if err != nil {
		t.Fatalf("plain Call() error: %v", err)
	}
	if result.IsError || result.Content[0].(*mcp.TextContent).Text != "plain response" || result.StructuredContent != nil {
		t.Fatalf("plain result = %#v", result)
	}
	observed = <-requests
	if observed.method != http.MethodGet || observed.path != "/plain" || observed.contentType != "" {
		t.Fatalf("plain request = %#v", observed)
	}
}

func TestManagerCallUsesParameterDefaultsWhenArgumentsAreOmitted(t *testing.T) {
	requests := make(chan struct {
		path   string
		filter string
		trace  string
	}, 1)
	server := newManagerTestTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		requests <- struct {
			path   string
			filter string
			trace  string
		}{path: req.URL.Path, filter: req.URL.Query().Get("filter"), trace: req.Header.Get("X-Trace")}
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, "ok")
	}))
	group := GroupConfig{
		ID: "api", BaseURL: server.URL, Enabled: true, RequestTimeout: time.Second,
		MaxResponseBodyBytes: DefaultMaxResponseBytes,
		Tools: []ToolConfig{{
			Name: "lookup", Enabled: true, Method: http.MethodGet, Path: "/items/{id}",
			Parameters: []Parameter{
				{Name: "id", Argument: "id", In: "path", Required: true, Schema: map[string]any{"type": "string", "default": "default-id"}},
				{Name: "filter", Argument: "filter", In: "query", Schema: map[string]any{"type": "string", "default": "all"}},
				{Name: "X-Trace", Argument: "trace", In: "header", Schema: map[string]any{"type": "string", "default": "trace-default"}},
			},
		}},
	}
	manager, err := NewManager(context.Background(), []GroupConfig{group}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("NewManager() error: %v", err)
	}
	result, err := manager.Call(context.Background(), "api", "lookup", nil)
	if err != nil {
		t.Fatalf("Call() error: %v", err)
	}
	if result.IsError || result.Content[0].(*mcp.TextContent).Text != "ok" {
		t.Fatalf("default argument result = %#v", result)
	}
	observed := <-requests
	if observed.path != "/items/default-id" || observed.filter != "all" || observed.trace != "trace-default" {
		t.Fatalf("default arguments sent upstream = %#v", observed)
	}
}

func TestManagerCallRedactsNon2xxJSONAndTextResponses(t *testing.T) {
	server := newManagerTestTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/json":
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"token":"token-value","nested":{"password":"password-value"},"authorization":"Bearer abc123"}`)
		case "/text":
			w.Header().Set("Content-Type", "text/plain")
			w.WriteHeader(http.StatusBadGateway)
			_, _ = io.WriteString(w, "Authorization: Bearer abc123; Basic dXNlcjpwYXNz")
		}
	}))
	group := GroupConfig{
		ID: "api", BaseURL: server.URL, Enabled: true, RequestTimeout: time.Second,
		MaxResponseBodyBytes: DefaultMaxResponseBytes,
		Tools: []ToolConfig{
			{Name: "json", Enabled: true, Method: http.MethodGet, Path: "/json"},
			{Name: "text", Enabled: true, Method: http.MethodGet, Path: "/text"},
		},
	}
	manager, err := NewManager(context.Background(), []GroupConfig{group}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("NewManager() error: %v", err)
	}
	for _, tool := range []string{"json", "text"} {
		result, callErr := manager.Call(context.Background(), "api", tool, nil)
		if callErr != nil {
			t.Fatalf("Call(%s) error: %v", tool, callErr)
		}
		if !result.IsError {
			t.Fatalf("Call(%s) IsError = false", tool)
		}
		text := result.Content[0].(*mcp.TextContent).Text
		if strings.Contains(text, "token-value") || strings.Contains(text, "password-value") || strings.Contains(text, "abc123") || strings.Contains(text, "dXNlcjpwYXNz") {
			t.Fatalf("Call(%s) leaked sensitive response: %q", tool, text)
		}
		if !strings.Contains(text, "[redacted]") {
			t.Fatalf("Call(%s) did not redact response: %q", tool, text)
		}
	}
}

func TestResponseResultRedactsConfiguredCredentialsAcrossSuccessAndErrorShapes(t *testing.T) {
	group := GroupConfig{
		ID:      "api",
		Headers: map[string]string{"X-API-Key": "header-value"},
		OAuth:   &config.OAuthConfig{Type: "client_credentials", ClientID: "client", ClientSecret: "oauth-secret"},
	}
	checks := []struct {
		name       string
		result     *mcp.CallToolResult
		wantError  bool
		structured bool
	}{
		{
			name:       "success JSON",
			result:     responseResult(http.StatusOK, "application/json", []byte(`{"header":"header-value","authorization":"Bearer bearer-value","oauth":"oauth-secret"}`), group),
			structured: true,
		},
		{
			name:   "success text",
			result: responseResult(http.StatusOK, "text/plain", []byte("header-value Authorization: Bearer bearer-value oauth-secret"), group),
		},
		{
			name:       "error JSON",
			result:     responseResult(http.StatusBadGateway, "application/json", []byte(`{"echo":"header-value","authorization":"Bearer bearer-value","oauth":"oauth-secret"}`), group),
			wantError:  true,
			structured: true,
		},
		{
			name:       "error text",
			result:     responseResult(http.StatusBadGateway, "text/plain", []byte("header-value Authorization: Bearer bearer-value oauth-secret"), group),
			wantError:  true,
			structured: true,
		},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			if check.result.IsError != check.wantError {
				t.Fatalf("IsError = %v, want %v", check.result.IsError, check.wantError)
			}
			text := check.result.Content[0].(*mcp.TextContent).Text
			serialized := text + "\n" + fmt.Sprint(check.result.StructuredContent)
			for _, secret := range []string{"header-value", "bearer-value", "oauth-secret"} {
				if strings.Contains(serialized, secret) {
					t.Fatalf("result leaked %q: %s", secret, serialized)
				}
			}
			if check.structured != (check.result.StructuredContent != nil) {
				t.Fatalf("StructuredContent present = %v, want %v: %#v", check.result.StructuredContent != nil, check.structured, check.result.StructuredContent)
			}
			if check.structured && !strings.Contains(serialized, "[redacted]") {
				t.Fatalf("structured result has no redaction marker: %s", serialized)
			}
		})
	}
}

func TestResponseResultRejectsMultipleJSONValues(t *testing.T) {
	result := responseResult(http.StatusOK, "application/json", []byte(`{"ok":true} {"extra":true}`), GroupConfig{})
	if !result.IsError || result.Content[0].(*mcp.TextContent).Text != "upstream API returned invalid JSON" {
		t.Fatalf("multiple JSON values result = %#v, want invalid JSON error", result)
	}
}

func TestResponseResultPreservesLargeJSONNumberOnError(t *testing.T) {
	result := responseResult(http.StatusBadGateway, "application/json", []byte(`{"large":9007199254740993}`), GroupConfig{})
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil || string(encoded) != `{"body":{"large":9007199254740993},"status":502}` {
		t.Fatalf("error StructuredContent number preservation = %s, err=%v", encoded, err)
	}
}

func TestManagerCallRejectsOversizedResponse(t *testing.T) {
	server := newManagerTestTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = io.WriteString(w, strings.Repeat("x", int(MinMaxResponseBytes)+1))
	}))
	group := GroupConfig{
		ID: "api", BaseURL: server.URL, Enabled: true, RequestTimeout: time.Second,
		MaxResponseBodyBytes: MinMaxResponseBytes,
		Tools:                []ToolConfig{{Name: "large", Enabled: true, Method: http.MethodGet, Path: "/large"}},
	}
	manager, err := NewManager(context.Background(), []GroupConfig{group}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("NewManager() error: %v", err)
	}
	result, err := manager.Call(context.Background(), "api", "large", nil)
	if err != nil {
		t.Fatalf("Call() error: %v", err)
	}
	if !result.IsError || result.Content[0].(*mcp.TextContent).Text != "upstream API response exceeded the configured limit" {
		t.Fatalf("oversized response = %#v", result)
	}
}

func TestManagerCallRejectsMissingAndUnsupportedArguments(t *testing.T) {
	server := newManagerTestTLSServer(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("upstream should not be called for invalid arguments")
	}))
	group := GroupConfig{
		ID: "api", BaseURL: server.URL, Enabled: true, RequestTimeout: time.Second,
		Tools: []ToolConfig{{
			Name: "item", Enabled: true, Method: http.MethodGet, Path: "/items/{id}",
			Parameters: []Parameter{{Name: "id", Argument: "id", In: "path", Required: true, Schema: map[string]any{"type": "string"}}},
		}},
	}
	manager, err := NewManager(context.Background(), []GroupConfig{group}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("NewManager() error: %v", err)
	}
	if _, err := manager.Call(context.Background(), "api", "item", nil); err == nil || !strings.Contains(err.Error(), `required argument "id" is missing`) {
		t.Fatalf("missing required argument error = %v", err)
	}
	if _, err := manager.Call(context.Background(), "api", "item", json.RawMessage(`{"id":{"nested":true}}`)); err == nil || !strings.Contains(err.Error(), `argument "id" has an unsupported value`) {
		t.Fatalf("unsupported argument error = %v", err)
	}
}

func TestManagerCallRejectsUnsafePathSegmentsWithoutUpstreamRequest(t *testing.T) {
	requests := make(chan struct{}, 1)
	server := newManagerTestTLSServer(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests <- struct{}{}
	}))
	group := GroupConfig{
		ID: "api", BaseURL: server.URL, Enabled: true, RequestTimeout: time.Second,
		Tools: []ToolConfig{{
			Name: "item", Enabled: true, Method: http.MethodGet, Path: "/items/{id}",
			Parameters: []Parameter{{Name: "id", Argument: "id", In: "path", Required: true, Schema: map[string]any{"type": "string"}}},
		}},
	}
	manager, err := NewManager(context.Background(), []GroupConfig{group}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("NewManager() error: %v", err)
	}
	for _, value := range []string{".", "..", "nested/item", `nested\\item`} {
		t.Run(value, func(t *testing.T) {
			if _, err := manager.Call(context.Background(), "api", "item", json.RawMessage(fmt.Sprintf(`{"id":%q}`, value))); err == nil || !strings.Contains(err.Error(), "single safe segment") {
				t.Fatalf("Call(%q) error = %v, want unsafe path rejection", value, err)
			}
			select {
			case <-requests:
				t.Fatal("unsafe path argument reached upstream")
			default:
			}
		})
	}
}

func TestNewManagerRejectsInvalidSchemasRegardlessOfEnablement(t *testing.T) {
	for _, tt := range []struct {
		name         string
		groupEnabled bool
		toolEnabled  bool
	}{
		{name: "disabled group and tool", groupEnabled: false, toolEnabled: false},
		{name: "disabled group", groupEnabled: false, toolEnabled: true},
		{name: "disabled tool", groupEnabled: true, toolEnabled: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			group := GroupConfig{
				ID: "api", BaseURL: "https://api.example.com", Enabled: tt.groupEnabled, RequestTimeout: time.Second,
				Tools: []ToolConfig{{
					Name: "invalid", Enabled: tt.toolEnabled, Method: http.MethodGet, Path: "/invalid",
					Parameters: []Parameter{{
						Name: "items", Argument: "items", In: "query",
						Schema: map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "x-mcp-header": "Items"},
					}},
				}},
			}
			if err := ValidateGroups([]GroupConfig{group}, nil); err == nil {
				t.Fatal("ValidateGroups() accepted an invalid schema in a disabled candidate")
			}
			if _, err := NewManager(context.Background(), []GroupConfig{group}, slog.New(slog.NewTextHandler(io.Discard, nil))); err == nil {
				t.Fatal("NewManager() accepted an invalid schema in a disabled candidate")
			}
		})
	}
}

func TestValidateGroupsRejectsUnsafeBaseURLAndToolPathSegments(t *testing.T) {
	for _, baseURL := range []string{
		"https://api.example.com/./v1",
		"https://api.example.com/../v1",
		`https://api.example.com/v1\child`,
	} {
		t.Run("base "+baseURL, func(t *testing.T) {
			group := GroupConfig{ID: "api", BaseURL: baseURL, RequestTimeout: time.Second}
			if err := ValidateGroup(&group); err == nil {
				t.Fatalf("ValidateGroup() accepted unsafe BaseURL %q", baseURL)
			}
		})
	}
	for _, path := range []string{"/./items", "/../items", `/items\child`} {
		t.Run("path "+path, func(t *testing.T) {
			tool := ToolConfig{Name: "item", Method: http.MethodGet, Path: path}
			if err := ValidateTool("api", &tool); err == nil {
				t.Fatalf("ValidateTool() accepted unsafe path %q", path)
			}
		})
	}
}

func TestValidateGroupRejectsToolHeaderThatConflictsWithSharedHeader(t *testing.T) {
	group := GroupConfig{
		ID: "api", BaseURL: "https://api.example.com", RequestTimeout: time.Second,
		Headers: map[string]string{"X-Trace": "shared"},
		Tools: []ToolConfig{{
			Name: "lookup", Method: http.MethodGet, Path: "/lookup",
			Parameters: []Parameter{{
				Name: "x-trace", Argument: "trace", In: "header", Schema: map[string]any{"type": "string"},
			}},
		}},
	}
	if err := ValidateGroup(&group); err == nil || !strings.Contains(err.Error(), "conflicts with a shared header") {
		t.Fatalf("ValidateGroup() error = %v, want shared-header conflict", err)
	}
	if err := ValidateGroups([]GroupConfig{group}, nil); err == nil {
		t.Fatal("ValidateGroups() accepted a tool header that shadows a shared header")
	}
}

func newManagerTestTLSServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	previous := http.DefaultTransport
	http.DefaultTransport = server.Client().Transport
	t.Cleanup(func() {
		http.DefaultTransport = previous
		server.Close()
	})
	return server
}
