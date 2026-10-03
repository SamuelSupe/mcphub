package app

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SamuelSupe/mcphub/v2/internal/config"
	"github.com/SamuelSupe/mcphub/v2/internal/configstore"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/oauth2"
)

func TestNativeOAuthMultipleServices(t *testing.T) {
	var calls atomic.Int32
	betaStarted, betaRelease := make(chan struct{}), make(chan struct{})
	upstream := mcp.NewServer(&mcp.Implementation{Name: "native-test", Version: "1"}, nil)
	upstream.AddTool(&mcp.Tool{Name: "echo", InputSchema: map[string]any{"type": "object"}}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		calls.Add(1)
		var args struct {
			Wait bool `json:"wait"`
		}
		_ = json.Unmarshal(req.Params.Arguments, &args)
		if args.Wait {
			close(betaStarted)
			select {
			case <-betaRelease:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "ok"}}}, nil
	})
	backend := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return upstream }, &mcp.StreamableHTTPOptions{Stateless: true}))
	defer backend.Close()
	t.Setenv("MCPHUB_CONFIG_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	path := filepath.Join(t.TempDir(), "config.yaml")
	yaml := "server:\n  public_url: https://hub.example.com/mcp\nauth:\n  mode: builtin\nadmin:\n  enabled: true\n  database_path: ./config.db\nclient_authorization:\n  enabled: true\n  require_client_grant: true\nbackends:\n"
	for _, id := range []string{"alpha", "beta"} {
		yaml += fmt.Sprintf("  - id: %s\n    url: %s\n    allow_insecure_http: true\n    required: true\n    required_scopes: [mcp:%s]\n    published_tools: [echo]\n    tool_rules:\n      - match: '*'\n        effect: read\n", id, backend.URL, id)
	}
	if err := os.WriteFile(path, []byte(yaml), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadStatic(path)
	if err != nil {
		t.Fatal(err)
	}
	application, err := New(t.Context(), cfg, path, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer application.Close()
	user, err := application.store.CreateLocalAccount(t.Context(), "admin", "Administrator", "native-long-password", true)
	if err != nil {
		t.Fatal(err)
	}
	group, err := application.store.CreateGroup(t.Context(), config.PermissionGroupProvider, "Agent services")
	if err != nil {
		t.Fatal(err)
	}
	_, err = application.store.UpdateIdentity(t.Context(), group.ID, group.Revision, true, config.IdentityPermissions{Scopes: []string{"mcp:alpha", "mcp:beta"}, Access: []config.IdentityAccess{{EndpointID: "alpha", Tools: []string{"echo"}}, {EndpointID: "beta", Tools: []string{"echo"}}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = application.store.UpdateUser(t.Context(), user.ID, user.Revision, true, append(user.Groups, group.ID)); err != nil {
		t.Fatal(err)
	}
	registered := []config.SSOClient{{ID: "desktop-a", Name: "Desktop A", RedirectURIs: []string{"http://127.0.0.1/callback"}, Resources: []string{cfg.Server.PublicURL}, RequireConsent: true}}
	if _, err = application.store.SaveNativeClients(t.Context(), 0, registered); err != nil {
		t.Fatal(err)
	}
	request := func(method, path, origin string, form url.Values, cookies []*http.Cookie) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, "https://hub.example.com"+path, strings.NewReader(form.Encode()))
		if form != nil {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		for _, c := range cookies {
			req.AddCookie(c)
		}
		response := httptest.NewRecorder()
		application.ServeHTTP(response, req)
		return response
	}
	verifier := oauth2.GenerateVerifier()
	query := url.Values{"client_id": {"desktop-a"}, "redirect_uri": {"http://127.0.0.1:17331/callback"}, "resource": {cfg.Server.PublicURL}, "response_type": {"code"}, "code_challenge_method": {"S256"}, "code_challenge": {oauth2.S256ChallengeFromVerifier(verifier)}, "state": {"caller-state"}, "scope": {"mcp:alpha mcp:beta offline_access"}}
	authorization := request("GET", "/sso/authorize?"+query.Encode(), "", nil, nil)
	if authorization.Code != 200 {
		t.Fatal(authorization.Code, authorization.Body.String())
	}
	state := ""
	for _, c := range authorization.Result().Cookies() {
		if strings.HasPrefix(c.Name, "__Host-mcphub-sso-") {
			state = strings.TrimPrefix(c.Name, "__Host-mcphub-sso-")
		}
	}
	login := request("POST", "/sso/login", "https://hub.example.com", url.Values{"state": {state}, "username": {"admin"}, "password": {"native-long-password"}}, authorization.Result().Cookies())
	if login.Code != 200 || !strings.Contains(login.Body.String(), "tool:alpha") || !strings.Contains(login.Body.String(), "tool:beta") {
		t.Fatal("consent catalog", login.Code, login.Body.String())
	}
	if !strings.Contains(login.Header().Get("Content-Security-Policy"), "form-action 'self' http://127.0.0.1:17331/callback;") {
		t.Fatal("browser OAuth callback blocked by form policy", login.Header())
	}
	id := ""
	for _, c := range login.Result().Cookies() {
		if strings.HasPrefix(c.Name, "__Host-mcphub-consent-") {
			id = strings.TrimPrefix(c.Name, "__Host-mcphub-consent-")
		}
	}
	form := url.Values{"request": {id}, "decision": {"confirm"}, "ttl": {"900"}, "tool:alpha": {"echo"}, "tool:beta": {"echo"}}
	if bad := request("POST", "/sso/consent", "https://other.example.com", form, login.Result().Cookies()); bad.Code != 400 && bad.Code != 403 {
		t.Fatal("cross-origin consent accepted")
	}
	// Language changes and recoverable form errors must keep the user's scope
	// choices without consuming the one-time authorization request.
	form.Set("decision", "language")
	form.Set("lang", "zh-CN")
	translated := request("POST", "/sso/consent", "https://hub.example.com", form, login.Result().Cookies())
	if translated.Code != 200 || !strings.Contains(translated.Body.String(), `lang="en"`) || strings.Count(translated.Body.String(), `value="echo" checked`) != 2 {
		t.Fatal("language change lost consent selections", translated.Code, translated.Body.String())
	}
	form.Set("decision", "confirm")
	form.Set("lang", "en")
	form.Set("rules:alpha", "invalid resource JSON")
	invalid := request("POST", "/sso/consent", "https://hub.example.com", form, login.Result().Cookies())
	if invalid.Code != 200 || !strings.Contains(invalid.Body.String(), "Invalid resource conditions") || !strings.Contains(invalid.Body.String(), "invalid resource JSON</textarea>") || strings.Count(invalid.Body.String(), `value="echo" checked`) != 2 || !strings.Contains(invalid.Body.String(), `value="900" selected`) {
		t.Fatal("invalid form lost consent selections", invalid.Code, invalid.Body.String())
	}
	form.Del("rules:alpha")
	consent := request("POST", "/sso/consent", "https://hub.example.com", form, login.Result().Cookies())
	if consent.Code != 303 {
		t.Fatal("confirm", consent.Code, consent.Body.String())
	}
	if repeat := request("POST", "/sso/consent", "https://hub.example.com", form, login.Result().Cookies()); repeat.Code != 400 {
		t.Fatal("consent replay accepted")
	}
	callback, _ := url.Parse(consent.Header().Get("Location"))
	if callback.Query().Get("state") != "caller-state" {
		t.Fatal("callback state lost")
	}
	tokenResponse := request("POST", "/sso/token", "", url.Values{"grant_type": {"authorization_code"}, "code": {callback.Query().Get("code")}, "code_verifier": {verifier}, "client_id": {"desktop-a"}, "redirect_uri": {"http://127.0.0.1:17331/callback"}, "resource": {cfg.Server.PublicURL}}, nil)
	var token struct {
		Access  string `json:"access_token"`
		Refresh string `json:"refresh_token"`
	}
	if tokenResponse.Code != 200 || json.Unmarshal(tokenResponse.Body.Bytes(), &token) != nil {
		t.Fatal(tokenResponse.Code, tokenResponse.Body.String())
	}
	grants, err := application.store.ListClientGrants(t.Context(), cfg.Auth.Issuer, user.ID)
	if err != nil || len(grants) != 2 || grants[0].SessionID != grants[1].SessionID {
		t.Fatal("grants not in one connection", grants, err)
	}
	hub := httptest.NewServer(application)
	defer hub.Close()
	session := connectAppClient(t, t.Context(), hub.URL+"/mcp", token.Access)
	defer session.Close()
	list, err := session.ListTools(t.Context(), nil)
	if err != nil || !slices.ContainsFunc(list.Tools, func(tool *mcp.Tool) bool { return tool.Name == "alpha.echo" }) || !slices.ContainsFunc(list.Tools, func(tool *mcp.Tool) bool { return tool.Name == "beta.echo" }) {
		t.Fatal("native catalog", list, err)
	}
	for _, id := range []string{"alpha", "beta"} {
		result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: id + ".echo", Arguments: map[string]any{}})
		if err != nil || result.IsError {
			t.Fatal("native call", id, result, err)
		}
	}
	betaDone := make(chan error, 1)
	go func() {
		result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "beta.echo", Arguments: map[string]any{"wait": true}})
		if err == nil && result.IsError {
			err = fmt.Errorf("beta failed: %v", result)
		}
		betaDone <- err
	}()
	select {
	case <-betaStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("beta call did not start")
	}
	alpha := grants[slices.IndexFunc(grants, func(g configstore.ClientGrant) bool { return g.EndpointID == "alpha" })]
	if err = application.store.RevokeClientGrant(t.Context(), alpha.GrantID, cfg.Auth.Issuer, user.ID); err != nil {
		t.Fatal(err)
	}
	application.currentRuntime().hub.PruneClientGrantViews(t.Context())
	application.currentRuntime().hub.PruneClientGrantViews(t.Context())
	close(betaRelease)
	select {
	case err := <-betaDone:
		if err != nil {
			t.Fatal("unaffected in-flight service canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("beta call did not complete")
	}
	before := calls.Load()
	if _, err = session.CallTool(t.Context(), &mcp.CallToolParams{Name: "alpha.echo", Arguments: map[string]any{}}); err == nil {
		t.Fatal("revoked service accepted")
	}
	if calls.Load() != before {
		t.Fatal("revoked call reached upstream")
	}
	if result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "beta.echo", Arguments: map[string]any{}}); err != nil || result.IsError {
		t.Fatal("other service lost access", result, err)
	}
	raw := httptest.NewRequest("POST", "https://hub.example.com/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	raw.Header.Set("Authorization", "Bearer "+token.Access)
	raw.Header.Set(configstore.GrantHeader, "cannot-widen-native-access")
	denied := httptest.NewRecorder()
	application.ServeHTTP(denied, raw)
	if denied.Code != 401 && denied.Code != 403 {
		t.Fatal("mixed native grant/header accepted", denied.Code)
	}
	registered[0].Name = "Changed desktop"
	if _, err = application.store.SaveNativeClients(t.Context(), 1, registered); err != nil {
		t.Fatal(err)
	}
	if _, err = application.sso.Verifier(cfg.Server.PublicURL).Verify(t.Context(), token.Access, nil); err == nil {
		t.Fatal("changed client retained token")
	}
	refresh := request("POST", "/sso/token", "", url.Values{"grant_type": {"refresh_token"}, "refresh_token": {token.Refresh}, "client_id": {"desktop-a"}, "resource": {cfg.Server.PublicURL}}, nil)
	if refresh.Code != 400 {
		t.Fatal("changed client refreshed token", refresh.Code)
	}
}
