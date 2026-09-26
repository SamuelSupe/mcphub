package app

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	platform "runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SamuelSupe/mcphub/v2/internal/config"
	"github.com/SamuelSupe/mcphub/v2/internal/configstore"
	"github.com/SamuelSupe/mcphub/v2/internal/diagnostics"
)

func TestAdminSecurityHeadersAndHostPolicy(t *testing.T) {
	application := newAdminTestApp(t)
	handler := application.adminHandler()

	tests := []struct {
		name       string
		host       string
		remoteAddr string
		origin     string
		wantStatus int
	}{
		{name: "wrong host", host: "127.0.0.1:9999", remoteAddr: "127.0.0.1:34567", wantStatus: http.StatusForbidden},
		{name: "non-loopback peer", host: application.currentConfig().Admin.Listen, remoteAddr: "192.0.2.10:34567", wantStatus: http.StatusForbidden},
		{name: "wrong origin", host: application.currentConfig().Admin.Listen, remoteAddr: "127.0.0.1:34567", origin: "http://127.0.0.1:9999", wantStatus: http.StatusForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := newAdminRequest(http.MethodGet, "/api/v1/backends", nil)
			req.Host = tt.host
			req.RemoteAddr = tt.remoteAddr
			if tt.origin != "" {
				req.Header.Set("Origin", tt.origin)
			}
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, req)
			if recorder.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", recorder.Code, tt.wantStatus)
			}
			assertAdminSecurityHeaders(t, recorder.Header())
		})
	}

	req := newAdminRequest(http.MethodGet, "/api/v1/backends", nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("valid admin request status = %d, want 200; body=%s", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("valid API Cache-Control = %q, want no-store", got)
	}
	assertAdminSecurityHeaders(t, recorder.Header())
}

func TestNativeToolPolicyWorkbench(t *testing.T) {
	if os.Getenv("MCPHUB_POLICY_BROWSER_QA") != "1" || platform.GOOS != "darwin" {
		t.Skip("set MCPHUB_POLICY_BROWSER_QA=1 on macOS for interactive tool-policy verification")
	}
	var calls atomic.Int32
	upstream := mcp.NewServer(&mcp.Implementation{Name: "policy-browser-fixture", Version: "1"}, nil)
	for _, name := range []string{"get_project", "update_project", "delete_project"} {
		upstream.AddTool(&mcp.Tool{Name: name, Description: "Project operations / 项目操作", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			calls.Add(1)
			return &mcp.CallToolResult{}, nil
		})
	}
	backendHTTP := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return upstream }, &mcp.StreamableHTTPOptions{Stateless: true}))
	defer backendHTTP.Close()
	application := newAdminTestApp(t)
	input := backendInput{ID: "projects", URL: backendHTTP.URL, Required: true, AllowInsecureHTTP: true, PublishedTools: []string{"get_project", "update_project"}, RequiredScopes: []string{"projects:access"}, ToolRules: []config.ToolRule{{Match: "get_*", Effect: "read"}, {Match: "update_*", Effect: "write"}}}
	body, _ := json.Marshal(input)
	response := serveAdminJSON(t, application, "POST", "/api/v1/backends", body, "")
	if response.Code != 201 {
		t.Fatalf("create fixture: %s", response.Body.String())
	}
	group := []byte(`{"id":"rest-api","base_url":"https://api.example.com","enabled":true,"required_scopes":["api:access"]}`)
	if response := serveAdminJSON(t, application, "POST", "/api/v1/tool-groups", group, ""); response.Code != 201 {
		t.Fatalf("create group: %s", response.Body.String())
	}
	tool := []byte(`{"name":"lookup","description":"Look up a business record","method":"GET","path":"/items","enabled":true}`)
	if response := serveAdminJSON(t, application, "POST", "/api/v1/tool-groups/rest-api/tools", tool, ""); response.Code != 201 {
		t.Fatalf("create HTTP tool: %s", response.Body.String())
	}
	application.currentConfig().ClientAuthorization.Enabled = true
	uid, policy, err := application.store.ClientEndpointPolicy(t.Context(), "projects")
	if err != nil {
		t.Fatal(err)
	}
	for i := range 28 {
		subject := fmt.Sprintf("qa-user-%02d", i)
		if i == 27 {
			subject = "qa-alice"
		}
		input := configstore.ClientGrant{GrantBinding: configstore.GrantBinding{ClientID: fmt.Sprintf("ci_qa_editor_instance_%02d", i), EndpointUID: uid}, Issuer: application.currentConfig().Auth.Issuer, Subject: subject, Resource: application.currentConfig().Server.PublicURL, ClientName: fmt.Sprintf("QA editor %02d", i), EndpointID: "projects", EndpointPolicy: policy, AllowedScopes: []string{"projects:access"}, AllowedTools: []string{"get_project"}, Capabilities: configstore.GrantCapabilities{Tools: true}}
		if err := application.currentRuntime().hub.PrepareClientGrant(&input, input.AllowedScopes); err != nil {
			t.Fatal(err)
		}
		g, exchange, _, err := application.store.CreateClientGrant(t.Context(), input, "", time.Hour, 8*time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		if err := application.store.DecideClientGrant(t.Context(), g.GrantID, g.Issuer, g.Subject, true); err != nil {
			t.Fatal(err)
		}
		g, _, err = application.store.ExchangeClientGrant(t.Context(), g.GrantID, g.Issuer, g.Subject, exchange)
		if err != nil {
			t.Fatal(err)
		}
		ctx := diagnostics.Begin(t.Context(), fmt.Sprintf("qa-request-%02d", i))
		diagnostics.Update(ctx, func(r *diagnostics.Record) {
			r.Subject = subject
			r.ClientID = g.ClientID
			r.GrantID = g.GrantID
			r.Endpoint = "projects"
			r.Tool = "get_project"
			r.Method = "tools/call"
			r.StartedAt = time.Now().Add(-time.Duration(i+1) * 20 * time.Millisecond)
		})
		outcome := "success"
		status := 200
		if i%4 == 0 {
			outcome = "tool_error"
		}
		if i%4 == 1 {
			outcome = "scope_denied"
			status = 403
		}
		diagnostics.Outcome(ctx, outcome, "")
		application.requests.Finish(ctx, status)
	}
	done := make(chan struct{}, 1)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/__qa/done" && r.Method == http.MethodPost {
			select {
			case done <- struct{}{}:
			default:
			}
			w.WriteHeader(204)
			return
		}
		application.adminHandler().ServeHTTP(w, r)
	}))
	application.currentRuntime().cfg.Admin.Listen = server.Listener.Addr().String()
	server.Start()
	defer server.Close()
	t.Logf("POLICY_BROWSER_URL=%s/#tool-policies", server.URL)
	select {
	case <-done:
	case <-time.After(10 * time.Minute):
		t.Fatal("browser verification timed out")
	}
	if calls.Load() != 0 {
		t.Fatal("policy browsing or simulation executed a tool")
	}
}

func TestAdminBackendViewsRedactConfiguredSecrets(t *testing.T) {
	application := newAdminTestApp(t, configstore.Record{Config: config.BackendConfig{
		ID:  "alpha",
		URL: "https://alpha.example.com/mcp",
		Headers: map[string]string{
			"X-API-Key": "header-secret-value",
		},
		OAuth: &config.OAuthConfig{
			Type:         "client_credentials",
			Issuer:       "https://idp.example.com",
			ClientID:     "client",
			ClientSecret: "oauth-secret-value",
		},
	}, Enabled: true})
	req := newAdminRequest(http.MethodGet, "/api/v1/backends", nil)
	recorder := httptest.NewRecorder()
	application.adminHandler().ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/backends status = %d; body=%s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	if strings.Contains(body, "header-secret-value") || strings.Contains(body, "oauth-secret-value") {
		t.Fatalf("admin backend view leaked a configured secret: %s", body)
	}
	var payload struct {
		Backends []struct {
			Headers []struct {
				Name       string `json:"name"`
				Configured bool   `json:"configured"`
			} `json:"headers"`
			OAuth *struct {
				ClientSecretConfigured bool `json:"client_secret_configured"`
			} `json:"oauth"`
		} `json:"backends"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode backend view: %v", err)
	}
	if len(payload.Backends) != 1 || len(payload.Backends[0].Headers) != 1 || !payload.Backends[0].Headers[0].Configured || payload.Backends[0].OAuth == nil || !payload.Backends[0].OAuth.ClientSecretConfigured {
		t.Fatalf("redacted backend metadata = %#v, want configured markers", payload)
	}
}

func TestAdminBackendEditPreservesReplacesAndDeletesSecrets(t *testing.T) {
	application := newAdminTestApp(t, configstore.Record{Config: config.BackendConfig{
		ID:  "alpha",
		URL: "https://alpha.example.com/mcp",
		Headers: map[string]string{
			"X-Preserve": "preserved-header-secret",
			"X-Replace":  "old-header-secret",
			"X-Delete":   "deleted-header-secret",
		},
		OAuth: &config.OAuthConfig{
			Type:         "client_credentials",
			Issuer:       "https://idp.example.com",
			ClientID:     "client",
			ClientSecret: "old-oauth-secret",
		},
	}, Enabled: false})

	// A masked header/OAuth secret is represented by an omitted value. Omitting
	// a configured item entirely is the explicit delete operation.
	firstUpdate := []byte(`{"id":"alpha","url":"https://alpha.example.com/mcp","enabled":false,"headers":[{"name":"X-Preserve"},{"name":"X-Replace","value":"new-header-secret"}],"oauth":{"type":"client_credentials","issuer":"https://idp.example.com","client_id":"client","scopes":[]}}`)
	response := putAdminBackend(t, application, `"1"`, firstUpdate)
	if response.Code != http.StatusOK || response.Header().Get("ETag") != `"2"` {
		t.Fatalf("first secret edit status/ETag = %d/%q; body=%s", response.Code, response.Header().Get("ETag"), response.Body.String())
	}
	record, err := application.store.Get(context.Background(), "alpha")
	if err != nil {
		t.Fatalf("Get() after first secret edit: %v", err)
	}
	if record.Revision != 2 || record.Config.Headers["X-Preserve"] != "preserved-header-secret" || record.Config.Headers["X-Replace"] != "new-header-secret" {
		t.Fatalf("headers after preserve/replace = %#v", record.Config.Headers)
	}
	if _, ok := record.Config.Headers["X-Delete"]; ok {
		t.Fatalf("deleted header still present: %#v", record.Config.Headers)
	}
	if record.Config.OAuth == nil || record.Config.OAuth.ClientSecret != "old-oauth-secret" {
		t.Fatalf("OAuth secret was not preserved: %#v", record.Config.OAuth)
	}

	secondUpdate := []byte(`{"id":"alpha","url":"https://alpha.example.com/mcp","enabled":false,"headers":[{"name":"X-Preserve"},{"name":"X-Replace"}],"oauth":{"type":"client_credentials","issuer":"https://idp.example.com","client_id":"client","client_secret":"new-oauth-secret","scopes":[]}}`)
	response = putAdminBackend(t, application, `"2"`, secondUpdate)
	if response.Code != http.StatusOK || response.Header().Get("ETag") != `"3"` {
		t.Fatalf("second secret edit status/ETag = %d/%q; body=%s", response.Code, response.Header().Get("ETag"), response.Body.String())
	}
	record, err = application.store.Get(context.Background(), "alpha")
	if err != nil {
		t.Fatalf("Get() after OAuth replacement: %v", err)
	}
	if record.Config.OAuth == nil || record.Config.OAuth.ClientSecret != "new-oauth-secret" {
		t.Fatalf("OAuth replacement = %#v", record.Config.OAuth)
	}

	thirdUpdate := []byte(`{"id":"alpha","url":"https://alpha.example.com/mcp","enabled":false,"headers":[{"name":"X-Preserve"},{"name":"X-Replace"}]}`)
	response = putAdminBackend(t, application, `"3"`, thirdUpdate)
	if response.Code != http.StatusOK || response.Header().Get("ETag") != `"4"` {
		t.Fatalf("third secret edit status/ETag = %d/%q; body=%s", response.Code, response.Header().Get("ETag"), response.Body.String())
	}
	record, err = application.store.Get(context.Background(), "alpha")
	if err != nil {
		t.Fatalf("Get() after OAuth deletion: %v", err)
	}
	if record.Config.OAuth != nil {
		t.Fatalf("omitted OAuth was not deleted: %#v", record.Config.OAuth)
	}
	if record.Revision != 4 {
		t.Fatalf("revision after secret edits = %d, want 4", record.Revision)
	}

	events, err := application.store.Events(context.Background(), 20)
	if err != nil {
		t.Fatalf("Events() after secret edits: %v", err)
	}
	if len(events) != 4 {
		t.Fatalf("secret edit events = %#v, want create plus three updates", events)
	}
	for _, event := range events {
		if event.Actor != "local" {
			t.Fatalf("audit event actor = %q, want local: %#v", event.Actor, event)
		}
	}
}

func TestAdminRequiredBackendFailureLeavesRuntimeAndStoreUnchanged(t *testing.T) {
	application := newAdminTestApp(t)
	previous := application.currentRuntime()
	body := []byte(`{"id":"required-down","url":"http://127.0.0.1:1/mcp","required":true,"allow_insecure_http":true,"request_timeout":"50ms"}`)
	req := newAdminRequest(http.MethodPost, "/api/v1/backends", body)
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	application.adminHandler().ServeHTTP(recorder, req)
	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("required unavailable create status = %d, want 422; body=%s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), `"required_backend_unavailable"`) {
		t.Fatalf("required unavailable error body = %s", recorder.Body.String())
	}
	if application.currentRuntime() != previous {
		t.Fatal("required backend failure replaced the active runtime")
	}
	records, err := application.store.List(context.Background())
	if err != nil {
		t.Fatalf("List() after rejected create: %v", err)
	}
	if len(records) != 0 {
		t.Fatalf("store after rejected required create = %#v, want empty", records)
	}
}

func TestAdminCanCreateDisabledBackendWithRevisionETag(t *testing.T) {
	application := newAdminTestApp(t)
	body := []byte(`{"id":"optional","url":"http://127.0.0.1:1/mcp","enabled":false,"allow_insecure_http":true,"request_timeout":"50ms"}`)
	req := newAdminRequest(http.MethodPost, "/api/v1/backends", body)
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	application.adminHandler().ServeHTTP(recorder, req)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("disabled backend create status = %d, want 201; body=%s", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("ETag"); got != `"1"` {
		t.Fatalf("create ETag = %q, want %q", got, `"1"`)
	}
	var view backendView
	if err := json.Unmarshal(recorder.Body.Bytes(), &view); err != nil {
		t.Fatalf("decode created backend view: %v", err)
	}
	if view.ID != "optional" || view.Enabled || view.Revision != 1 || view.Runtime.State != "disabled" {
		t.Fatalf("created backend view = %#v", view)
	}
	record, err := application.store.Get(context.Background(), "optional")
	if err != nil {
		t.Fatalf("Get() created backend: %v", err)
	}
	if record.Revision != 1 || record.Enabled {
		t.Fatalf("stored created backend = %#v, want disabled revision 1", record)
	}
}

func TestAdminStaleRevisionRejectsWriteWithoutChangingCurrentRecord(t *testing.T) {
	application := newAdminTestApp(t, configstore.Record{Config: config.BackendConfig{
		ID:  "alpha",
		URL: "https://alpha.example.com/mcp",
	}, Enabled: false})
	current, err := application.store.Get(context.Background(), "alpha")
	if err != nil {
		t.Fatalf("Get() initial backend: %v", err)
	}
	current.Config.URL = "https://new.example.com/mcp"
	if _, err := application.store.Update(context.Background(), current, 1); err != nil {
		t.Fatalf("Update() current backend: %v", err)
	}

	body := []byte(`{"id":"alpha","url":"https://stale.example.com/mcp","enabled":false}`)
	response := putAdminBackend(t, application, `"1"`, body)
	if response.Code != http.StatusConflict {
		t.Fatalf("stale admin update status = %d, want 409; body=%s", response.Code, response.Body.String())
	}
	record, err := application.store.Get(context.Background(), "alpha")
	if err != nil {
		t.Fatalf("Get() after stale admin update: %v", err)
	}
	if record.Revision != 2 || record.Config.URL != "https://new.example.com/mcp" {
		t.Fatalf("record after stale admin update = %#v, want revision 2 and current URL", record)
	}
}

func newAdminTestApp(t *testing.T, records ...configstore.Record) *App {
	t.Helper()
	const keyEnv = "MCPHUB_ADMIN_APP_TEST_KEY"
	key := make([]byte, 32)
	t.Setenv(keyEnv, base64.StdEncoding.EncodeToString(key))
	dir := t.TempDir()
	cfg := &config.Config{
		Server: config.ServerConfig{
			Listen:              ":8080",
			PublicURL:           "https://hub.example.com/mcp",
			PageSize:            10,
			RequestTimeout:      config.Duration{Duration: 200 * time.Millisecond},
			DrainTimeout:        config.Duration{Duration: time.Second},
			RefreshInterval:     config.Duration{Duration: 100 * time.Millisecond},
			CatalogTTL:          config.Duration{Duration: time.Second},
			MaxRequestBodyBytes: 1 << 20,
		},
		Auth: config.AuthConfig{Issuer: "https://idp.example.com"},
		Admin: config.AdminConfig{
			Enabled:          true,
			Listen:           "127.0.0.1:8081",
			DatabasePath:     filepath.Join(dir, "config.db"),
			EncryptionKeyEnv: keyEnv,
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	rt, err := newRuntime(ctx, cfg, logger, false)
	if err != nil {
		cancel()
		t.Fatalf("newRuntime() error: %v", err)
	}
	store, err := configstore.Open(ctx, cfg.Admin.DatabasePath, key)
	if err != nil {
		rt.close()
		cancel()
		t.Fatalf("configstore.Open() error: %v", err)
	}
	for _, record := range records {
		if _, err := store.Create(ctx, record); err != nil {
			_ = store.Close()
			rt.close()
			cancel()
			t.Fatalf("store.Create() error: %v", err)
		}
	}
	application := &App{ctx: ctx, cancel: cancel, logger: logger, auth: staticVerifier{}, runtime: rt, store: store}
	t.Cleanup(application.Close)
	return application
}

func newAdminRequest(method, path string, body []byte) *http.Request {
	req := httptest.NewRequest(method, "http://127.0.0.1:8081"+path, bytes.NewReader(body))
	req.Host = "127.0.0.1:8081"
	req.RemoteAddr = "127.0.0.1:34567"
	return req
}

func putAdminBackend(t *testing.T, application *App, ifMatch string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	req := newAdminRequest(http.MethodPut, "/api/v1/backends/alpha", body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("If-Match", ifMatch)
	recorder := httptest.NewRecorder()
	application.adminHandler().ServeHTTP(recorder, req)
	return recorder
}

func assertAdminSecurityHeaders(t *testing.T, headers http.Header) {
	t.Helper()
	for _, name := range []string{
		"Content-Security-Policy",
		"X-Content-Type-Options",
		"Referrer-Policy",
		"Cross-Origin-Opener-Policy",
		"Permissions-Policy",
	} {
		if headers.Get(name) == "" {
			t.Errorf("missing admin security header %s", name)
		}
	}
}

func TestToolPublicationAndResourcePolicyThroughAdmin(t *testing.T) {
	var calls atomic.Int32
	upstream := mcp.NewServer(&mcp.Implementation{Name: "publication-test", Version: "1"}, nil)
	handler := func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		calls.Add(1)
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(req.Params.Arguments)}}}, nil
	}
	upstream.AddTool(&mcp.Tool{Name: "search", InputSchema: map[string]any{"type": "object"}}, handler)
	backendHTTP := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return upstream }, &mcp.StreamableHTTPOptions{Stateless: true}))
	defer backendHTTP.Close()
	application := newAdminTestApp(t)
	input := backendInput{ID: "alpha", URL: backendHTTP.URL, Required: true, AllowInsecureHTTP: true, RequiredScopes: []string{"mcp:alpha"}, ToolRules: []config.ToolRule{{Match: "*", Effect: "read", RequiredScopes: []string{"mcp:alpha:audit"}, ResourceRules: []config.ResourceRule{{Argument: "/project", AllowedValues: []string{"work"}}, {Argument: "/body/database", AllowedValues: []string{"reports"}}}}}}
	etag := ""
	save := func(method string) {
		t.Helper()
		body, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		target := "/api/v1/backends"
		if method == http.MethodPut {
			target += "/alpha"
		}
		response := serveAdminJSON(t, application, method, target, body, etag)
		if response.Code != http.StatusCreated && response.Code != http.StatusOK {
			t.Fatalf("save: %d %s", response.Code, response.Body.String())
		}
		etag = response.Header().Get("ETag")
	}
	save(http.MethodPost)
	hubHTTP := httptest.NewServer(application)
	defer hubHTTP.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	client := connectAppClient(t, ctx, hubHTTP.URL+"/mcp", "privileged")
	defer client.Close()
	assertCatalog := func(want int) {
		t.Helper()
		fresh := connectAppClient(t, ctx, hubHTTP.URL+"/mcp", "privileged")
		defer fresh.Close()
		list, err := fresh.ListTools(ctx, nil)
		if err != nil || len(list.Tools) != want {
			t.Fatalf("catalog: %#v, %v; want %d", list, err, want)
		}
	}
	args := json.RawMessage(`{"project":"work","body":{"database":"reports"}}`)
	checkAccess := func(scopes []string, arguments json.RawMessage, outcome, failedCode string) {
		t.Helper()
		before := calls.Load()
		body, _ := json.Marshal(accessCheckInput{Endpoint: "alpha", Tool: "search", Scopes: scopes, Arguments: arguments})
		response := serveAdminJSON(t, application, http.MethodPost, "/api/v1/access-check", body, "")
		var result accessCheckResult
		if response.Code != 200 || json.Unmarshal(response.Body.Bytes(), &result) != nil || !result.Simulated || result.Outcome != outcome {
			t.Fatalf("access check: %d %s", response.Code, response.Body.String())
		}
		if failedCode != "" {
			found := false
			for _, step := range result.Checks {
				found = found || (step.Code == failedCode && !step.Passed)
			}
			if !found {
				t.Fatalf("missing denial %s: %+v", failedCode, result)
			}
		}
		if before != calls.Load() {
			t.Fatal("permission simulation executed an upstream tool")
		}
	}
	allScopes := []string{"mcp:alpha", "mcp:alpha:audit"}
	policyResponse := serveAdminJSON(t, application, http.MethodGet, "/api/v1/tool-policies?endpoint=alpha", nil, "")
	var policies endpointPolicies
	if policyResponse.Code != 200 || json.Unmarshal(policyResponse.Body.Bytes(), &policies) != nil || len(policies.Tools) != 1 || policies.Tools[0].Published || policies.Tools[0].Effect != "read" || len(policies.Tools[0].RequiredScopes) != 2 {
		t.Fatalf("unpublished tool policy: %s", policyResponse.Body.String())
	}
	checkAccess(allScopes, args, "denied", "tool_published")
	assertDenied := func(name string, arguments json.RawMessage) {
		t.Helper()
		before := calls.Load()
		fresh := connectAppClient(t, ctx, hubHTTP.URL+"/mcp", "privileged")
		defer fresh.Close()
		result, err := fresh.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: arguments})
		if err == nil && !result.IsError {
			t.Fatalf("accepted %s: %s", name, arguments)
		}
		if calls.Load() != before {
			t.Fatal("denied request reached upstream")
		}
	}
	assertCatalog(0)
	assertDenied("alpha.search", args)
	input.PublishedTools = []string{"search"}
	save(http.MethodPut)
	stored, err := application.store.Get(ctx, "alpha")
	if err != nil || len(stored.Config.PublishedTools) != 1 || len(stored.Config.ToolRules[0].ResourceRules) != 2 {
		t.Fatalf("policy not persisted: %#v, %v", stored, err)
	}
	assertCatalog(1)
	checkAccess(allScopes[:1], args, "denied", "required_scopes")
	checkAccess(allScopes, json.RawMessage(`{"project":"other"}`), "denied", "resource_allowed")
	checkAccess(allScopes, args, "checks_passed", "")
	unprivileged := connectAppClient(t, ctx, hubHTTP.URL+"/mcp", "allowed")
	defer unprivileged.Close()
	if _, err := unprivileged.CallTool(ctx, &mcp.CallToolParams{Name: "alpha.search", Arguments: args}); err == nil {
		t.Fatal("missing tool scope allowed")
	}
	for _, bad := range []string{`{}`, `{"project":null}`, `{"project":"other","body":{"database":"reports"}}`, `{"project":["work","other"],"body":{"database":"reports"}}`, `{"project":"work","body":{"database":"private"}}`} {
		assertDenied("alpha.search", json.RawMessage(bad))
	}
	if calls.Load() != 0 {
		t.Fatal("unauthorized calls reached upstream")
	}
	valid := json.RawMessage(`{"project":"other","project":"work","body":{"database":"reports"},"n":9007199254740993}`)
	result, err := client.CallTool(ctx, &mcp.CallToolParams{Name: "alpha.search", Arguments: valid})
	if err != nil || result.IsError || calls.Load() != 1 {
		t.Fatalf("allowed call: %#v, %v", result, err)
	}
	forwarded := result.Content[0].(*mcp.TextContent).Text
	if strings.Count(forwarded, `"project"`) != 1 || !strings.Contains(forwarded, "9007199254740993") {
		t.Fatalf("upstream received ambiguous or rounded arguments: %s", forwarded)
	}
	upstream.AddTool(&mcp.Tool{Name: "new_search", InputSchema: map[string]any{"type": "object"}}, handler)
	backendClient, _ := application.currentRuntime().manager.Client("alpha")
	for len(backendClient.Catalog().Tools) != 2 {
		select {
		case <-ctx.Done():
			t.Fatal("new catalog was not discovered")
		case <-time.After(10 * time.Millisecond):
		}
	}
	assertCatalog(1)
	assertDenied("alpha.new_search", args)
	input.ToolRules[0].ResourceRules[0].AllowedValues = []string{"ops"}
	save(http.MethodPut)
	assertDenied("alpha.search", args)
	input.PublishedTools = nil
	save(http.MethodPut)
	assertCatalog(0)
	assertDenied("alpha.search", json.RawMessage(`{"project":"ops","body":{"database":"reports"}}`))
	input.PublishedTools = []string{"search"}
	input.ToolRules = append(input.ToolRules, config.ToolRule{Match: "search", Effect: "write"})
	save(http.MethodPut)
	checkAccess(allScopes, json.RawMessage(`{"project":"ops","body":{"database":"reports"}}`), "denied", "approval_service")
	disabled := false
	input.Enabled = &disabled
	save(http.MethodPut)
	assertCatalog(0)
	assertDenied("alpha.search", args)
	checkAccess(allScopes, args, "denied", "endpoint_enabled")
}
