package hub

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func (v *view) toolHandler(definition toolDefinition) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		client, _ := v.hub.manager.Client(definition.backendID)
		params := &mcp.CallToolParams{
			Meta:           req.Params.Meta,
			Name:           definition.original,
			Arguments:      req.Params.Arguments,
			InputResponses: req.Params.InputResponses,
			RequestState:   req.Params.RequestState,
		}
		result, err := client.CallTool(ctx, req.Session, params)
		if err != nil {
			publicError := publicBackendError(definition.backendID, err)
			var protocolError *jsonrpc.Error
			if errors.As(publicError, &protocolError) {
				return nil, protocolError
			}
			failure := &mcp.CallToolResult{}
			failure.SetError(publicError)
			return failure, nil
		}
		return v.hub.rewriteToolResult(definition.backendID, result), nil
	}
}

func (v *view) promptHandler(definition promptDefinition) mcp.PromptHandler {
	return func(ctx context.Context, req *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		client, _ := v.hub.manager.Client(definition.backendID)
		params := *req.Params
		params.Name = definition.original
		result, err := client.GetPrompt(ctx, req.Session, &params)
		if err != nil {
			return nil, publicBackendError(definition.backendID, err)
		}
		return v.hub.rewritePromptResult(definition.backendID, result), nil
	}
}

func (v *view) resourceHandler(backendID, original string) mcp.ResourceHandler {
	return func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		return v.forwardResource(ctx, req.Session, backendID, original, req.Params)
	}
}

func (v *view) issuedResourceHandler(backendID string) mcp.ResourceHandler {
	return func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		resolvedBackend, original, ok := decodeResource(req.Params.URI)
		if !ok || !strings.EqualFold(resolvedBackend, backendID) || !v.hub.wasResourceIssued(backendID, original) {
			return nil, mcp.ResourceNotFoundError(req.Params.URI)
		}
		return v.forwardResource(ctx, req.Session, backendID, original, req.Params)
	}
}

func (v *view) forwardResource(ctx context.Context, upstream *mcp.ServerSession, backendID, original string, params *mcp.ReadResourceParams) (*mcp.ReadResourceResult, error) {
	client, _ := v.hub.manager.Client(backendID)
	forwarded := *params
	forwarded.URI = original
	result, err := client.ReadResource(ctx, upstream, &forwarded)
	if err != nil {
		return nil, publicBackendError(backendID, err)
	}
	return v.hub.rewriteResourceResult(backendID, result), nil
}

func (v *view) templateHandler(route *templateRoute) mcp.ResourceHandler {
	return func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		original, err := route.expand(req.Params.URI)
		if err != nil {
			return nil, err
		}
		client, _ := v.hub.manager.Client(route.backendID)
		params := *req.Params
		params.URI = original
		result, err := client.ReadResource(ctx, req.Session, &params)
		if err != nil {
			return nil, publicBackendError(route.backendID, err)
		}
		return v.hub.rewriteResourceResult(route.backendID, result), nil
	}
}

func (v *view) complete(ctx context.Context, req *mcp.CompleteRequest) (*mcp.CompleteResult, error) {
	if req.Params == nil || req.Params.Ref == nil {
		return nil, fmt.Errorf("completion reference is required")
	}
	params := *req.Params
	ref := *req.Params.Ref
	params.Ref = &ref

	var backendID string
	switch ref.Type {
	case "ref/prompt":
		v.routesMu.RLock()
		route, ok := v.promptRoutes[ref.Name]
		v.routesMu.RUnlock()
		if !ok {
			return nil, fmt.Errorf("unknown prompt %q", ref.Name)
		}
		backendID = route.backendID
		ref.Name = route.original
	case "ref/resource":
		v.routesMu.RLock()
		route, ok := v.templateRoutes[ref.URI]
		v.routesMu.RUnlock()
		if !ok {
			return nil, fmt.Errorf("unknown resource template %q", ref.URI)
		}
		backendID = route.backendID
		ref.URI = route.original
	default:
		return nil, fmt.Errorf("unsupported completion reference %q", ref.Type)
	}
	client, _ := v.hub.manager.Client(backendID)
	result, err := client.Complete(ctx, req.Session, &params)
	if err != nil {
		return nil, publicBackendError(backendID, err)
	}
	return result, nil
}

type backendUnavailableError struct {
	backendID string
}

func (e *backendUnavailableError) Error() string {
	return fmt.Sprintf("backend %s unavailable", e.backendID)
}

func publicBackendError(backendID string, err error) error {
	var protocolError *jsonrpc.Error
	if errors.As(err, &protocolError) {
		return protocolError
	}
	// Network and transport errors can contain internal URLs or URL query
	// credentials. Only backend-authored JSON-RPC errors cross the Hub boundary.
	return &backendUnavailableError{backendID: backendID}
}
