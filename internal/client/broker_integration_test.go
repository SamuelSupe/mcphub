package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SamuelSupe/mcphub/v2/internal/config"
	"github.com/SamuelSupe/mcphub/v2/internal/configstore"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestSetupSelectsNarrowClientAndPrintsConfiguration(t *testing.T) {
	f := newLoginFixture(t, true)
	root, err := os.MkdirTemp("", "mh-setup-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	// Store must create the credential directory with its owner-only Windows ACL.
	dir := filepath.Join(root, "credentials")
	f.store = &Store{Dir: dir}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	if err := f.login(ctx, nil); err != nil {
		t.Fatal(err)
	}
	browser := *f.client
	browser.Jar, _ = cookiejar.New(nil)
	consents := 0
	open := func(raw string) error {
		consents++
		resp, err := browser.Get(f.hub.URL + "/client-auth/auth/login")
		if err != nil {
			return err
		}
		resp.Body.Close()
		resp, err = browser.Get(f.hub.URL + "/client-auth/auth/session")
		if err != nil {
			return err
		}
		var session struct{ CSRF string }
		err = json.NewDecoder(resp.Body).Decode(&session)
		resp.Body.Close()
		if err != nil {
			return err
		}
		u, err := url.Parse(raw)
		if err != nil {
			return err
		}
		req, err := http.NewRequestWithContext(ctx, "POST", f.hub.URL+"/client-auth/api/client-authorization-requests/"+u.Query().Get("request")+"/confirm", nil)
		if err != nil {
			return err
		}
		req.Header.Set("Origin", f.hub.URL)
		req.Header.Set("X-MCPHub-CSRF", session.CSRF)
		resp, err = browser.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode != 204 {
			return fmt.Errorf("consent returned %d", resp.StatusCode)
		}
		return nil
	}
	for _, format := range []string{"1", "2"} {
		var progress, output bytes.Buffer
		opts := SetupOptions{Command: "/usr/local/bin/mcphub-cli", Input: strings.NewReader("1\n1\n\n30\neditor\n" + format + "\nyes\n"), Output: &progress, ConfigOutput: &output, HTTPClient: f.client, OpenBrowser: open}
		if err := Setup(ctx, f.store, "work", opts); err != nil {
			t.Fatalf("setup: %v\n%s", err, progress.String())
		}
		var value map[string]map[string]struct {
			Command string
			Args    []string
			Env     map[string]string
			Type    string
		}
		if err := json.Unmarshal(output.Bytes(), &value); err != nil {
			t.Fatal("configuration stdout contaminated", err)
		}
		key := "mcpServers"
		if format == "2" {
			key = "servers"
		}
		entry := value[key]["editor"]
		if len(entry.Args) != 5 || entry.Args[0] != "connect" || entry.Args[2] != "work" || entry.Env["MCPHUB_HOME"] != dir || entry.Command != opts.Command || (format == "2" && entry.Type != "stdio") {
			t.Fatalf("configuration: %s", output.String())
		}
		p, err := f.store.load("work")
		if err != nil {
			t.Fatal(err)
		}
		credential := p.Broker.Clients[entry.Args[4]]
		if strings.Join(credential.Grant.AllowedTools, ",") != "echo" || strings.Join(credential.Grant.AllowedScopes, ",") != "mcp:alpha" || credential.Grant.AllowWriteRequests || credential.Grant.Capabilities.Resources {
			t.Fatalf("setup broadened permissions: %+v", credential.Grant)
		}
		if strings.Contains(output.String(), credential.Credential) || strings.Contains(output.String(), p.Token.AccessToken) || strings.Contains(progress.String(), "admin [") {
			t.Fatal("setup exposed credentials or ineligible tools")
		}
		if f.toolCalls.Load() != 0 {
			t.Fatal("setup executed a tool")
		}
	}
	for _, input := range []string{"1\n\n", "1\n1\n\n\n\n\nno\n"} {
		err := Setup(ctx, f.store, "work", SetupOptions{Input: strings.NewReader(input), Output: io.Discard, ConfigOutput: io.Discard, HTTPClient: f.client, OpenBrowser: open})
		if err == nil || consents != 2 {
			t.Fatalf("cancelled setup requested consent: %v (%d)", err, consents)
		}
	}
	files, err := filepath.Glob(filepath.Join(dir, "client-*.json"))
	if err != nil || len(files) != 2 {
		t.Fatalf("cancelled setup wrote client entries: %v %v", files, err)
	}
}

func TestSetupCancellationInterruptsInput(t *testing.T) {
	input, writer := io.Pipe()
	defer input.Close()
	defer writer.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	reading := make(chan struct{})
	reader := setupReadFunc(func(p []byte) (int, error) { close(reading); return input.Read(p) })
	store := &Store{Dir: filepath.Join(t.TempDir(), "credentials")}
	go func() {
		done <- Setup(ctx, store, "work", SetupOptions{Input: reader, Output: io.Discard, ConfigOutput: io.Discard})
	}()
	select {
	case <-reading:
	case <-time.After(time.Second):
		t.Fatal("setup never read its input")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel setup: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled setup is blocked on terminal input")
	}
}

type setupReadFunc func([]byte) (int, error)

func (read setupReadFunc) Read(p []byte) (int, error) { return read(p) }

func TestBrokerClientAuthorizationIsolationAndRevocation(t *testing.T) {
	f := newLoginFixture(t, true)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	root, err := os.MkdirTemp("", "mh-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	dir := filepath.Join(root, "credentials")
	f.store = &Store{Dir: dir}
	if err := f.login(ctx, nil); err != nil {
		t.Fatal(err)
	}
	legacy, _ := f.connect(ctx, nil)
	tools, err := legacy.ListTools(ctx, nil)
	if err != nil || len(tools.Tools) != 0 {
		t.Fatalf("strict handshake exposed tools: %+v %v", tools, err)
	}
	if _, err := legacy.CallTool(ctx, &mcp.CallToolParams{Name: "alpha.echo"}); err == nil {
		t.Fatal("bearer bypassed strict endpoint")
	}
	legacy.Close()
	jar, _ := cookiejar.New(nil)
	browser := *f.client
	browser.Jar = jar
	open := func(raw string) error {
		resp, err := browser.Get(f.hub.URL + "/client-auth/auth/login")
		if err != nil {
			return err
		}
		resp.Body.Close()
		resp, err = browser.Get(f.hub.URL + "/client-auth/auth/session")
		if err != nil {
			return err
		}
		var session struct {
			Authenticated bool   `json:"authenticated"`
			CSRF          string `json:"csrf"`
		}
		err = json.NewDecoder(resp.Body).Decode(&session)
		resp.Body.Close()
		if err != nil || !session.Authenticated {
			return errors.New("ordinary user could not sign in to portal")
		}
		u, _ := url.Parse(raw)
		id := u.Query().Get("request")
		// Possession of the MCP token must not stand in for browser consent.
		var token string
		f.editToken(func(p *profile) { token = p.Token.AccessToken })
		for _, headers := range []map[string]string{{"Authorization": "Bearer " + token}, {"Origin": f.hub.URL}, {"Origin": "https://evil.example", "X-MCPHub-CSRF": session.CSRF}} {
			req, _ := http.NewRequestWithContext(ctx, "POST", f.hub.URL+"/client-auth/api/client-authorization-requests/"+id+"/confirm", nil)
			for name, value := range headers {
				req.Header.Set(name, value)
			}
			response, err := browser.Do(req)
			if err != nil {
				return err
			}
			response.Body.Close()
			if response.StatusCode != http.StatusForbidden {
				return fmt.Errorf("consent bypass returned %d", response.StatusCode)
			}
		}
		req, _ := http.NewRequestWithContext(ctx, "POST", f.hub.URL+"/client-auth/api/client-authorization-requests/"+id+"/confirm", nil)
		req.Header.Set("Origin", f.hub.URL)
		req.Header.Set("X-MCPHub-CSRF", session.CSRF)
		resp, err = browser.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusNoContent {
			data, _ := io.ReadAll(resp.Body)
			return fmt.Errorf("consent: HTTP %d %s", resp.StatusCode, data)
		}
		return nil
	}
	authorize := func(project string) configstore.ClientGrant {
		t.Helper()
		g, err := AuthorizeClient(ctx, f.store, "work", ClientOptions{Name: "editor-" + project, Endpoint: "alpha", Scopes: []string{"mcp:alpha"}, Tools: []string{"echo", "slow"}, ResourceRules: []config.ResourceRule{{Argument: "/project", AllowedValues: []string{project}}}, HTTPClient: f.client, OpenBrowser: open, Output: io.Discard})
		if err != nil {
			t.Fatal(err)
		}
		return g
	}
	a, b := authorize("project-a"), authorize("project-b")
	if a.SessionID != b.SessionID || a.GrantID == b.GrantID {
		t.Fatal("client grants did not share only the broker session")
	}
	brokerCtx, stopBroker := context.WithCancel(ctx)
	brokerDone := make(chan error, 1)
	go func() { brokerDone <- RunBroker(brokerCtx, f.store, f.client) }()
	defer func() {
		stopBroker()
		if err := <-brokerDone; err != nil {
			t.Error(err)
		}
	}()
	for {
		if _, err := BrokerControl(ctx, f.store, "status"); err == nil {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("broker failed to start")
		case <-time.After(10 * time.Millisecond):
		}
	}
	entry, err := f.store.localClient("work", a.ClientID)
	if err != nil {
		t.Fatal(err)
	}
	diagnosticCalls := f.toolCalls.Load()
	report := Doctor(ctx, f.store, "work", DoctorOptions{ClientID: a.ClientID, HTTPClient: f.client})
	if !report.Healthy || report.Endpoint != "alpha" || f.toolCalls.Load() != diagnosticCalls {
		t.Fatalf("doctor through Broker: %+v", report)
	}
	encodedReport, _ := json.Marshal(report)
	if strings.Contains(string(encodedReport), entry.Secret) {
		t.Fatal("diagnostic report exposed an IPC credential")
	}
	if _, _, _, err := ipcHandshake(ctx, f.store, brokerMessage{Operation: "connect", Profile: "work", Client: a.ClientID, Secret: entry.Secret + "wrong"}); err == nil {
		t.Fatal("invalid IPC credential accepted")
	}
	connect := func(g configstore.ClientGrant, options ...*mcp.ClientOptions) *mcp.ClientSession {
		t.Helper()
		local, process := net.Pipe()
		go func() {
			_ = ConnectBroker(ctx, f.store, "work", g.ClientID, ConnectOptions{Input: process, Output: process})
		}()
		var clientOptions *mcp.ClientOptions
		if len(options) > 0 {
			clientOptions = options[0]
		}
		cli := mcp.NewClient(&mcp.Implementation{Name: "same-client-name", Version: "1"}, clientOptions)
		if clientOptions != nil {
			cli.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
				return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
					if method == "notifications/subscriptions/acknowledged" {
						select {
						case f.subscriptionReady <- struct{}{}:
						default:
						}
					}
					return next(ctx, method, req)
				}
			})
		}
		session, err := cli.Connect(ctx, &mcp.IOTransport{Reader: local, Writer: local}, nil)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { session.Close(); local.Close(); process.Close() })
		return session
	}
	first, second := connect(a), connect(b)
	f.editToken(func(p *profile) { p.Token.Expiry = time.Now().Add(-time.Second) })
	before := f.refreshCalls.Load()
	var wg sync.WaitGroup
	for _, call := range []struct {
		session *mcp.ClientSession
		project string
	}{{first, "project-a"}, {second, "project-b"}} {
		wg.Go(func() {
			result, err := call.session.CallTool(ctx, &mcp.CallToolParams{Name: "alpha.echo", Arguments: map[string]any{"project": call.project}})
			if err != nil || result.IsError {
				t.Errorf("broker call: %+v %v", result, err)
			}
		})
	}
	wg.Wait()
	if f.refreshCalls.Load()-before != 1 {
		t.Fatal("shared profile refreshed more than once")
	}
	for _, session := range []*mcp.ClientSession{first, second} {
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "alpha.echo", Arguments: map[string]any{"project": "outside"}})
		if err == nil && !result.IsError {
			t.Fatal("grant resource restriction bypassed")
		}
		if _, err := session.ListResources(ctx, nil); err == nil {
			t.Fatal("tools-only grant exposed resources")
		}
	}
	callCtx, stopCall := context.WithCancel(ctx)
	callDone := make(chan error, 1)
	go func() {
		_, err := first.CallTool(callCtx, &mcp.CallToolParams{Name: "alpha.slow", Arguments: map[string]any{"project": "project-a"}})
		callDone <- err
	}()
	select {
	case <-f.slowStarted:
	case <-ctx.Done():
		t.Fatal("slow call never started")
	}
	if _, err := second.CallTool(ctx, &mcp.CallToolParams{Name: "alpha.echo", Arguments: map[string]any{"project": "project-b"}}); err != nil {
		t.Fatal("other client blocked", err)
	}
	stopCall()
	select {
	case <-f.slowCanceled:
	case <-ctx.Done():
		t.Fatal("broker cancellation not forwarded")
	}
	if err := <-callDone; err == nil {
		t.Fatal("canceled call succeeded")
	}
	if err := RevokeClient(ctx, f.store, "work", a.ClientID, f.client); err != nil {
		t.Fatal(err)
	}
	if _, err := first.CallTool(ctx, &mcp.CallToolParams{Name: "alpha.echo", Arguments: map[string]any{"project": "project-a"}}); err == nil {
		t.Fatal("revoked entry still called")
	}
	if _, err := second.CallTool(ctx, &mcp.CallToolParams{Name: "alpha.echo", Arguments: map[string]any{"project": "project-b"}}); err != nil {
		t.Fatal("revoking A stopped B", err)
	}
	values, online, err := ListClients(ctx, f.store, "work", f.client)
	if err != nil || !online || len(values) != 1 {
		t.Fatalf("client status: %+v %t %v", values, online, err)
	}
	updated, err := AuthorizeClient(ctx, f.store, "work", ClientOptions{ID: b.ClientID, Changed: []string{"resource"}, ResourceRules: []config.ResourceRule{{Argument: "/project", AllowedValues: []string{"project-c"}}}, HTTPClient: f.client, OpenBrowser: open, Output: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := second.CallTool(ctx, &mcp.CallToolParams{Name: "alpha.echo", Arguments: map[string]any{"project": "project-b"}}); err == nil {
		t.Fatal("reauthorization silently changed an established connection")
	}
	second = connect(updated)
	if _, err := second.CallTool(ctx, &mcp.CallToolParams{Name: "alpha.echo", Arguments: map[string]any{"project": "project-c"}}); err != nil {
		t.Fatal(err)
	}
	full, err := AuthorizeClient(ctx, f.store, "work", ClientOptions{Name: "resource-client", Endpoint: "alpha", Scopes: []string{"mcp:alpha"}, Tools: []string{"echo", "slow"}, Capabilities: configstore.GrantCapabilities{Tools: true, Prompts: true, Resources: true, Subscriptions: true}, HTTPClient: f.client, OpenBrowser: open, Output: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	progress, updates := make(chan string, 4), make(chan string, 4)
	third := connect(full, &mcp.ClientOptions{ProgressNotificationHandler: func(_ context.Context, r *mcp.ProgressNotificationClientRequest) {
		progress <- fmt.Sprint(r.Params.ProgressToken)
	}, ResourceUpdatedHandler: func(_ context.Context, r *mcp.ResourceUpdatedNotificationRequest) { updates <- r.Params.URI }})
	page, err := third.ListTools(ctx, nil)
	if err != nil || page.NextCursor == "" {
		t.Fatalf("broker pagination: %+v %v", page, err)
	}
	if _, err := third.ListTools(ctx, &mcp.ListToolsParams{Cursor: page.NextCursor}); err != nil {
		t.Fatal(err)
	}
	if _, err := third.CallTool(ctx, &mcp.CallToolParams{Name: "alpha.echo", Meta: mcp.Meta{"progressToken": "broker-progress"}}); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-progress:
		if got != "broker-progress" {
			t.Fatal("progress token changed")
		}
	case <-ctx.Done():
		t.Fatal("broker progress missing")
	}
	if prompt, err := third.GetPrompt(ctx, &mcp.GetPromptParams{Name: "alpha.greeting"}); err != nil || len(prompt.Messages) != 1 {
		t.Fatalf("broker prompt: %+v %v", prompt, err)
	}
	resources, err := third.ListResources(ctx, nil)
	if err != nil || len(resources.Resources) != 1 {
		t.Fatalf("broker resources: %+v %v", resources, err)
	}
	uri := resources.Resources[0].URI
	if read, err := third.ReadResource(ctx, &mcp.ReadResourceParams{URI: uri}); err != nil || read.Contents[0].Text != "sample data" {
		t.Fatalf("broker resource read: %+v %v", read, err)
	}
	if err := third.Subscribe(ctx, &mcp.SubscribeParams{URI: uri}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-f.subscriptionReady:
	case <-ctx.Done():
		t.Fatal("broker subscription acknowledgment missing")
	}
	if err := f.backend.ResourceUpdated(ctx, &mcp.ResourceUpdatedNotificationParams{URI: "memory://sample"}); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-updates:
		if got != uri {
			t.Fatal("broker resource URI changed")
		}
	case <-ctx.Done():
		t.Fatal("broker subscription update missing")
	}
	if err := RevokeClient(ctx, f.store, "work", full.ClientID, f.client); err != nil {
		t.Fatal(err)
	}
	select {
	case <-f.unsubscribed:
	case <-ctx.Done():
		t.Fatal("revocation did not release subscription")
	}
	var cached []byte
	f.editToken(func(p *profile) { cached, _ = json.Marshal(p.Broker) })
	if bytes.Contains(cached, []byte(entry.Secret)) {
		t.Fatal("remote credential store retained plaintext IPC secret")
	}
	if revoked, err := Logout(ctx, f.store, "work", f.client); err != nil || !revoked {
		t.Fatalf("online logout: %t %v", revoked, err)
	}
	status, err := f.store.Status(ctx, "work")
	if err != nil || status.LoggedIn || status.PendingRevocations != 0 {
		t.Fatalf("logout state: %+v %v", status, err)
	}
	if _, err := second.CallTool(ctx, &mcp.CallToolParams{Name: "alpha.echo"}); err == nil {
		t.Fatal("logout left a client active")
	}
	if _, err := f.store.localClient("another", b.ClientID); err == nil || !strings.Contains(err.Error(), "profile") {
		t.Fatal("pairing crossed profiles")
	}
	if err := f.login(ctx, nil); err != nil {
		t.Fatal(err)
	}
	fresh, err := AuthorizeClient(ctx, f.store, "work", ClientOptions{ID: b.ClientID, HTTPClient: f.client, OpenBrowser: open, Output: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if fresh.SessionID == b.SessionID {
		t.Fatal("login revived an ended broker session")
	}
	narrowed, err := jwt.Signed(f.signer).Claims(map[string]any{"iss": f.issuer.URL, "aud": f.hub.URL + "/mcp", "sub": "test-user", "exp": time.Now().Add(time.Hour).Unix(), "scope": ""}).Serialize()
	if err != nil {
		t.Fatal(err)
	}
	var bound clientCredentials
	f.editToken(func(p *profile) { p.Token.AccessToken = narrowed; bound = p.Broker.Clients[b.ClientID] })
	watchCtx, stopWatch := context.WithCancel(ctx)
	defer stopWatch()
	go watchClientGrant(watchCtx, stopWatch, f.store, "work", b.ClientID, bound, f.client)
	select {
	case <-watchCtx.Done():
		if ctx.Err() != nil {
			t.Fatal("scope watch only stopped when its parent timed out")
		}
	case <-ctx.Done():
		t.Fatal("scope reduction did not stop the bound connection")
	}
	f.hub.Close()
	if revoked, err := Logout(ctx, f.store, "work", f.client); err != nil || revoked {
		t.Fatalf("offline revocation reported success: %v %v", revoked, err)
	}
	status, err = f.store.Status(ctx, "work")
	if err != nil || status.LoggedIn || status.PendingRevocations != 1 {
		t.Fatalf("offline logout failed to clear local secrets: %+v %v", status, err)
	}
}
