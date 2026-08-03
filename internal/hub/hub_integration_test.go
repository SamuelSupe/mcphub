package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/SamuelSupe/mcphub/internal/backend"
	"github.com/SamuelSupe/mcphub/internal/config"
	"github.com/SamuelSupe/mcphub/internal/mcpcompat"
)

func TestHubAggregatesAndRoutesProtocolFeatures(t *testing.T) {
	alpha := startAggregationBackend(t, "alpha", true)
	beta := startAggregationBackend(t, "beta", false)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := aggregationConfig(alpha.httpServer.URL, beta.httpServer.URL)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	var aggregate *Hub
	manager := backend.NewManager(
		cfg,
		logger,
		func(id string) {
			if aggregate != nil {
				aggregate.ReconcileBackend(id)
			}
		},
		func(id, uri string) {
			if aggregate != nil {
				aggregate.ResourceUpdated(id, uri)
			}
		},
	)
	aggregate = New(cfg, manager, logger)
	if err := manager.Start(ctx, false); err != nil {
		t.Fatalf("start backend manager: %v", err)
	}
	legacyConnected := false
	for backendSession := range beta.server.Sessions() {
		if params := backendSession.InitializeParams(); params != nil && params.ProtocolVersion == "2025-11-25" {
			legacyConnected = true
		}
	}
	if !legacyConnected {
		t.Fatal("beta backend did not negotiate the legacy Streamable HTTP protocol")
	}
	t.Cleanup(func() {
		aggregate.Close()
		manager.Close()
	})

	handler := mcp.NewStreamableHTTPHandler(aggregate.ServerForRequest, &mcp.StreamableHTTPOptions{
		Stateless:                    true,
		PropagateRequestCancellation: true,
	})
	hubHTTP := httptest.NewServer(handler)
	t.Cleanup(func() {
		hubHTTP.CloseClientConnections()
		hubHTTP.Close()
	})

	progress := make(chan *mcp.ProgressNotificationParams, 4)
	listChanged := make(chan struct{}, 4)
	resourceListChanged := make(chan struct{}, 4)
	resourceUpdates := make(chan string, 4)
	subscriptionReady := make(chan struct{}, 1)
	client := mcp.NewClient(&mcp.Implementation{Name: "hub-test", Version: "1"}, &mcp.ClientOptions{
		MultiRoundTrip: &mcp.MultiRoundTripOptions{Disabled: true},
		ToolListChangedHandler: func(context.Context, *mcp.ToolListChangedRequest) {
			select {
			case listChanged <- struct{}{}:
			default:
			}
		},
		ResourceListChangedHandler: func(context.Context, *mcp.ResourceListChangedRequest) {
			select {
			case resourceListChanged <- struct{}{}:
			default:
			}
		},
		ProgressNotificationHandler: func(_ context.Context, req *mcp.ProgressNotificationClientRequest) {
			if req != nil && req.Params != nil {
				progress <- req.Params
			}
		},
		ResourceUpdatedHandler: func(_ context.Context, req *mcp.ResourceUpdatedNotificationRequest) {
			if req != nil && req.Params != nil {
				resourceUpdates <- req.Params.URI
			}
		},
	})
	client.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if method == "notifications/subscriptions/acknowledged" {
				select {
				case subscriptionReady <- struct{}{}:
				default:
				}
			}
			return next(ctx, method, req)
		}
	})
	session, err := client.Connect(ctx, compatClientTransport(hubHTTP.URL), nil)
	if err != nil {
		t.Fatalf("connect hub client: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	select {
	case <-subscriptionReady:
	case <-ctx.Done():
		t.Fatal("timed out waiting for subscriptions/listen acknowledgment")
	}

	tools := listAllTools(t, ctx, session)
	toolNames := featureNames(tools, func(tool *mcp.Tool) string { return tool.Name })
	if want := []string{"alpha.confirm", "alpha.echo", "alpha.slow", "beta.echo", "beta.slow"}; !slices.Equal(toolNames, want) {
		t.Fatalf("tool names = %v, want %v", toolNames, want)
	}
	prompts := listAllPrompts(t, ctx, session)
	if got := featureNames(prompts, func(prompt *mcp.Prompt) string { return prompt.Name }); !slices.Equal(got, []string{"alpha.welcome", "beta.welcome"}) {
		t.Fatalf("prompt names = %v", got)
	}
	resources := listAllResources(t, ctx, session)
	if len(resources) != 2 {
		t.Fatalf("resource count = %d, want 2", len(resources))
	}
	for _, resource := range resources {
		backendID, original, ok := decodeResource(resource.URI)
		if !ok || original != "memory://shared" || backendID == "" {
			t.Fatalf("invalid aggregated resource URI %q", resource.URI)
		}
	}
	templates := listAllTemplates(t, ctx, session)
	if len(templates) != 2 {
		t.Fatalf("template count = %d, want 2", len(templates))
	}

	result, err := session.CallTool(ctx, &mcp.CallToolParams{
		Meta:      mcp.Meta{"progressToken": "client-progress"},
		Name:      "alpha.echo",
		Arguments: map[string]any{"message": "hello"},
	})
	if err != nil {
		t.Fatalf("call alpha.echo: %v", err)
	}
	if got := firstText(result.Content); got != "alpha:hello" {
		t.Fatalf("tool result text = %q", got)
	}
	link, ok := result.Content[1].(*mcp.ResourceLink)
	if !ok {
		t.Fatalf("tool content[1] = %T, want ResourceLink", result.Content[1])
	}
	if backendID, original, ok := decodeResource(link.URI); !ok || backendID != "alpha" || original != "memory://shared" {
		t.Fatalf("rewritten resource link = %q", link.URI)
	}
	if got := result.Meta["backend"]; got != "alpha" {
		t.Fatalf("tool result metadata backend = %v", got)
	}
	select {
	case update := <-progress:
		if update.ProgressToken != "client-progress" || update.Progress != 1 {
			t.Fatalf("progress = %#v", update)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for forwarded progress")
	}

	read, err := session.ReadResource(ctx, &mcp.ReadResourceParams{URI: link.URI})
	if err != nil {
		t.Fatalf("read rewritten resource: %v", err)
	}
	if len(read.Contents) != 1 || read.Contents[0].URI != link.URI || read.Contents[0].Text != "alpha:memory://shared" {
		t.Fatalf("read resource result = %#v", read.Contents)
	}

	prompt, err := session.GetPrompt(ctx, &mcp.GetPromptParams{Name: "beta.welcome"})
	if err != nil {
		t.Fatalf("get beta prompt: %v", err)
	}
	embedded, ok := prompt.Messages[0].Content.(*mcp.EmbeddedResource)
	if !ok || embedded.Resource == nil {
		t.Fatalf("prompt content = %T, want EmbeddedResource", prompt.Messages[0].Content)
	}
	if backendID, original, ok := decodeResource(embedded.Resource.URI); !ok || backendID != "beta" || original != "memory://prompt" {
		t.Fatalf("rewritten embedded URI = %q", embedded.Resource.URI)
	}

	alphaTemplate := templateForBackend(t, templates, "alpha")
	completion, err := session.Complete(ctx, &mcp.CompleteParams{
		Ref:      &mcp.CompleteReference{Type: "ref/resource", URI: alphaTemplate.URITemplate},
		Argument: mcp.CompleteParamsArgument{Name: "id", Value: "4"},
	})
	if err != nil {
		t.Fatalf("complete resource template: %v", err)
	}
	if got := completion.Completion.Values; !slices.Equal(got, []string{"alpha-4"}) {
		t.Fatalf("completion values = %v", got)
	}

	inputRequired, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "alpha.confirm"})
	if err != nil {
		t.Fatalf("initial MRTR call: %v", err)
	}
	if !inputRequired.NeedsInput() || inputRequired.RequestState != "alpha-state" || inputRequired.InputRequests["approval"] == nil {
		t.Fatalf("initial MRTR result = %#v", inputRequired)
	}
	completed, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:         "alpha.confirm",
		RequestState: inputRequired.RequestState,
		InputResponses: mcp.InputResponseMap{
			"approval": &mcp.ElicitResult{Action: "accept", Content: map[string]any{"approved": true}},
		},
	})
	if err != nil {
		t.Fatalf("MRTR retry: %v", err)
	}
	if completed.NeedsInput() || firstText(completed.Content) != "alpha:approved" {
		t.Fatalf("completed MRTR result = %#v", completed)
	}
	select {
	case retry := <-alpha.mrtrRetries:
		if retry.RequestState != "alpha-state" || retry.InputResponses["approval"] == nil {
			t.Fatalf("forwarded MRTR params = %#v", retry)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for MRTR retry")
	}

	expandedURI := strings.TrimSuffix(alphaTemplate.URITemplate, "{?id}") + "?id=42"
	templateRead, err := session.ReadResource(ctx, &mcp.ReadResourceParams{URI: expandedURI})
	if err != nil {
		t.Fatalf("read resource template: %v", err)
	}
	_, templateOriginal, ok := decodeResource(templateRead.Contents[0].URI)
	if !ok || templateOriginal != "memory://items/42" {
		t.Fatalf("template read URI = %q", templateRead.Contents[0].URI)
	}
	if err := session.Subscribe(ctx, &mcp.SubscribeParams{URI: expandedURI}); err != nil {
		t.Fatalf("subscribe expanded template resource: %v", err)
	}
	waitAtomicValue(t, ctx, &alpha.subscribeCalls, 1, "downstream template subscription")

drainResourceChanges:
	for {
		select {
		case <-resourceListChanged:
		default:
			break drainResourceChanges
		}
	}
	alpha.server.RemoveResourceTemplates("memory://items/{id}")
	for {
		select {
		case <-resourceListChanged:
		case <-ctx.Done():
			t.Fatal("timed out waiting for aggregate resource list change")
		}
		currentTemplates := listAllTemplates(t, ctx, session)
		if !slices.ContainsFunc(currentTemplates, func(template *mcp.ResourceTemplate) bool {
			return strings.HasPrefix(template.URITemplate, "mcphub://alpha/t/")
		}) {
			break
		}
	}
	if err := session.Unsubscribe(ctx, &mcp.UnsubscribeParams{URI: expandedURI}); err != nil {
		t.Fatalf("unsubscribe resource after template removal: %v", err)
	}
	waitAtomicValue(t, ctx, &alpha.unsubscribeCalls, 1, "downstream removed-template unsubscription")

	alpha.addLateTool()
	select {
	case <-listChanged:
	case <-ctx.Done():
		t.Fatal("timed out waiting for aggregated tool list change")
	}
	tools = listAllTools(t, ctx, session)
	if names := featureNames(tools, func(tool *mcp.Tool) string { return tool.Name }); !slices.Contains(names, "alpha.late") {
		t.Fatalf("refreshed tools do not contain alpha.late: %v", names)
	}

	if err := session.Subscribe(ctx, &mcp.SubscribeParams{URI: link.URI}); err != nil {
		t.Fatalf("subscribe aggregated resource: %v", err)
	}
	waitAtomicValue(t, ctx, &alpha.subscribeCalls, 2, "downstream subscription")
	if err := alpha.server.ResourceUpdated(ctx, &mcp.ResourceUpdatedNotificationParams{URI: "memory://shared"}); err != nil {
		t.Fatalf("send downstream resource update: %v", err)
	}
	select {
	case uri := <-resourceUpdates:
		if uri != link.URI {
			t.Fatalf("resource update URI = %q, want %q", uri, link.URI)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for resource update")
	}
	if err := session.Unsubscribe(ctx, &mcp.UnsubscribeParams{URI: link.URI}); err != nil {
		t.Fatalf("unsubscribe aggregated resource: %v", err)
	}
	// The SDK's stateless HTTP listen stream observes cancellation on its next
	// write; drive that boundary so the server-side unsubscribe defer runs.
	_ = alpha.server.ResourceUpdated(ctx, &mcp.ResourceUpdatedNotificationParams{URI: "memory://shared"})
	waitAtomicValue(t, ctx, &alpha.unsubscribeCalls, 2, "downstream unsubscription")

	cancelClient := mcp.NewClient(&mcp.Implementation{Name: "cancel-test", Version: "1"}, &mcp.ClientOptions{
		MultiRoundTrip: &mcp.MultiRoundTripOptions{Disabled: true},
	})
	cancelSession, err := cancelClient.Connect(ctx, compatClientTransport(hubHTTP.URL), nil)
	if err != nil {
		t.Fatalf("connect cancellation client: %v", err)
	}
	t.Cleanup(func() { _ = cancelSession.Close() })
	slowCtx, cancelSlow := context.WithCancel(ctx)
	slowResult := make(chan error, 1)
	go func() {
		_, err := cancelSession.CallTool(slowCtx, &mcp.CallToolParams{Name: "beta.slow"})
		slowResult <- err
	}()
	select {
	case <-beta.slowStarted:
		cancelSlow()
	case <-ctx.Done():
		t.Fatal("timed out waiting for downstream slow tool")
	}
	select {
	case <-beta.slowCanceled:
	case <-ctx.Done():
		t.Fatal("downstream tool did not observe cancellation")
	}
	select {
	case err := <-slowResult:
		if err == nil {
			t.Fatal("canceled tool call returned no error")
		}
	case <-ctx.Done():
		t.Fatal("canceled tool call did not return")
	}

	legacyInitialize, legacySessionID := legacyRPCCall(t, ctx, hubHTTP.URL, "", map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "initialize",
		"params": map[string]any{
			"protocolVersion": "2025-11-25",
			"capabilities":    map[string]any{},
			"clientInfo":      map[string]any{"name": "legacy-test", "version": "1"},
		},
	})
	initializeResult, _ := legacyInitialize["result"].(map[string]any)
	if initializeResult["protocolVersion"] != "2025-11-25" {
		t.Fatalf("legacy negotiated version = %v", initializeResult["protocolVersion"])
	}
	legacyRPCCall(t, ctx, hubHTTP.URL, legacySessionID, map[string]any{
		"jsonrpc": "2.0",
		"method":  "notifications/initialized",
	})
	legacyToolResponse, _ := legacyRPCCall(t, ctx, hubHTTP.URL, legacySessionID, map[string]any{
		"jsonrpc": "2.0",
		"id":      2,
		"method":  "tools/list",
		"params":  map[string]any{},
	})
	legacyTools, _ := legacyToolResponse["result"].(map[string]any)
	listed, _ := legacyTools["tools"].([]any)
	if len(listed) == 0 {
		t.Fatal("legacy Streamable HTTP client received no tools")
	}

	beta.httpServer.CloseClientConnections()
	beta.httpServer.Close()
	betaClient, _ := manager.Client("beta")
	for betaClient.Ready() {
		select {
		case <-time.After(10 * time.Millisecond):
		case <-ctx.Done():
			t.Fatal("beta backend did not become unavailable")
		}
	}
	unavailable, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "beta.echo", Arguments: map[string]any{}})
	if err != nil {
		t.Fatalf("call disconnected backend: %v", err)
	}
	if !unavailable.IsError || firstText(unavailable.Content) != "backend beta unavailable" {
		t.Fatalf("disconnected backend result = %#v", unavailable)
	}
	if strings.Contains(firstText(unavailable.Content), beta.httpServer.URL) || strings.Contains(firstText(unavailable.Content), "127.0.0.1") {
		t.Fatalf("disconnected backend result exposed its transport URL: %q", firstText(unavailable.Content))
	}
}

func TestCanceledModernSubscriptionUnsubscribesLegacyBackend(t *testing.T) {
	beta := startAggregationBackend(t, "beta", false)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := &config.Config{
		Server: config.ServerConfig{
			PublicURL:           "https://hub.example.com/mcp",
			PageSize:            10,
			RequestTimeout:      config.Duration{Duration: 5 * time.Second},
			DrainTimeout:        config.Duration{Duration: time.Second},
			RefreshInterval:     config.Duration{Duration: time.Hour},
			CatalogTTL:          config.Duration{Duration: 30 * time.Second},
			MaxRequestBodyBytes: 4 << 20,
		},
		Backends: []config.BackendConfig{{
			ID:                "beta",
			URL:               beta.httpServer.URL,
			AllowInsecureHTTP: true,
			RequestTimeout:    config.Duration{Duration: 5 * time.Second},
		}},
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	var aggregate *Hub
	manager := backend.NewManager(
		cfg,
		logger,
		func(id string) {
			if aggregate != nil {
				aggregate.ReconcileBackend(id)
			}
		},
		func(id, uri string) {
			if aggregate != nil {
				aggregate.ResourceUpdated(id, uri)
			}
		},
	)
	aggregate = New(cfg, manager, logger)
	if err := manager.Start(ctx, false); err != nil {
		t.Fatalf("start legacy backend manager: %v", err)
	}
	t.Cleanup(func() {
		aggregate.Close()
		manager.Close()
	})

	hubHTTP := httptest.NewServer(mcp.NewStreamableHTTPHandler(
		aggregate.ServerForRequest,
		&mcp.StreamableHTTPOptions{Stateless: true, PropagateRequestCancellation: true},
	))
	t.Cleanup(func() {
		hubHTTP.CloseClientConnections()
		hubHTTP.Close()
	})

	client := mcp.NewClient(&mcp.Implementation{Name: "modern-subscription-test", Version: "1"}, &mcp.ClientOptions{
		MultiRoundTrip: &mcp.MultiRoundTripOptions{Disabled: true},
	})
	session, err := client.Connect(ctx, compatClientTransport(hubHTTP.URL), nil)
	if err != nil {
		t.Fatalf("connect modern hub client: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	if negotiated := session.InitializeResult().ProtocolVersion; negotiated < "2026-07-28" {
		t.Fatalf("hub client negotiated %q, want modern subscriptions/listen protocol", negotiated)
	}

	resources := listAllResources(t, ctx, session)
	var listed *mcp.Resource
	for _, resource := range resources {
		backendID, original, ok := decodeResource(resource.URI)
		if ok && backendID == "beta" && original == "memory://shared" {
			listed = resource
			break
		}
	}
	if listed == nil {
		t.Fatal("modern hub client did not receive beta resource")
	}
	if err := session.Subscribe(ctx, &mcp.SubscribeParams{URI: listed.URI}); err != nil {
		t.Fatalf("subscribe modern hub resource: %v", err)
	}
	waitAtomicValue(t, ctx, &beta.subscribeCalls, 1, "legacy backend subscription")

	aggregate.viewsMu.RLock()
	var candidate *view
	for _, current := range aggregate.views {
		candidate = current
		break
	}
	aggregate.viewsMu.RUnlock()
	if candidate == nil {
		t.Fatal("hub did not create a view for the modern session")
	}
	var upstream *mcp.ServerSession
	for current := range candidate.server.Sessions() {
		upstream = current
		break
	}
	if upstream == nil {
		t.Fatal("hub view has no server session for the modern client")
	}

	// A disconnecting modern subscriptions/listen request carries a canceled
	// context into the hub cleanup path. The legacy backend must still receive
	// the real unsubscribe RPC under the backend timeout.
	canceledCtx, cancelRequest := context.WithCancel(ctx)
	cancelRequest()
	if err := candidate.unsubscribe(canceledCtx, &mcp.UnsubscribeRequest{
		Session: upstream,
		Params:  &mcp.UnsubscribeParams{URI: listed.URI},
	}); err != nil {
		t.Fatalf("canceled modern unsubscribe: %v", err)
	}
	waitAtomicValue(t, ctx, &beta.unsubscribeCalls, 1, "legacy backend unsubscription after modern cancellation")
}

type aggregationBackend struct {
	id               string
	server           *mcp.Server
	httpServer       *httptest.Server
	mrtrRetries      chan *mcp.CallToolParamsRaw
	slowStarted      chan struct{}
	slowCanceled     chan struct{}
	subscribeCalls   atomic.Int32
	unsubscribeCalls atomic.Int32
}

func startAggregationBackend(t *testing.T, id string, includeMRTR bool) *aggregationBackend {
	t.Helper()
	backend := &aggregationBackend{
		id:           id,
		mrtrRetries:  make(chan *mcp.CallToolParamsRaw, 2),
		slowStarted:  make(chan struct{}, 1),
		slowCanceled: make(chan struct{}, 1),
	}
	backend.server = mcp.NewServer(
		&mcp.Implementation{Name: id + "-backend", Version: "1"},
		&mcp.ServerOptions{
			PageSize: 1,
			CompletionHandler: func(_ context.Context, req *mcp.CompleteRequest) (*mcp.CompleteResult, error) {
				if req.Params.Ref.Type == "ref/resource" && req.Params.Ref.URI != "memory://items/{id}" {
					return nil, fmt.Errorf("unexpected template reference %q", req.Params.Ref.URI)
				}
				if req.Params.Ref.Type == "ref/prompt" && req.Params.Ref.Name != "welcome" {
					return nil, fmt.Errorf("unexpected prompt reference %q", req.Params.Ref.Name)
				}
				return &mcp.CompleteResult{Completion: mcp.CompletionResultDetails{Values: []string{id + "-" + req.Params.Argument.Value}}}, nil
			},
			SubscribeHandler: func(_ context.Context, req *mcp.SubscribeRequest) error {
				if req.Params.URI != "memory://shared" && req.Params.URI != "memory://items/42" {
					return fmt.Errorf("unexpected subscription URI %q", req.Params.URI)
				}
				backend.subscribeCalls.Add(1)
				return nil
			},
			UnsubscribeHandler: func(_ context.Context, req *mcp.UnsubscribeRequest) error {
				if req.Params.URI != "memory://shared" && req.Params.URI != "memory://items/42" {
					return fmt.Errorf("unexpected unsubscription URI %q", req.Params.URI)
				}
				backend.unsubscribeCalls.Add(1)
				return nil
			},
		},
	)
	backend.addEchoTool()
	if includeMRTR {
		backend.server.AddTool(objectTool("confirm"), func(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if req.Params.RequestState == "" {
				return &mcp.CallToolResult{
					InputRequests: mcp.InputRequestMap{
						"approval": &mcp.ElicitParams{
							Message: "Approve?",
							RequestedSchema: map[string]any{
								"type":       "object",
								"properties": map[string]any{"approved": map[string]any{"type": "boolean"}},
							},
						},
					},
					RequestState: id + "-state",
				}, nil
			}
			copyParams := *req.Params
			backend.mrtrRetries <- &copyParams
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: id + ":approved"}}}, nil
		})
	}
	backend.server.AddTool(objectTool("slow"), func(ctx context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		backend.slowStarted <- struct{}{}
		<-ctx.Done()
		backend.slowCanceled <- struct{}{}
		return nil, ctx.Err()
	})
	backend.server.AddPrompt(&mcp.Prompt{Name: "welcome"}, func(_ context.Context, _ *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		return &mcp.GetPromptResult{Messages: []*mcp.PromptMessage{{
			Role: mcp.Role("user"),
			Content: &mcp.EmbeddedResource{Resource: &mcp.ResourceContents{
				URI:  "memory://prompt",
				Text: id + " prompt",
			}},
		}}}, nil
	})
	resourceHandler := func(_ context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{
			URI:  req.Params.URI,
			Text: id + ":" + req.Params.URI,
		}}}, nil
	}
	backend.server.AddResource(&mcp.Resource{Name: id + " shared", URI: "memory://shared"}, resourceHandler)
	backend.server.AddResourceTemplate(&mcp.ResourceTemplate{Name: id + " items", URITemplate: "memory://items/{id}"}, resourceHandler)
	legacy := id == "beta"
	var handler http.Handler = mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return backend.server },
		&mcp.StreamableHTTPOptions{Stateless: !legacy, PropagateRequestCancellation: !legacy},
	)
	if legacy {
		handler = legacyOnlyBackend(handler)
	}
	backend.httpServer = httptest.NewServer(handler)
	t.Cleanup(func() {
		backend.httpServer.CloseClientConnections()
		backend.httpServer.Close()
	})
	return backend
}

func legacyOnlyBackend(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Method == http.MethodPost {
			body, err := io.ReadAll(req.Body)
			if err == nil {
				req.Body = io.NopCloser(strings.NewReader(string(body)))
				var envelope struct {
					ID     json.RawMessage `json:"id"`
					Method string          `json:"method"`
				}
				if json.Unmarshal(body, &envelope) == nil && envelope.Method == "server/discover" {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusNotFound)
					_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"error":{"code":-32601,"message":"Method not found"}}`, envelope.ID)
					return
				}
			}
		}
		next.ServeHTTP(w, req)
	})
}

func (b *aggregationBackend) addEchoTool() {
	b.server.AddTool(objectTool("echo"), func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var args struct {
			Message string `json:"message"`
		}
		if len(req.Params.Arguments) > 0 {
			if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
				return nil, err
			}
		}
		if token := req.Params.GetProgressToken(); token != nil {
			if err := req.Session.NotifyProgress(ctx, &mcp.ProgressNotificationParams{
				ProgressToken: token,
				Progress:      1,
				Total:         1,
				Message:       "done",
			}); err != nil {
				return nil, err
			}
		}
		return &mcp.CallToolResult{
			Meta: mcp.Meta{"backend": b.id},
			Content: []mcp.Content{
				&mcp.TextContent{Text: b.id + ":" + args.Message},
				&mcp.ResourceLink{URI: "memory://shared", Name: "shared"},
			},
			StructuredContent: map[string]any{"backend": b.id},
		}, nil
	})
}

func (b *aggregationBackend) addLateTool() {
	b.server.AddTool(objectTool("late"), func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: b.id + ":late"}}}, nil
	})
}

func objectTool(name string) *mcp.Tool {
	return &mcp.Tool{Name: name, InputSchema: map[string]any{"type": "object"}}
}

func compatClientTransport(endpoint string) *mcp.StreamableClientTransport {
	return &mcp.StreamableClientTransport{
		Endpoint: endpoint,
		HTTPClient: &http.Client{Transport: &mcpcompat.RoundTripper{
			Base: http.DefaultTransport,
		}},
	}
}

func aggregationConfig(alphaURL, betaURL string) *config.Config {
	return &config.Config{
		Server: config.ServerConfig{
			PublicURL:           "https://hub.example.com/mcp",
			PageSize:            1,
			RequestTimeout:      config.Duration{Duration: 5 * time.Second},
			DrainTimeout:        config.Duration{Duration: time.Second},
			RefreshInterval:     config.Duration{Duration: time.Hour},
			CatalogTTL:          config.Duration{Duration: 30 * time.Second},
			MaxRequestBodyBytes: 4 << 20,
		},
		Auth: config.AuthConfig{Issuer: "https://idp.example.com"},
		Backends: []config.BackendConfig{
			{ID: "alpha", URL: alphaURL, AllowInsecureHTTP: true, RequestTimeout: config.Duration{Duration: 5 * time.Second}},
			{ID: "beta", URL: betaURL, AllowInsecureHTTP: true, RequestTimeout: config.Duration{Duration: 5 * time.Second}},
		},
	}
}

func listAllTools(t *testing.T, ctx context.Context, session *mcp.ClientSession) []*mcp.Tool {
	t.Helper()
	var result []*mcp.Tool
	for cursor := ""; ; {
		page, err := session.ListTools(ctx, &mcp.ListToolsParams{Cursor: cursor})
		if err != nil {
			t.Fatalf("list tools: %v", err)
		}
		result = append(result, page.Tools...)
		if page.NextCursor == "" {
			return result
		}
		cursor = page.NextCursor
	}
}

func listAllPrompts(t *testing.T, ctx context.Context, session *mcp.ClientSession) []*mcp.Prompt {
	t.Helper()
	var result []*mcp.Prompt
	for cursor := ""; ; {
		page, err := session.ListPrompts(ctx, &mcp.ListPromptsParams{Cursor: cursor})
		if err != nil {
			t.Fatalf("list prompts: %v", err)
		}
		result = append(result, page.Prompts...)
		if page.NextCursor == "" {
			return result
		}
		cursor = page.NextCursor
	}
}

func listAllResources(t *testing.T, ctx context.Context, session *mcp.ClientSession) []*mcp.Resource {
	t.Helper()
	var result []*mcp.Resource
	for cursor := ""; ; {
		page, err := session.ListResources(ctx, &mcp.ListResourcesParams{Cursor: cursor})
		if err != nil {
			t.Fatalf("list resources: %v", err)
		}
		result = append(result, page.Resources...)
		if page.NextCursor == "" {
			return result
		}
		cursor = page.NextCursor
	}
}

func listAllTemplates(t *testing.T, ctx context.Context, session *mcp.ClientSession) []*mcp.ResourceTemplate {
	t.Helper()
	var result []*mcp.ResourceTemplate
	for cursor := ""; ; {
		page, err := session.ListResourceTemplates(ctx, &mcp.ListResourceTemplatesParams{Cursor: cursor})
		if err != nil {
			t.Fatalf("list resource templates: %v", err)
		}
		result = append(result, page.ResourceTemplates...)
		if page.NextCursor == "" {
			return result
		}
		cursor = page.NextCursor
	}
}

func featureNames[T any](features []T, name func(T) string) []string {
	result := make([]string, 0, len(features))
	for _, feature := range features {
		result = append(result, name(feature))
	}
	slices.Sort(result)
	return result
}

func templateForBackend(t *testing.T, templates []*mcp.ResourceTemplate, backendID string) *mcp.ResourceTemplate {
	t.Helper()
	prefix := "mcphub://" + backendID + "/t/"
	for _, template := range templates {
		if strings.HasPrefix(template.URITemplate, prefix) {
			return template
		}
	}
	t.Fatalf("no template for backend %q", backendID)
	return nil
}

func firstText(content []mcp.Content) string {
	if len(content) == 0 {
		return ""
	}
	text, _ := content[0].(*mcp.TextContent)
	if text == nil {
		return ""
	}
	return text.Text
}

func waitAtomicValue(t *testing.T, ctx context.Context, value *atomic.Int32, want int32, label string) {
	t.Helper()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if got := value.Load(); got == want {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("timed out waiting for %s; got %d, want %d", label, value.Load(), want)
		case <-ticker.C:
		}
	}
}

func legacyRPCCall(t *testing.T, ctx context.Context, endpoint, sessionID string, payload any) (map[string]any, string) {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal legacy request: %v", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(string(body)))
	if err != nil {
		t.Fatalf("new legacy request: %v", err)
	}
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Mcp-Protocol-Version", "2025-11-25")
	if sessionID != "" {
		req.Header.Set("Mcp-Session-Id", sessionID)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("legacy request: %v", err)
	}
	defer resp.Body.Close()
	responseSessionID := resp.Header.Get("Mcp-Session-Id")
	if responseSessionID == "" {
		responseSessionID = sessionID
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read legacy response: %v", err)
	}
	if resp.StatusCode == http.StatusAccepted && len(data) == 0 {
		return nil, responseSessionID
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		t.Fatalf("legacy response status = %d, body = %s", resp.StatusCode, data)
	}
	if strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		for line := range strings.Lines(string(data)) {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "data:") {
				data = []byte(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
				break
			}
		}
	}
	var response map[string]any
	if err := json.Unmarshal(data, &response); err != nil {
		t.Fatalf("decode legacy response %q: %v", data, err)
	}
	if response["error"] != nil {
		t.Fatalf("legacy RPC error: %v", response["error"])
	}
	return response, responseSessionID
}
