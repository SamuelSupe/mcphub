package client

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SamuelSupe/mcphub/v2/internal/app"
	"github.com/SamuelSupe/mcphub/v2/internal/config"
	"github.com/SamuelSupe/mcphub/v2/internal/configstore"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The real builtin issuer, ordinary browser consent, grant exchange and connector
// run together: a successful login alone must never expose business tools.
func TestDevicePairAndInteractiveAgent(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	var application *app.App
	var controlRequests atomic.Int64
	hub := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/v1/client-") {
			controlRequests.Add(1)
		}
		application.ServeHTTP(w, r)
	}))
	defer hub.Close()
	backend := mcp.NewServer(&mcp.Implementation{Name: "pair-backend", Version: "1"}, nil)
	backend.AddTool(&mcp.Tool{Name: "read", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "authorized read"}}}, nil
	})
	upstream := httptest.NewTLSServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return backend }, &mcp.StreamableHTTPOptions{Stateless: true}))
	defer upstream.Close()
	original := http.DefaultTransport
	http.DefaultTransport = hub.Client().Transport
	defer func() { http.DefaultTransport = original }()
	dir := t.TempDir()
	t.Setenv("MCPHUB_CONFIG_KEY", base64.StdEncoding.EncodeToString(make([]byte, 32)))
	path := filepath.Join(dir, "config.yaml")
	yaml := "server:\n  public_url: " + hub.URL + "/mcp\nauth:\n  mode: builtin\nadmin:\n  enabled: true\n  database_path: ./config.db\nclient_authorization:\n  enabled: true\n  require_client_grant: true\nbackends:\n- id: alpha\n  url: " + upstream.URL + "\n  required_scopes: [mcp:alpha.read]\n  published_tools: [read]\n  tool_rules:\n  - match: read\n    effect: read\n"
	if err := os.WriteFile(path, []byte(yaml), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadStatic(path)
	if err != nil {
		t.Fatal(err)
	}
	application, err = app.New(ctx, cfg, path, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer application.Close()
	identityStore, err := configstore.Open(ctx, filepath.Join(dir, "config.db"), make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	defer identityStore.Close()
	user, err := identityStore.CreateLocalAccount(ctx, "admin", "Pair tester", "long-test-password", true)
	if err != nil {
		t.Fatal(err)
	}
	group, err := identityStore.CreateGroup(ctx, config.LocalIdentityProvider, "Readers")
	if err != nil {
		t.Fatal(err)
	}
	_, err = identityStore.UpdateIdentity(ctx, group.ID, group.Revision, true, config.IdentityPermissions{Scopes: []string{"mcp:alpha.read"}, Access: []config.IdentityAccess{{EndpointID: "alpha", Tools: []string{"read"}}}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = identityStore.UpdateUser(ctx, user.ID, user.Revision, true, append(user.Groups, group.ID))
	if err != nil {
		t.Fatal(err)
	}
	jar, _ := cookiejar.New(nil)
	browser := httpClient(hub.Client(), 10*time.Second)
	browser.Jar = jar
	browserRequest := func(path string, body any, csrf string) map[string]any {
		t.Helper()
		var raw []byte
		if body != nil {
			raw, _ = json.Marshal(body)
		}
		method := http.MethodGet
		if body != nil {
			method = http.MethodPost
		}
		r, _ := http.NewRequestWithContext(ctx, method, hub.URL+"/client-auth/"+path, strings.NewReader(string(raw)))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Origin", hub.URL)
		r.Header.Set("X-MCPHub-CSRF", csrf)
		resp, e := browser.Do(r)
		if e != nil {
			t.Fatal(e)
		}
		defer resp.Body.Close()
		data, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != 200 && resp.StatusCode != 204 {
			t.Fatalf("%s: HTTP %d %s", path, resp.StatusCode, data)
		}
		out := map[string]any{}
		if len(data) > 0 {
			if e = json.Unmarshal(data, &out); e != nil {
				t.Fatal(e)
			}
		}
		return out
	}
	browserRequest("auth/local-login", map[string]string{"username": "admin", "password": "long-test-password"}, "")
	session := browserRequest("auth/session", nil, "")
	csrf := session["csrf"].(string)
	store := &Store{Dir: filepath.Join(dir, "bridge")}
	opts := PairOptions{Server: hub.URL + "/mcp", Profile: "work", ClientOptions: ClientOptions{Name: "Agent", Endpoint: "alpha", HTTPClient: hub.Client()}}
	approve := func(status PairStatus) {
		t.Helper()
		view := browserRequest("api/device?user_code="+status.UserCode, nil, "")
		for _, secret := range []string{"device_code", "exchange_credential", "broker_session_proof"} {
			if _, ok := view[secret]; ok {
				t.Fatal("browser secret", secret)
			}
		}
		browserRequest("api/device/confirm", map[string]any{"user_code": status.UserCode, "endpoint_id": "alpha", "allowed_tools": []string{"read"}, "ttl_seconds": 3600}, csrf)
	}
	pair, err := PairStart(ctx, store, opts)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := PairFinish(ctx, store, pair.RequestID, false, hub.Client())
	if err != nil || pending.Status != "pending_user" {
		t.Fatal("login alone", pending, err)
	}
	approve(pair)
	ready, err := PairFinish(ctx, store, pair.RequestID, true, hub.Client())
	if err != nil || ready.Status != "ready" {
		t.Fatal("ready", ready, err)
	}
	again, err := PairFinish(ctx, store, pair.RequestID, false, hub.Client())
	if err != nil || again.ClientID != ready.ClientID || again.Status != "ready" {
		t.Fatal("idempotent status", again, err)
	}
	// A second connection uses the same broker without replacing the first client.
	second, err := PairStart(ctx, store, opts)
	if err != nil {
		t.Fatal(err)
	}
	approve(second)
	second, err = PairFinish(ctx, store, second.RequestID, true, hub.Client())
	if err != nil || second.Status != "ready" {
		t.Fatal("second", second, err)
	}
	clients, _, err := ListClients(ctx, store, "work", hub.Client())
	if err != nil || len(clients) != 2 {
		t.Fatal("independent clients", clients, err)
	}
	// Explicit pairing can replace an ended broker; live connections are never transferred.
	oldProfile, err := store.load("work")
	if err != nil {
		t.Fatal(err)
	}
	if err = identityStore.RevokeBrokerSession(ctx, oldProfile.Broker.SessionID, cfg.Auth.Issuer, user.ID, "", true); err != nil {
		t.Fatal(err)
	}
	renewed, err := PairStart(ctx, store, opts)
	if err != nil {
		t.Fatal(err)
	}
	approve(renewed)
	renewed, err = PairFinish(ctx, store, renewed.RequestID, true, hub.Client())
	if err != nil || renewed.Status != "ready" {
		t.Fatal("explicit renewal of ended broker", renewed, err)
	}
	currentProfile, err := store.load("work")
	if err != nil || currentProfile.Broker.SessionID == oldProfile.Broker.SessionID || len(currentProfile.Broker.Clients) != 1 {
		t.Fatal("ended grants transferred", err)
	}
	// Denial leaves the working profile and all of its credentials untouched.
	before, _ := json.Marshal(currentProfile)
	denied, err := PairStart(ctx, store, opts)
	if err != nil {
		t.Fatal(err)
	}
	browserRequest("api/device/deny", map[string]string{"user_code": denied.UserCode}, csrf)
	denied, err = PairFinish(ctx, store, denied.RequestID, true, hub.Client())
	if err != nil || denied.Status != "denied" {
		t.Fatal("denied pairing", denied, err)
	}
	afterProfile, _ := store.load("work")
	after, _ := json.Marshal(afterProfile)
	if string(before) != string(after) {
		t.Fatal("denial overwrote a working profile")
	}
	// A real filesystem failure after exchange must revoke the new grant and preserve the working profile.
	failed, err := PairStart(ctx, store, opts)
	if err != nil {
		t.Fatal(err)
	}
	approve(failed)
	if err = os.Mkdir(filepath.Join(store.Dir, "client-"+failed.ClientID+".json"), 0700); err != nil {
		t.Fatal(err)
	}
	failed, err = PairFinish(ctx, store, failed.RequestID, true, hub.Client())
	if err == nil || failed.Status != "unavailable" {
		t.Fatal("credential installation failure", failed, err)
	}
	afterProfile, err = store.load("work")
	if err != nil {
		t.Fatal(err)
	}
	after, _ = json.Marshal(afterProfile)
	if string(before) != string(after) {
		t.Fatal("installation failure overwrote a working profile")
	}
	grants, _, err := identityStore.QueryClientGrants(ctx, configstore.ClientGrantQuery{Issuer: cfg.Auth.Issuer, Subject: user.ID, ClientID: failed.ClientID})
	if err != nil || len(grants) != 1 || grants[0].Status != "revoked" {
		t.Fatal("failed installation left a live grant", grants, err)
	}
	// Offline initialization returns immediately and exposes only reserved auth tools.
	opts.Profile = "interactive"
	inputRead, inputWrite := io.Pipe()
	outputRead, outputWrite := io.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- ConnectInteractive(ctx, store, opts, ConnectOptions{Input: inputRead, Output: outputWrite, HTTPClient: hub.Client()})
	}()
	local, err := mcp.NewClient(&mcp.Implementation{Name: "test-agent", Version: "1"}, nil).Connect(ctx, &mcp.IOTransport{Reader: outputRead, Writer: inputWrite}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer local.Close()
	tools, err := local.ListTools(ctx, nil)
	if err != nil || len(tools.Tools) != 2 {
		t.Fatal("preauth catalog", tools, err)
	}
	callStatus := func(name string) PairStatus {
		t.Helper()
		res, e := local.CallTool(ctx, &mcp.CallToolParams{Name: name})
		if e != nil {
			t.Fatal("auth tool", name, res, e)
		}
		raw, _ := json.Marshal(res.StructuredContent)
		var s PairStatus
		if e = json.Unmarshal(raw, &s); e != nil {
			t.Fatal(e)
		}
		return s
	}
	pair = callStatus(authStartTool)
	privatePair, err := store.pairPath(pair.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(privatePair, privatePair+".unavailable"); err != nil {
		t.Fatal(err)
	}
	interrupted := callStatus(authStatusTool)
	if err = os.Rename(privatePair+".unavailable", privatePair); err != nil {
		t.Fatal(err)
	}
	if interrupted.RequestID != pair.RequestID || interrupted.Status != "pending_user" {
		t.Fatal("temporary credential read failure lost the pending pairing", interrupted)
	}
	if reused := callStatus(authStartTool); reused.RequestID != pair.RequestID {
		t.Fatal("start created duplicate request")
	}
	approve(pair)
	for {
		ready = callStatus(authStatusTool)
		if ready.Status == "ready" {
			break
		}
		if ready.Status != "pending_user" {
			t.Fatal(ready)
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(time.Second):
		}
	}
	tools, err = local.ListTools(ctx, nil)
	if err != nil || !slices.ContainsFunc(tools.Tools, func(tool *mcp.Tool) bool { return tool.Name == "alpha.read" }) {
		t.Fatal("postauth catalog", tools, err)
	}
	beforeCall := controlRequests.Load()
	result, err := local.CallTool(ctx, &mcp.CallToolParams{Name: "alpha.read"})
	if err != nil || result.IsError {
		t.Fatal("business call", result, err)
	}
	if requests := controlRequests.Load() - beforeCall; requests != 0 {
		t.Errorf("business call made %d redundant authorization requests", requests)
	}
	group, err = identityStore.Identity(ctx, group.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Removing tool access while retaining the same scopes must still make the Agent unavailable.
	if _, err = identityStore.UpdateIdentity(ctx, group.ID, group.Revision, true, config.IdentityPermissions{Scopes: []string{"mcp:alpha.read"}}); err != nil {
		t.Fatal(err)
	}
	result, err = local.CallTool(ctx, &mcp.CallToolParams{Name: "alpha.read"})
	if err == nil && !result.IsError {
		t.Fatal("revoked permission allowed a business call")
	}
	status := callStatus(authStatusTool)
	if status.Status != "unavailable" {
		t.Fatal("group access withdrawal", status)
	}
	tools, err = local.ListTools(ctx, nil)
	if err != nil || len(tools.Tools) != 2 {
		t.Fatal("revoked catalog", tools, err)
	}
	local.Close()
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("interactive shutdown", ctx.Err())
	}
}
