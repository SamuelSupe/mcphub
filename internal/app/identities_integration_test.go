package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SamuelSupe/mcphub/v2/internal/config"
	"github.com/SamuelSupe/mcphub/v2/internal/configstore"
	"github.com/SamuelSupe/mcphub/v2/internal/sso"
	mcpauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestAdminCannotDisableLastAdministrator(t *testing.T) {
	a := newAdminTestApp(t)
	cfg := *a.currentConfig()
	cfg.Admin.Mode, cfg.Admin.PublicURL = "remote", "https://hub.example.com"
	cfg.Admin.RequiredScopes = []string{"mcphub:admin"}
	cfg.Auth.Issuer = "https://hub.example.com/sso"
	t.Setenv("TEST_IDENTITY_SECRET", "test-secret")
	cfg.Auth.SSO = &config.SSOConfig{Upstream: config.IdentityProvider{Protocol: "oidc", Issuer: "https://idp.example.com", ClientID: "enterprise", ClientSecretEnv: "TEST_IDENTITY_SECRET"}}
	if err := a.replaceRuntimeLocked(&cfg); err != nil {
		t.Fatal(err)
	}
	var serverErr error
	a.sso, serverErr = sso.New(t.Context(), &cfg, a.store)
	if serverErr != nil {
		t.Fatal(serverErr)
	}
	user, err := a.store.SyncIdentity(t.Context(), cfg.Auth.SSO.Upstream.Namespace(), "only-admin", "Only admin", nil, nil, false, true)
	if err != nil {
		t.Fatal(err)
	}
	permissions := config.IdentityPermissions{Roles: []string{"admin"}, Scopes: []string{"mcphub:admin"}}
	group, err := a.store.Identity(t.Context(), user.Groups[0])
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.store.UpdateIdentity(t.Context(), group.ID, group.Revision, true, permissions)
	if err != nil {
		t.Fatal(err)
	}
	a.adminAuth = newAdminAuthorization(cfg.Admin, cfg.Auth.Issuer, identityVerifier{a.store, cfg.Auth.Issuer})
	body, _ := json.Marshal(map[string]any{"enabled": false})
	req := httptest.NewRequest("PUT", cfg.Admin.PublicURL+"/api/v1/identities/"+user.ID, bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+user.ID)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("If-Match", revisionETag(user.Revision))
	w := httptest.NewRecorder()
	a.adminHandler().ServeHTTP(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("last administrator disabled: %d %s", w.Code, w.Body)
	}
	current, err := a.store.Identity(t.Context(), user.ID)
	if err != nil || !current.Enabled || current.Revision != user.Revision {
		t.Fatal("rejected edit changed the account", err)
	}
	req = httptest.NewRequest("GET", cfg.Admin.PublicURL+"/api/v1/identities", nil)
	req.Header.Set("Authorization", "Bearer "+user.ID)
	w = httptest.NewRecorder()
	a.adminHandler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("administrator lost access after rejected edit: %d", w.Code)
	}
}

type identityVerifier struct {
	store  *configstore.Store
	issuer string
}

func (v identityVerifier) Ready() bool { return true }
func (v identityVerifier) Verify(ctx context.Context, id string, _ *http.Request) (*mcpauth.TokenInfo, error) {
	p, err := v.store.EffectiveIdentity(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("%w", mcpauth.ErrInvalidToken)
	}
	return &mcpauth.TokenInfo{UserID: id, Scopes: p.Permissions.Scopes, Expiration: time.Now().Add(time.Hour), Extra: map[string]any{"issuer": v.issuer, "identity": &p}}, nil
}

func TestManagedIdentityRestrictsMCPCatalogCallsAndRunningRequests(t *testing.T) {
	var calls atomic.Int32
	started := make(chan struct{}, 1)
	cancelled := make(chan struct{}, 1)
	backend := mcp.NewServer(&mcp.Implementation{Name: "identity-test", Version: "1"}, nil)
	for _, name := range []string{"read", "write", "hidden", "slow"} {
		backend.AddTool(&mcp.Tool{Name: name, InputSchema: map[string]any{"type": "object"}}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			calls.Add(1)
			if req.Params.Name == "slow" {
				started <- struct{}{}
				<-ctx.Done()
				cancelled <- struct{}{}
				return nil, ctx.Err()
			}
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "ok"}}}, nil
		})
	}
	backend.AddResource(&mcp.Resource{Name: "private", URI: "memory://private"}, func(context.Context, *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		t.Error("unauthorized resource forwarded")
		return nil, nil
	})
	upstream := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return backend }, &mcp.StreamableHTTPOptions{Stateless: true, PropagateRequestCancellation: true}))
	defer upstream.Close()
	application := newAdminTestApp(t)
	cfg := *application.currentConfig()
	t.Setenv("TEST_SECRET", "test-secret")
	cfg.Auth = config.AuthConfig{Issuer: "https://hub.example.com/sso", SSO: &config.SSOConfig{Upstream: config.IdentityProvider{Protocol: "oidc", Issuer: "https://idp.example.com", ClientID: "enterprise", ClientSecretEnv: "TEST_SECRET"}, Clients: []config.SSOClient{{ID: "cli", RedirectURIs: []string{"http://127.0.0.1/oauth/callback"}, Resources: []string{cfg.Server.PublicURL}}}}}
	cfg.Server.RequestTimeout = config.Duration{Duration: 5 * time.Second}
	_, err := application.store.Create(t.Context(), configstore.Record{Enabled: true, Config: config.BackendConfig{ID: "db", URL: upstream.URL, AllowInsecureHTTP: true, Required: true, RequestTimeout: config.Duration{Duration: 5 * time.Second}, PublishedTools: []string{"read", "write", "hidden", "slow"}, ToolRules: []config.ToolRule{{Match: "*", Effect: "read"}, {Match: "write", Effect: "write"}}}})
	if err != nil {
		t.Fatal(err)
	}
	records, _ := application.store.List(t.Context())
	if err = application.replaceRuntimeLocked(configWithRecords(&cfg, records)); err != nil {
		t.Fatal(err)
	}
	application.sso, err = sso.New(t.Context(), &cfg, application.store)
	if err != nil {
		t.Fatal(err)
	}
	user, err := application.store.SyncIdentity(t.Context(), cfg.Auth.SSO.Upstream.Namespace(), "test-user", "Test user", nil, nil, false, false)
	if err != nil {
		t.Fatal(err)
	}
	permissions := config.IdentityPermissions{Access: []config.IdentityAccess{{EndpointID: "db", Tools: []string{"read", "write", "slow"}, ResourceRules: []config.ResourceRule{{Argument: "/project", AllowedValues: []string{"work"}}}}}}
	group, err := application.store.CreateGroup(t.Context(), user.Provider, "Tool users")
	if err != nil {
		t.Fatal(err)
	}
	group, err = application.store.UpdateIdentity(t.Context(), group.ID, group.Revision, true, permissions)
	if err != nil {
		t.Fatal(err)
	}
	user, err = application.store.UpdateUser(t.Context(), user.ID, user.Revision, true, []string{group.ID})
	if err != nil {
		t.Fatal(err)
	}
	application.auth = identityVerifier{application.store, cfg.Auth.Issuer}
	response := serveAdminJSON(t, application, "POST", "/api/v1/backends", []byte(`{"id":"unrelated","url":"https://unused.example/mcp","enabled":false}`), "")
	if response.Code != http.StatusCreated {
		t.Fatalf("save backend with managed identities: %d %s", response.Code, response.Body.String())
	}
	server := httptest.NewServer(application)
	defer server.Close()
	client := connectAppClient(t, t.Context(), server.URL+"/mcp", user.ID)
	defer client.Close()
	listed, err := client.ListTools(t.Context(), nil)
	if err != nil || len(listed.Tools) != 2 {
		t.Fatalf("user tool catalog: %v, %v", listed, err)
	}
	resources, err := client.ListResources(t.Context(), nil)
	if err != nil || len(resources.Resources) != 0 {
		t.Fatal("user resources leaked", err)
	}
	templates, err := client.ListResourceTemplates(t.Context(), nil)
	if err != nil || len(templates.ResourceTemplates) != 0 {
		t.Fatal("issued resource template leaked", err)
	}
	for _, call := range []struct{ name, project string }{{"db.hidden", "work"}, {"db.write", "work"}, {"db.read", "secret"}} {
		body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": call.name, "arguments": map[string]any{"project": call.project}}})
		response := doRequest(t, http.MethodPost, server.URL+"/mcp", user.ID, "", body)
		response.Body.Close()
		if response.StatusCode != http.StatusForbidden {
			t.Fatalf("denied call status: %d", response.StatusCode)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("denied calls reached backend")
	}
	if result, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: "db.read", Arguments: map[string]any{"project": "work"}}); err != nil || result.IsError {
		t.Fatal("allowed call rejected", err)
	}
	// Past users must not exhaust the view cache after their HTTP sessions close.
	for i := range 513 {
		other, err := application.store.SyncIdentity(t.Context(), cfg.Auth.SSO.Upstream.Namespace(), fmt.Sprintf("visitor-%d", i), "Visitor", nil, nil, false, false)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := application.store.UpdateUser(t.Context(), other.ID, other.Revision, true, []string{group.ID}); err != nil {
			t.Fatal(err)
		}
		visitor := connectAppClient(t, t.Context(), server.URL+"/mcp", other.ID)
		visitor.Close()
	}
	completed := make(chan error, 1)
	go func() {
		_, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: "db.slow", Arguments: map[string]any{"project": "work"}})
		completed <- err
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("slow request did not start")
	}
	body, _ := json.Marshal(map[string]any{"enabled": false})
	request := newAdminRequest(http.MethodPut, "/api/v1/identities/"+user.ID, body)
	request.Header.Set("If-Match", revisionETag(user.Revision))
	request.Header.Set("Content-Type", "application/json")
	response = httptest.NewRecorder()
	application.adminHandler().ServeHTTP(response, request)
	if response.Code != 200 {
		t.Fatalf("permission edit: %d %s", response.Code, response.Body.String())
	}
	select {
	case <-cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("revoked request kept running")
	}
	select {
	case <-completed:
	case <-time.After(2 * time.Second):
		t.Fatal("client request did not close")
	}
	if _, err = client.CallTool(t.Context(), &mcp.CallToolParams{Name: "db.read", Arguments: map[string]any{"project": "work"}}); err == nil {
		t.Fatal("disabled user kept calling")
	}
	request = newAdminRequest(http.MethodPut, "/api/v1/identities/"+user.ID, body)
	request.Header.Set("If-Match", revisionETag(user.Revision))
	request.Header.Set("Content-Type", "application/json")
	response = httptest.NewRecorder()
	application.adminHandler().ServeHTTP(response, request)
	if response.Code != 409 {
		t.Fatal("stale admin update accepted")
	}
}
