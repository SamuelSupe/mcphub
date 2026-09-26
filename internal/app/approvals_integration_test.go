package app

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/SamuelSupe/mcphub/v2/internal/authn"
	"github.com/SamuelSupe/mcphub/v2/internal/config"
	"github.com/SamuelSupe/mcphub/v2/internal/configstore"
	"github.com/SamuelSupe/mcphub/v2/internal/diagnostics"
	"github.com/SamuelSupe/mcphub/v2/internal/httptool"
)

func loginApprovalReviewer(t *testing.T, f *adminLoginFixture) (*http.Client, string) {
	return loginAdminBrowser(t, f, "/auth/login?role=approver")
}

func loginAdminBrowser(t *testing.T, f *adminLoginFixture, path string) (*http.Client, string) {
	t.Helper()
	c := *f.client
	c.Jar, _ = cookiejar.New(nil)
	resp := f.request(t, &c, "GET", path, "", "", nil)
	for range 2 {
		location := resp.Header.Get("Location")
		resp.Body.Close()
		var err error
		resp, err = c.Get(location)
		if err != nil {
			t.Fatal(err)
		}
	}
	if resp.StatusCode != 303 {
		t.Fatalf("reviewer login: %d", resp.StatusCode)
	}
	resp.Body.Close()
	resp = f.request(t, &c, "GET", "/auth/session", "", "", nil)
	var session struct{ CSRF string }
	if err := json.NewDecoder(resp.Body).Decode(&session); err != nil || session.CSRF == "" {
		t.Fatalf("reviewer session: %v", err)
	}
	return &c, session.CSRF
}

func TestWriteApprovalThroughOIDCAdminAndMCP(t *testing.T) {
	f := newAdminLoginFixture(t)
	defer func() {
		page := f.app.requests.Query(diagnostics.Query{})
		if page.Statistics.Outcomes["approval_pending"] == 0 || page.Statistics.ApprovalResumes == 0 {
			t.Errorf("approval diagnostics: %+v", page.Statistics)
		}
	}()
	var calls atomic.Int32
	var observedMu sync.Mutex
	var observed string
	var resourceVersion atomic.Value
	resourceVersion.Store("v1")
	upstream := mcp.NewServer(&mcp.Implementation{Name: "writes", Version: "1"}, nil)
	for _, name := range []string{"read", "write", "unknown"} {
		// Even an upstream read-only hint cannot bypass the administrator's policy.
		upstream.AddTool(&mcp.Tool{Name: name, Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true}, InputSchema: map[string]any{"type": "object"}}, func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			calls.Add(1)
			observedMu.Lock()
			observed = string(req.Params.Arguments)
			observedMu.Unlock()
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(req.Params.Arguments)}}}, nil
		})
	}
	upstream.AddTool(&mcp.Tool{Name: "preview", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{StructuredContent: map[string]any{"version": resourceVersion.Load(), "before": map[string]any{"limit": 10}, "after": map[string]any{"limit": 20}}}, nil
	})
	backend := httptest.NewServer(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return upstream }, &mcp.StreamableHTTPOptions{Stateless: true}))
	t.Cleanup(backend.Close)
	_, err := f.app.store.Create(t.Context(), configstore.Record{Enabled: true, Config: config.BackendConfig{
		ID: "db", URL: backend.URL, Required: true, AllowInsecureHTTP: true, RequestTimeout: config.Duration{Duration: 5 * time.Second},
		PublishedTools: []string{"read", "write", "unknown", "preview"}, RequiredScopes: []string{"db:access"},
		ToolRules: []config.ToolRule{{Match: "read", Effect: "read"}, {Match: "preview", Effect: "read"}, {Match: "write", Effect: "write", RequiredScopes: []string{"db:write"}, ResourceRules: []config.ResourceRule{{Argument: "/project", AllowedValues: []string{"work"}}}, Approval: &config.ToolApprovalPolicy{Action: "Change project limit", Environment: "production", ResourceArguments: []string{"/project"}, PreviewTool: "preview", VersionArgument: "/expected_version", RequireDifferentReviewer: true, Approvers: []config.ApprovalGrant{{Subjects: []string{"administrator-123"}, Resources: []config.ResourceRule{{Argument: "/project", AllowedValues: []string{"work"}}}}}}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	var httpCalls atomic.Int32
	var disconnect atomic.Bool
	httpUpstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		httpCalls.Add(1)
		if disconnect.Load() {
			connection, _, _ := w.(http.Hijacker).Hijack()
			connection.Close()
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"ok":true}`)
	}))
	t.Cleanup(httpUpstream.Close)
	_, err = f.app.store.CreateToolGroup(t.Context(), configstore.ToolGroupRecord{Config: httptool.GroupConfig{
		ID: "api", BaseURL: httpUpstream.URL, Enabled: true, RequestTimeout: 5 * time.Second,
		RequiredScopes: []string{"db:write"}, ToolRules: []config.ToolRule{{Match: "write", Effect: "write"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.app.store.CreateHTTPTool(t.Context(), configstore.HTTPToolRecord{GroupID: "api", Config: httptool.ToolConfig{Name: "write", Enabled: true, Method: "GET", Path: "/write"}}); err != nil {
		t.Fatal(err)
	}
	records, _ := f.app.store.List(t.Context())
	cfg := configWithRecords(f.app.currentConfig(), records)
	cfg.Server.RequestTimeout = config.Duration{Duration: 5 * time.Second}
	old := http.DefaultTransport
	http.DefaultTransport = httpUpstream.Client().Transport
	err = f.app.replaceRuntimeLocked(cfg)
	http.DefaultTransport = f.client.Transport
	verifier := authn.NewManager(f.issuer.URL, cfg.Server.PublicURL, f.app.logger)
	http.DefaultTransport = old
	if err != nil {
		t.Fatal(err)
	}
	f.app.auth = verifier
	go verifier.Run(f.app.ctx)
	for deadline := time.Now().Add(5 * time.Second); !verifier.Ready(); {
		if time.Now().After(deadline) {
			t.Fatal("MCP verifier unavailable")
		}
		time.Sleep(10 * time.Millisecond)
	}
	hubServer := httptest.NewServer(f.app)
	t.Cleanup(hubServer.Close)
	token := func(subject, scopes string) string {
		raw, err := jwt.Signed(f.signer).Claims(map[string]any{"iss": f.issuer.URL, "aud": cfg.Server.PublicURL, "sub": subject, "scope": scopes, "exp": time.Now().Add(time.Hour).Unix()}).Serialize()
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	session := connectAppClient(t, t.Context(), hubServer.URL+"/mcp", token("requester", "db:access db:write"))
	t.Cleanup(func() { session.Close() })
	other := connectAppClient(t, t.Context(), hubServer.URL+"/mcp", token("other", "db:access db:write"))
	t.Cleanup(func() { other.Close() })
	revoked := connectAppClient(t, t.Context(), hubServer.URL+"/mcp", token("requester", "db:access"))
	t.Cleanup(func() { revoked.Close() })
	reviewer, csrf := loginApprovalReviewer(t, f)
	call := func(client *mcp.ClientSession, name string, args any) *mcp.CallToolResult {
		t.Helper()
		result, err := client.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	resume := func(client *mcp.ClientSession, id string) *mcp.CallToolResult {
		return call(client, "mcphub_resume_approval", map[string]any{"approval_id": id})
	}
	create := func(name string) string {
		t.Helper()
		args := json.RawMessage(`{}`)
		if name == "db.write" {
			args = json.RawMessage(`{"project":"denied","project":"work","n":9007199254740993,"expected_version":"v1"}`)
		}
		result := call(session, name, args)
		data, _ := json.Marshal(result.StructuredContent)
		var value struct {
			ID string `json:"approval_id"`
		}
		json.Unmarshal(data, &value)
		if !result.IsError || value.ID == "" {
			t.Fatalf("approval was not requested: %#v", result)
		}
		return value.ID
	}
	decide := func(id, decision string) {
		t.Helper()
		resp := f.request(t, reviewer, "POST", "/api/v1/approvals/"+id, "", csrf, []byte(`{"decision":"`+decision+`","reason":"Reviewed request"}`))
		if resp.StatusCode != 200 {
			body, _ := io.ReadAll(resp.Body)
			t.Fatalf("decision: %d %s", resp.StatusCode, body)
		}
	}
	if result := call(session, "db.read", map[string]any{}); result.IsError || calls.Load() != 1 {
		t.Fatal("explicit read failed")
	}
	if result := call(session, "db.write", map[string]any{"project": "denied"}); !result.IsError {
		t.Fatal("resource boundary bypassed")
	}
	id := create("db.write")
	if calls.Load() != 1 || !resume(session, id).IsError {
		t.Fatal("unapproved write reached upstream")
	}
	for _, bearer := range []string{token("requester", "db:access db:write"), f.signed("mcphub:admin", f.server.URL)} {
		resp := f.request(t, f.client, "POST", "/api/v1/approvals/"+id, bearer, "", []byte(`{"decision":"approved"}`))
		if resp.StatusCode != 401 && resp.StatusCode != 403 {
			t.Fatal("bearer credential approved write")
		}
	}
	if resp := f.request(t, reviewer, "POST", "/api/v1/approvals/"+id, "", "", []byte(`{"decision":"approved"}`)); resp.StatusCode != 403 {
		t.Fatal("approval accepted without CSRF")
	}
	decide(id, "approved")
	if !resume(other, id).IsError || !resume(revoked, id).IsError {
		t.Fatal("caller or scope binding bypassed")
	}
	if result := call(session, "mcphub_resume_approval", map[string]any{"approval_id": id, "arguments": map[string]any{"project": "denied"}}); !result.IsError {
		t.Fatal("resume allowed replacement arguments")
	}
	var wg sync.WaitGroup
	for range 6 {
		wg.Go(func() {
			_, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "mcphub_resume_approval", Arguments: map[string]any{"approval_id": id}})
			if err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if calls.Load() != 2 {
		t.Fatalf("write executed %d times", calls.Load()-1)
	}
	if resume(session, id).IsError || calls.Load() != 2 {
		t.Fatal("result replay executed again")
	}
	observedMu.Lock()
	got := observed
	observedMu.Unlock()
	if strings.Contains(got, "denied") || !strings.Contains(got, "9007199254740993") {
		t.Fatalf("review/execution JSON differs: %s", got)
	}
	unknown := create("db.unknown")
	decide(unknown, "rejected")
	if !resume(session, unknown).IsError || calls.Load() != 2 {
		t.Fatal("rejected tool executed")
	}
	changed := create("db.write")
	decide(changed, "approved")
	resourceVersion.Store("v2")
	if !resume(session, changed).IsError || calls.Load() != 2 {
		t.Fatal("write executed against a changed resource version")
	}
	resourceVersion.Store("v1")
	cancelled := create("db.write")
	if result := call(other, "mcphub_cancel_approval", map[string]any{"approval_id": cancelled}); !result.IsError {
		t.Fatal("another caller cancelled approval")
	}
	if result := call(session, "mcphub_cancel_approval", map[string]any{"approval_id": cancelled}); result.IsError {
		t.Fatal("requester could not cancel")
	}
	if !resume(session, cancelled).IsError || calls.Load() != 2 {
		t.Fatal("cancelled approval executed")
	}
	for _, broken := range []bool{false, true} {
		disconnect.Store(broken)
		httpID := create("api.write")
		decide(httpID, "approved")
		before := httpCalls.Load()
		result := resume(session, httpID)
		if result.IsError != broken || httpCalls.Load() != before+1 {
			t.Fatalf("HTTP approval: error=%v, backend calls=%d; want error=%v and one call", result.IsError, httpCalls.Load()-before, broken)
		}
		resume(session, httpID)
		if httpCalls.Load() != before+1 {
			t.Fatal("HTTP write replayed")
		}
		stored, _ := f.app.store.GetApproval(t.Context(), httpID)
		if broken && stored.Status != "unknown" {
			t.Fatalf("ambiguous outcome: %s", stored.Status)
		}
	}
	stale := create("db.write")
	decide(stale, "approved")
	if err := f.app.replaceRuntimeLocked(cfg); err != nil {
		t.Fatal(err)
	}
	if !resume(session, stale).IsError || calls.Load() != 2 {
		t.Fatal("old generation approval survived reload")
	}

	records, _ = f.app.store.List(t.Context())
	cfg = configWithRecords(f.app.currentConfig(), records)
	for i := range cfg.Backends {
		if cfg.Backends[i].ID == "db" {
			for j := range cfg.Backends[i].ToolRules {
				rule := &cfg.Backends[i].ToolRules[j]
				if rule.Match == "write" {
					rule.Approval.RequiredApprovals = 2
					rule.Approval.OperationIDArgument = "/operation_id"
					rule.Approval.StatusTool = "preview"
					rule.Approval.Approvers[0].Subjects = append(rule.Approval.Approvers[0].Subjects, "second-reviewer")
				}
			}
		}
	}
	if err := f.app.replaceRuntimeLocked(cfg); err != nil {
		t.Fatal(err)
	}
	currentSession := connectAppClient(t, t.Context(), hubServer.URL+"/mcp", token("requester", "db:access db:write"))
	t.Cleanup(func() { currentSession.Close() })
	operation := map[string]any{"project": "work", "expected_version": "v1", "operation_id": "operation-1"}
	initial := call(currentSession, "db.write", operation)
	var pending struct {
		ID string `json:"approval_id"`
	}
	encoded, _ := json.Marshal(initial.StructuredContent)
	json.Unmarshal(encoded, &pending)
	if pending.ID == "" {
		t.Fatalf("operation approval missing: %#v", initial)
	}
	duplicate := call(currentSession, "db.write", operation)
	encoded, _ = json.Marshal(duplicate.StructuredContent)
	if !bytes.Contains(encoded, []byte(pending.ID)) {
		t.Fatal("duplicate business operation created another approval")
	}
	decide(pending.ID, "approved")
	if !resume(currentSession, pending.ID).IsError || calls.Load() != 2 {
		t.Fatal("single vote executed two-reviewer operation")
	}
	f.loginSubject.Store("second-reviewer")
	secondReviewer, secondCSRF := loginApprovalReviewer(t, f)
	response := f.request(t, secondReviewer, "POST", "/api/v1/approvals/"+pending.ID, "", secondCSRF, []byte(`{"decision":"approved","reason":"Independent second review"}`))
	if response.StatusCode != 200 {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("second reviewer: %d %s", response.StatusCode, body)
	}
	response.Body.Close()
	status := call(currentSession, "mcphub_approval_status", map[string]any{"approval_id": pending.ID, "query_upstream": true})
	if status.IsError || calls.Load() != 2 {
		t.Fatal("read-only status triggered execution or failed")
	}
	if !call(other, "mcphub_approval_status", map[string]any{"approval_id": pending.ID}).IsError {
		t.Fatal("operation status crossed caller boundary")
	}
	if result := resume(currentSession, pending.ID); result.IsError || calls.Load() != 3 {
		t.Fatal("quorum did not authorize one write")
	}
	call(currentSession, "db.write", operation)
	status = call(currentSession, "mcphub_approval_status", map[string]any{"approval_id": pending.ID})
	encoded, _ = json.Marshal(status.StructuredContent)
	if status.IsError || calls.Load() != 3 || !bytes.Contains(encoded, []byte(`"result"`)) {
		t.Fatal("completed operation was replayed or lost its cached result")
	}
	if err := f.app.replaceRuntimeLocked(cfg); err != nil {
		t.Fatal(err)
	}
	reloadedSession := connectAppClient(t, t.Context(), hubServer.URL+"/mcp", token("requester", "db:access db:write"))
	t.Cleanup(func() { reloadedSession.Close() })
	status = call(reloadedSession, "mcphub_approval_status", map[string]any{"approval_id": pending.ID, "query_upstream": true})
	encoded, _ = json.Marshal(status.StructuredContent)
	if status.IsError || calls.Load() != 3 || !bytes.Contains(encoded, []byte(`"result"`)) || !bytes.Contains(encoded, []byte(`"upstream_observation"`)) {
		t.Fatal("unchanged runtime reload prevented read-only operation lookup")
	}

	t.Run("client grants bind writes and cached results", func(t *testing.T) {
		cfg.ClientAuthorization = config.ClientAuthorizationConfig{Enabled: true, ClientID: "portal"}
		if err := f.app.replaceRuntimeLocked(cfg); err != nil {
			t.Fatal(err)
		}
		newClient := func(id string) (configstore.ClientGrant, *mcp.ClientSession) {
			t.Helper()
			g := configstore.ClientGrant{GrantBinding: configstore.GrantBinding{ClientID: id}, Issuer: f.issuer.URL, Subject: "requester", Resource: cfg.Server.PublicURL, ClientName: id, EndpointID: "db", AllowedScopes: []string{"db:access", "db:write"}, AllowedTools: []string{"read", "write", "preview"}, AllowWriteRequests: true, Capabilities: configstore.GrantCapabilities{Tools: true}}
			if err := f.app.currentRuntime().hub.PrepareClientGrant(&g, g.AllowedScopes); err != nil {
				t.Fatal(err)
			}
			g.EndpointUID, g.EndpointPolicy, err = f.app.store.ClientEndpointPolicy(t.Context(), "db")
			if err != nil {
				t.Fatal(err)
			}
			g, exchange, _, err := f.app.store.CreateClientGrant(t.Context(), g, "", time.Hour, 8*time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			if err := f.app.store.DecideClientGrant(t.Context(), g.GrantID, g.Issuer, g.Subject, true); err != nil {
				t.Fatal(err)
			}
			g, credential, err := f.app.store.ExchangeClientGrant(t.Context(), g.GrantID, g.Issuer, g.Subject, exchange)
			if err != nil {
				t.Fatal(err)
			}
			client := mcp.NewClient(&mcp.Implementation{Name: id, Version: "1"}, nil)
			s, err := client.Connect(t.Context(), &mcp.StreamableClientTransport{Endpoint: hubServer.URL + "/mcp", HTTPClient: &http.Client{Transport: bearerTransport{token: token("requester", "db:access db:write"), grant: credential, base: http.DefaultTransport}}}, nil)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { s.Close() })
			return g, s
		}
		grantA, clientA := newClient("ci_write_client_a")
		_, clientB := newClient("ci_write_client_b")
		checkGrant := func(subject string, scopes []string, outcome string) {
			t.Helper()
			before := calls.Load()
			args, _ := json.Marshal(operation)
			body, _ := json.Marshal(accessCheckInput{Endpoint: "db", Tool: "write", Scopes: scopes, Subject: subject, GrantID: grantA.GrantID, Arguments: args})
			resp := f.request(t, f.client, "POST", "/api/v1/access-check", f.signed("mcphub:admin", f.server.URL), "", body)
			var result accessCheckResult
			if resp.StatusCode != 200 || json.NewDecoder(resp.Body).Decode(&result) != nil || result.Outcome != outcome || calls.Load() != before {
				t.Fatalf("grant simulation: HTTP %d %+v", resp.StatusCode, result)
			}
		}
		checkGrant("requester", []string{"db:access", "db:write"}, "requires_approval")
		checkGrant("requester", []string{"db:access"}, "denied")
		checkGrant("another-user", []string{"db:access", "db:write"}, "denied")
		if result := call(clientA, "db.write", operation); !result.IsError {
			t.Fatal("grant replayed a legacy operation")
		}
		operation["operation_id"] = "grant-operation"
		result := call(clientA, "db.write", operation)
		var created struct {
			ID string `json:"approval_id"`
		}
		data, _ := json.Marshal(result.StructuredContent)
		_ = json.Unmarshal(data, &created)
		if created.ID == "" || calls.Load() != 3 {
			t.Fatalf("grant bypassed independent approval: %+v", result)
		}
		for _, tool := range []string{"mcphub_approval_status", "mcphub_resume_approval", "mcphub_cancel_approval"} {
			if !call(clientB, tool, map[string]any{"approval_id": created.ID}).IsError {
				t.Fatal("approval crossed client grants", tool)
			}
		}
		if !call(clientB, "db.write", operation).IsError {
			t.Fatal("second grant reused business operation")
		}
		decide(created.ID, "approved")
		response := f.request(t, secondReviewer, "POST", "/api/v1/approvals/"+created.ID, "", secondCSRF, []byte(`{"decision":"approved","reason":"Reviewed grant-bound write"}`))
		response.Body.Close()
		if response.StatusCode != 200 {
			t.Fatalf("second approval: %d", response.StatusCode)
		}
		if result := resume(clientA, created.ID); result.IsError || calls.Load() != 4 {
			t.Fatalf("grant write failed: %+v", result)
		}
		if !call(clientB, "mcphub_approval_status", map[string]any{"approval_id": created.ID}).IsError {
			t.Fatal("cached result crossed grants")
		}
		if err := f.app.store.RevokeClientGrant(t.Context(), grantA.GrantID, grantA.Issuer, grantA.Subject); err != nil {
			t.Fatal(err)
		}
		checkGrant("requester", []string{"db:access", "db:write"}, "denied")
		if _, err := clientA.CallTool(t.Context(), &mcp.CallToolParams{Name: "mcphub_resume_approval", Arguments: map[string]any{"approval_id": created.ID}}); err == nil {
			t.Fatal("revoked grant accessed approval")
		}
		_, replacement := newClient("ci_write_client_a")
		if !call(replacement, "db.write", operation).IsError || calls.Load() != 4 {
			t.Fatal("reauthorization replayed business operation")
		}
		if !call(replacement, "mcphub_approval_status", map[string]any{"approval_id": created.ID}).IsError {
			t.Fatal("replacement grant inherited cached result")
		}
	})
}

func TestLocalModeCannotApproveWrites(t *testing.T) {
	a := newAdminTestApp(t)
	rec, err := a.store.CreateApproval(t.Context(), configstore.ApprovalIntent{Subject: "local", Params: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	resp := serveAdminJSON(t, a, "POST", "/api/v1/approvals/"+rec.ID, []byte(`{"decision":"approved"}`), "")
	if resp.Code != 403 {
		t.Fatalf("local approval: %d", resp.Code)
	}
}

func TestApprovalReviewerPermissionsAndStepUp(t *testing.T) {
	f := newAdminLoginFixture(t)
	f.app.adminAuth.cfg.Approvals.StepUpACRValues = []string{"urn:mcphub:test:mfa"}
	reviewer, csrf := loginApprovalReviewer(t, f)
	administrator, adminCSRF := loginAdminBrowser(t, f, "/auth/login")
	for _, method := range []string{"GET", "POST"} {
		if response := f.request(t, reviewer, method, "/api/v1/backends", "", csrf, []byte(`{}`)); response.StatusCode != 403 {
			t.Fatal("approver obtained configuration access")
		}
	}
	if response := f.request(t, administrator, "GET", "/api/v1/approvals", "", adminCSRF, nil); response.StatusCode != 403 {
		t.Fatal("configuration administrator implicitly became an approver")
	}
	requestNumber := 0
	create := func(subject, project string) string {
		t.Helper()
		requestNumber++
		params, _ := json.Marshal(map[string]any{"name": "db.update", "arguments": map[string]any{"project": project, "request_number": requestNumber}})
		record, err := f.app.store.CreateApproval(t.Context(), configstore.ApprovalIntent{Issuer: f.issuer.URL, Subject: subject, Tool: "db.update", Params: params, Rules: []config.ToolApprovalPolicy{{RequireDifferentReviewer: true, RequireStepUp: true, Approvers: []config.ApprovalGrant{{Subjects: []string{"administrator-123"}, Resources: []config.ResourceRule{{Argument: "/project", AllowedValues: []string{"work"}}}}}}}})
		if err != nil {
			t.Fatal(err)
		}
		return record.ID
	}
	post := func(id, decision string, status int) {
		t.Helper()
		response := f.request(t, reviewer, "POST", "/api/v1/approvals/"+id, "", csrf, []byte(`{"decision":"`+decision+`","reason":"Verified production request"}`))
		if response.StatusCode != status {
			data, _ := io.ReadAll(response.Body)
			t.Fatalf("decision %s status=%d want=%d: %s", decision, response.StatusCode, status, data)
		}
	}
	id := create("requester", "work")
	forbidden := create("requester", "other")
	own := create("administrator-123", "work")
	post(forbidden, "approved", 404)
	post(own, "approved", 403)
	post(id, "approved", 403)
	response := f.request(t, reviewer, "GET", "/api/v1/approvals", "", "", nil)
	data, _ := io.ReadAll(response.Body)
	if bytes.Contains(data, []byte(forbidden)) {
		t.Fatal("approval outside resource scope leaked in listing")
	}
	verify := func(id, failure string) {
		t.Helper()
		f.stepUpFailure.Store(failure)
		response := f.request(t, reviewer, "POST", "/api/v1/approvals/"+id+"/verify", "", csrf, nil)
		var value struct {
			URL string `json:"authorization_url"`
		}
		if json.NewDecoder(response.Body).Decode(&value) != nil || value.URL == "" {
			t.Fatalf("step-up start: %d", response.StatusCode)
		}
		u, _ := url.Parse(value.URL)
		if u.Query().Get("max_age") != "0" || u.Query().Get("nonce") == "" || u.Query().Get("acr_values") != "urn:mcphub:test:mfa" {
			t.Fatal("step-up did not request fresh, strong authentication")
		}
		response, err := reviewer.Get(value.URL)
		if err != nil {
			t.Fatal(err)
		}
		callback := response.Header.Get("Location")
		response.Body.Close()
		response, err = reviewer.Get(callback)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		expected := "verification=complete"
		if failure != "" {
			expected = "verification=failed"
		}
		if response.StatusCode != 303 || !strings.Contains(response.Header.Get("Location"), expected) {
			t.Fatalf("step-up result: %d %s", response.StatusCode, response.Header.Get("Location"))
		}
	}
	for _, failure := range []string{"acr", "old", "nonce", "subject", "audience"} {
		verify(id, failure)
		post(id, "approved", 403)
	}
	verify(id, "")
	another := create("requester", "work")
	post(another, "approved", 403)
	post(id, "approved", 200)
	post(id, "approved", 409)
	history, err := f.app.store.ApprovalHistory(t.Context(), id)
	if err != nil || len(history) != 2 || history[1].Detail.ACR != "urn:mcphub:test:mfa" || history[1].Detail.AuthTime.IsZero() || history[1].Detail.Reason == "" {
		t.Fatalf("verified approval audit missing: %+v %v", history, err)
	}
	post(id, "revoked", 200)
	if err := f.app.store.ClaimApproval(t.Context(), id); err == nil {
		t.Fatal("revoked approval remained executable")
	}
}

func TestConfigurationApprovalSeparatesRolesAndBindsRevision(t *testing.T) {
	f := newAdminLoginFixture(t)
	f.app.currentConfig().Admin.Approvals.PolicyChanges.Enabled = true
	f.app.adminAuth.cfg.Approvals.PolicyChanges.Enabled = true
	admin, adminCSRF := loginAdminBrowser(t, f, "/auth/login")
	samePerson, ownCSRF := loginAdminBrowser(t, f, "/auth/login?role=security")
	reviewer, reviewerCSRF := loginApprovalReviewer(t, f)
	f.loginSubject.Store("security-reviewer")
	security, securityCSRF := loginAdminBrowser(t, f, "/auth/login?role=security")
	create := []byte(`{"id":"governed","base_url":"https://api.example.com","enabled":true,"request_timeout":"1s","headers":[{"name":"X-Key","value":"encrypted-proposal-secret"}],"tool_rules":[{"match":"*","effect":"write"}]}`)
	propose := func(method, path string, body []byte, etag string) string {
		t.Helper()
		req, err := http.NewRequest(method, f.server.URL+path, bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Origin", f.server.URL)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-MCPHub-CSRF", adminCSRF)
		if etag != "" {
			req.Header.Set("If-Match", etag)
		}
		response, err := admin.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		data, _ := io.ReadAll(response.Body)
		var value struct {
			ID string `json:"approval_id"`
		}
		if response.StatusCode != 202 || json.Unmarshal(data, &value) != nil || value.ID == "" {
			t.Fatalf("proposal: %d %s", response.StatusCode, data)
		}
		return value.ID
	}
	decide := func(c *http.Client, csrf, id string, want int) approvalView {
		t.Helper()
		response := f.request(t, c, "POST", "/api/v1/approvals/"+id, "", csrf, []byte(`{"decision":"approved","reason":"Reviewed immutable configuration"}`))
		defer response.Body.Close()
		data, _ := io.ReadAll(response.Body)
		if response.StatusCode != want {
			t.Fatalf("config decision: %d want %d %s", response.StatusCode, want, data)
		}
		var view approvalView
		json.Unmarshal(data, &view)
		return view
	}
	id := propose("POST", "/api/v1/tool-groups", create, "")
	if _, err := f.app.store.GetToolGroup(t.Context(), "governed"); !errors.Is(err, configstore.ErrNotFound) {
		t.Fatal("unapproved configuration reached storage")
	}
	if second := propose("POST", "/api/v1/tool-groups", create, ""); second != id {
		t.Fatal("duplicate config proposal was not reused")
	}
	decide(admin, adminCSRF, id, 403)
	decide(samePerson, ownCSRF, id, 403)
	decide(reviewer, reviewerCSRF, id, 403)
	response := f.request(t, security, "GET", "/api/v1/approvals/"+id, "", "", nil)
	raw, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if bytes.Contains(raw, []byte("encrypted-proposal-secret")) || !bytes.Contains(raw, []byte("configuration")) {
		t.Fatal("configuration review disclosed credentials or omitted kind")
	}
	if value := decide(security, securityCSRF, id, 200); value.Status != "succeeded" {
		t.Fatalf("configuration not applied: %s", value.Status)
	}
	group, err := f.app.store.GetToolGroup(t.Context(), "governed")
	if err != nil || group.Config.Headers["X-Key"] != "encrypted-proposal-secret" || group.Config.ToolRules[0].Effect != "write" {
		t.Fatalf("approved configuration not preserved: %v", err)
	}
	// Reclassifying a write as a read must go through independent review.
	update := bytes.Replace(create, []byte(`"effect":"write"`), []byte(`"effect":"read"`), 1)
	id = propose("PUT", "/api/v1/tool-groups/governed", update, revisionETag(group.Revision))
	current, _ := f.app.store.GetToolGroup(t.Context(), "governed")
	if current.Config.ToolRules[0].Effect != "write" {
		t.Fatal("effect downgrade bypassed approval")
	}
	// A separate emergency disable invalidates the older proposal.
	disabled := bytes.Replace(create, []byte(`"enabled":true`), []byte(`"enabled":false`), 1)
	req, _ := http.NewRequest("PUT", f.server.URL+"/api/v1/tool-groups/governed", bytes.NewReader(disabled))
	req.Header.Set("Origin", f.server.URL)
	req.Header.Set("X-MCPHub-CSRF", adminCSRF)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("If-Match", revisionETag(current.Revision))
	response, err = admin.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		data, _ := io.ReadAll(response.Body)
		t.Fatalf("emergency disable: %d %s", response.StatusCode, data)
	}
	if value := decide(security, securityCSRF, id, 200); value.Status != "failed" {
		t.Fatalf("stale configuration applied: %s", value.Status)
	}
	current, _ = f.app.store.GetToolGroup(t.Context(), "governed")
	if current.Config.Enabled || current.Config.ToolRules[0].Effect != "write" {
		t.Fatal("stale proposal replaced emergency disable")
	}
}

func TestApprovalDeliveryRequiresSignedNotificationAndExactArchiveAck(t *testing.T) {
	application := newAdminTestApp(t)
	public, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	const secret = "a-test-webhook-key-with-at-least-32-bytes"
	var acknowledge atomic.Bool
	var archiveAttempts, notifications atomic.Int32
	receiver := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		payload, _ := io.ReadAll(r.Body)
		if r.URL.Path == "/notification" {
			notifications.Add(1)
			mac := hmac.New(sha256.New, []byte(secret))
			mac.Write([]byte(r.Header.Get("X-MCPHub-Timestamp") + "."))
			mac.Write(payload)
			if r.Header.Get("X-MCPHub-Signature") != "sha256="+hex.EncodeToString(mac.Sum(nil)) || bytes.Contains(payload, []byte("private-business-resource")) {
				t.Error("notification signature or redaction failed")
			}
			w.WriteHeader(204)
			return
		}
		archiveAttempts.Add(1)
		var envelope configstore.AuditEnvelope
		if json.Unmarshal(payload, &envelope) != nil {
			t.Error("invalid envelope")
			w.WriteHeader(400)
			return
		}
		entry, err := configstore.VerifyAuditEnvelope(envelope, map[string]ed25519.PublicKey{"test": public}, 0, "")
		if err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		if !acknowledge.Load() {
			writeJSON(w, 200, map[string]any{"sequence": entry.Sequence, "hash": "wrong"})
			return
		}
		writeJSON(w, 200, map[string]any{"sequence": entry.Sequence, "hash": envelope.Hash})
	}))
	defer receiver.Close()
	settings := config.ApprovalSettings{Notifications: config.ApprovalWebhook{URL: receiver.URL + "/notification", Secret: secret}, AuditArchive: config.ApprovalArchive{URL: receiver.URL + "/archive", KeyID: "test", SigningKey: base64.StdEncoding.EncodeToString(key)}}
	application.currentConfig().Admin.Approvals = settings
	application.store.ConfigureApprovalDelivery(settings, "https://admin.example")
	if _, err := application.store.CreateApproval(t.Context(), configstore.ApprovalIntent{Subject: "requester", Params: []byte(`{"arguments":{"project":"private-business-resource"}}`)}); err != nil {
		t.Fatal(err)
	}
	archive, err := application.store.NextApprovalDelivery(t.Context(), true)
	if err != nil {
		t.Fatal(err)
	}
	if err := application.deliverApproval(t.Context(), receiver.Client(), archive, true); err == nil {
		t.Fatal("incorrect archive receipt acknowledged")
	}
	acknowledge.Store(true)
	if err := application.deliverApproval(t.Context(), receiver.Client(), archive, true); err != nil {
		t.Fatal(err)
	}
	if err := application.store.CompleteApprovalDelivery(t.Context(), archive, true, true); err != nil {
		t.Fatal(err)
	}
	notification, err := application.store.NextApprovalDelivery(t.Context(), false)
	if err != nil {
		t.Fatal(err)
	}
	if err := application.deliverApproval(t.Context(), receiver.Client(), notification, false); err != nil {
		t.Fatal(err)
	}
	if err := application.store.CompleteApprovalDelivery(t.Context(), notification, false, true); err != nil {
		t.Fatal(err)
	}
	status, err := application.store.ApprovalDeliveryStatus(t.Context())
	if err != nil || status.ArchivePending != 0 || status.NotificationsPending != 0 || notifications.Load() != 1 || archiveAttempts.Load() != 2 {
		t.Fatalf("delivery not completed: %+v %v", status, err)
	}
}
