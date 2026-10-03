package hub

import (
	"context"
	"strings"

	"github.com/SamuelSupe/mcphub/v2/internal/configstore"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func (v *view) grantsValid(ctx context.Context) bool {
	for _, g := range v.allGrants() {
		if v.hub.grantStore.ValidateClientGrant(ctx, g) != nil {
			return false
		}
	}
	return true
}

func (v *view) collectionMiddleware(ctx context.Context, method string, req mcp.Request, next mcp.MethodHandler) (mcp.Result, error) {
	extra := req.GetExtra()
	capable := false
	for _, grant := range v.allGrants() {
		if extra == nil || extra.TokenInfo == nil || extra.TokenInfo.UserID != grant.Subject || extra.TokenInfo.Extra["issuer"] != grant.Issuer {
			return nil, &jsonrpc.Error{Code: -32003, Message: string(configstore.ErrGrantInvalid)}
		}
		allowed := true
		switch {
		case strings.HasPrefix(method, "tools/"):
			allowed = grant.Capabilities.Tools
		case strings.HasPrefix(method, "prompts/"):
			allowed = grant.Capabilities.Prompts
		case strings.HasPrefix(method, "resources/"):
			allowed = grant.Capabilities.Resources
		case method == "subscriptions/listen":
			allowed = grant.Capabilities.Subscriptions
		}
		capable = capable || allowed
	}
	if !capable && (strings.HasPrefix(method, "tools/") || strings.HasPrefix(method, "prompts/") || strings.HasPrefix(method, "resources/") || method == "subscriptions/listen") {
		return nil, &jsonrpc.Error{Code: -32003, Message: string(configstore.ErrGrantInsufficient)}
	}
	ctx, release, err := v.admitGrant(ctx)
	if err != nil {
		return nil, &jsonrpc.Error{Code: -32003, Message: err.Error()}
	}
	defer release()
	return next(ctx, method, req)
}
