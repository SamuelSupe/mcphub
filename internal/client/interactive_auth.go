package client

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const authStartTool = "mcpbridge_auth_start"
const authStatusTool = "mcpbridge_auth_status"

type interactiveAuth struct {
	mu     sync.Mutex
	store  *Store
	opts   PairOptions
	server *mcp.Server
	status PairStatus
	remote *mcp.ClientSession
	names  []string
}

// ConnectInteractive initializes the local MCP session before contacting the gateway.
// Authentication is explicit; business calls are never queued or replayed.
func ConnectInteractive(ctx context.Context, store *Store, opts PairOptions, connection ConnectOptions) error {
	if opts.Profile == "" {
		opts.Profile = "default"
	}
	if opts.Name == "" {
		return errors.New("--name is required with --interactive-auth")
	}
	if opts.Server == "" {
		return errors.New("--server is required with --interactive-auth")
	}
	opts.HTTPClient = connection.HTTPClient
	state := &interactiveAuth{store: store, opts: opts}
	state.server = mcp.NewServer(&mcp.Implementation{Name: "mcpbridge", Version: "1"}, &mcp.ServerOptions{Capabilities: &mcp.ServerCapabilities{Tools: &mcp.ToolCapabilities{ListChanged: true}}, Instructions: "Call mcpbridge_auth_start once, show the user its verification link and comparison code, and call mcpbridge_auth_status no faster than interval. Status may collect credentials and check the connection. Never ask the user to copy tokens. Business tools appear after ready; refresh the tool list or reconnect using the returned profile and client instance if your Agent cannot refresh. Failed business calls are never replayed."})
	schema := map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}
	state.server.AddTool(&mcp.Tool{Name: authStartTool, Description: "Explicitly start or reuse pairing. Returns only a public link, comparison code and status; never credentials.", InputSchema: schema}, state.start)
	state.server.AddTool(&mcp.Tool{Name: authStatusTool, Description: "Check pairing status. May redeem the one-time response, save private credentials and check MCP connectivity. Ready responses are idempotent. Respect interval; refresh tools after ready.", InputSchema: schema}, state.poll)
	state.server.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if method == "tools/list" {
				state.mu.Lock()
				if state.status.Status == "ready" {
					_ = state.refresh(ctx)
				}
				state.mu.Unlock()
			}
			return next(ctx, method, req)
		}
	})
	var transport mcp.Transport = &mcp.StdioTransport{}
	if connection.Input != nil || connection.Output != nil {
		if connection.Input == nil || connection.Output == nil {
			return errors.New("both connector input and output are required")
		}
		transport = &mcp.IOTransport{Reader: connection.Input, Writer: connection.Output}
	}
	defer func() {
		state.mu.Lock()
		if state.remote != nil {
			state.remote.Close()
		}
		state.mu.Unlock()
	}()
	return state.server.Run(ctx, transport)
}
func publicPairResult(s PairStatus, err error) (*mcp.CallToolResult, error) {
	if err != nil && s.RequestID == "" {
		s.Status = "unavailable"
		s.NextStep = err.Error()
	}
	data, _ := json.Marshal(s)
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(data)}}, StructuredContent: s, IsError: err != nil}, nil
}
func emptyAuthArgs(req *mcp.CallToolRequest) bool {
	if len(req.Params.Arguments) == 0 {
		return true
	}
	var args map[string]any
	return json.Unmarshal(req.Params.Arguments, &args) == nil && len(args) == 0
}
func (s *interactiveAuth) start(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if !emptyAuthArgs(req) {
		return nil, &jsonrpc.Error{Code: -32602, Message: "Authentication tools take no arguments"}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.status.Status == "ready" {
		if err := s.refresh(ctx); err == nil {
			return publicPairResult(s.status, nil)
		}
	}
	if s.status.Status == "pending_user" && time.Now().Before(s.status.ExpiresAt) {
		return publicPairResult(s.status, nil)
	}
	status, err := PairStart(ctx, s.store, s.opts)
	if err == nil {
		s.status = status
		s.status.NextStep = "Show verification_uri_complete and user_code to the user. After browser confirmation, call mcpbridge_auth_status no faster than interval; credentials stay private. Refresh tools/list after ready."
		status = s.status
	}
	return publicPairResult(status, err)
}
func (s *interactiveAuth) poll(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	if !emptyAuthArgs(req) {
		return nil, &jsonrpc.Error{Code: -32602, Message: "Authentication tools take no arguments"}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.status.RequestID == "" {
		return publicPairResult(PairStatus{Status: "not_started", Profile: s.opts.Profile, Server: s.opts.Server, NextStep: "Call mcpbridge_auth_start and show the returned link and comparison code to the user."}, nil)
	}
	if s.status.Status == "ready" {
		err := s.refresh(ctx)
		return publicPairResult(s.status, err)
	}
	if s.status.Status != "pending_user" {
		return publicPairResult(s.status, nil)
	}
	status, err := PairFinish(ctx, s.store, s.status.RequestID, false, s.opts.HTTPClient)
	if status.RequestID != "" {
		s.status = status
	}
	if status.Status == "pending_user" {
		s.status.NextStep = "Waiting for explicit browser approval. Call mcpbridge_auth_status no faster than interval; do not call business tools yet."
	}
	if err == nil && status.Status == "ready" {
		err = s.refresh(ctx)
	}
	return publicPairResult(s.status, err)
}
func (s *interactiveAuth) unavailable() {
	s.server.RemoveTools(s.names...)
	s.names = nil
	if s.remote != nil {
		s.remote.Close()
		s.remote = nil
	}
	s.status.Status = "unavailable"
	s.status.NextStep = "Access is unavailable, expired or revoked. Start authentication explicitly. No business call was replayed."
}
func (s *interactiveAuth) refresh(ctx context.Context) error {
	current, err := verifyPairReady(ctx, s.store, s.status, s.opts.HTTPClient)
	if err != nil || current.Status != "ready" {
		s.unavailable()
		return errors.New("client authorization unavailable; call mcpbridge_auth_start explicitly")
	}
	if s.remote == nil {
		var grantID string
		err = s.store.locked(ctx, s.opts.Profile, func() error {
			p, e := s.store.load(s.opts.Profile)
			if e != nil {
				return e
			}
			if p.Broker == nil {
				return ErrProfileChanged
			}
			grantID = p.Broker.Clients[s.status.ClientID].Grant.GrantID
			return nil
		})
		if err != nil {
			s.unavailable()
			return err
		}
		_, httpConnection, e := connectorHTTPClient(ctx, s.store, s.opts.Profile, ConnectOptions{clientID: s.status.ClientID, grantID: grantID, HTTPClient: s.opts.HTTPClient})
		if e != nil {
			s.unavailable()
			return e
		}
		s.remote, e = mcp.NewClient(&mcp.Implementation{Name: "mcpbridge-interactive", Version: "1"}, nil).Connect(ctx, &mcp.StreamableClientTransport{Endpoint: s.opts.Server, HTTPClient: httpConnection, DisableStandaloneSSE: true, MaxRetries: -1}, nil)
		if e != nil {
			s.unavailable()
			return errors.New("remote MCP connection failed; authenticate explicitly again")
		}
	}
	var tools []*mcp.Tool
	for tool, e := range s.remote.Tools(ctx, nil) {
		if e != nil {
			s.unavailable()
			return errors.New("remote tool catalog unavailable; reconnect explicitly")
		}
		if tool.Name == authStartTool || tool.Name == authStatusTool {
			s.unavailable()
			return errors.New("remote tool conflicts with the reserved authentication namespace")
		}
		tools = append(tools, tool)
	}
	// Change catalog membership only, avoiding spurious list_changed notifications on status checks.
	var names []string
	previous := make(map[string]bool, len(s.names))
	for _, name := range s.names {
		previous[name] = true
	}
	for _, tool := range tools {
		names = append(names, tool.Name)
		if previous[tool.Name] {
			delete(previous, tool.Name)
			continue
		}
		remoteName := tool.Name
		s.server.AddTool(tool, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			s.mu.Lock()
			remote := s.remote
			s.mu.Unlock()
			if remote == nil {
				return nil, &jsonrpc.Error{Code: -32003, Message: "Client authorization unavailable; authenticate explicitly; this call was not replayed"}
			}
			result, e := remote.CallTool(ctx, &mcp.CallToolParams{Name: remoteName, Arguments: json.RawMessage(req.Params.Arguments)})
			if e != nil {
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
				s.mu.Lock()
				if s.remote == remote {
					s.unavailable()
				}
				s.mu.Unlock()
				return nil, &jsonrpc.Error{Code: -32003, Message: "Remote call failed; check authorization and reconnect explicitly. This call was not replayed"}
			}
			return result, nil
		})
	}
	for name := range previous {
		s.server.RemoveTools(name)
	}
	s.names = names
	s.status.NextStep = "Connection ready. Refresh tools/list to see authorized business tools. If your Agent cannot refresh, reconnect with mcpbridge connect --profile " + s.status.Profile + " --client " + s.status.ClientID + "."
	return nil
}
