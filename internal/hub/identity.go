package hub

import (
	"context"
	"encoding/json"
	"slices"
	"strings"

	"github.com/SamuelSupe/mcphub/v2/internal/configstore"
	"github.com/SamuelSupe/mcphub/v2/internal/sso"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func filterIdentityEndpoints(ids []string, p *configstore.EffectiveIdentity) []string {
	return slices.DeleteFunc(slices.Clone(ids), func(id string) bool { return !p.Permissions.AllowsEndpoint(id) })
}

func (v *view) identityMiddleware(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		if v.identity == nil {
			return next(ctx, method, req)
		}
		denied := func() (mcp.Result, error) {
			return nil, &jsonrpc.Error{Code: -32003, Message: configstore.ErrIdentityDenied.Error()}
		}
		extra := req.GetExtra()
		if extra == nil || sso.Identity(extra.TokenInfo) == nil || extra.TokenInfo.UserID != v.identity.ID {
			return denied()
		}
		id := backendForParams(req.GetParams())
		p := v.identity.Permissions
		if id != "" && !p.AllowsEndpoint(id) {
			return denied()
		}
		capability := ""
		if strings.HasPrefix(method, "prompts/") {
			capability = "prompts"
		}
		if strings.HasPrefix(method, "resources/") {
			capability = "resources"
		}
		if method == "resources/subscribe" {
			capability = "subscriptions"
		}
		if params, ok := req.GetParams().(*mcp.CompleteParams); ok && params.Ref != nil {
			capability = "resources"
			if params.Ref.Type == "ref/prompt" {
				capability = "prompts"
			}
		}
		if id != "" && capability != "" && !p.AllowsCapability(id, capability) {
			return denied()
		}
		call, release, err := v.hub.identityStore.AdmitIdentity(ctx, *v.identity)
		if err != nil {
			return denied()
		}
		defer release()
		return next(call, method, req)
	}
}

func (h *Hub) CheckIdentityRequest(p *configstore.EffectiveIdentity, method string, params json.RawMessage, ids []string) error {
	if p == nil {
		return nil
	}
	for _, id := range ids {
		if !p.Permissions.AllowsEndpoint(id) {
			return configstore.ErrIdentityDenied
		}
	}
	if method != "tools/call" {
		return nil
	}
	var call mcp.CallToolParamsRaw
	if json.Unmarshal(params, &call) != nil {
		return configstore.ErrIdentityDenied
	}
	if call.Name == statusApprovalTool || call.Name == resumeApprovalTool || call.Name == cancelApprovalTool {
		return nil
	}
	id, _, ok := h.ToolTarget(call.Name)
	if !ok {
		return configstore.ErrIdentityDenied
	}
	defs := h.backendToolDefinitions(id, false)
	if len(defs) == 0 {
		defs = h.httpToolDefinitions(id)
	}
	d, ok := defs[call.Name]
	if !ok {
		return configstore.ErrIdentityDenied
	}
	_, err := p.Permissions.ToolArguments(id, d.original, d.effect, call.Arguments)
	return err
}

func (h *Hub) pruneIdentityViews(current *configstore.EffectiveIdentity) {
	h.viewsMu.Lock()
	var removed []*view
	for key, v := range h.views {
		if v.identity != nil && (current == nil || (v.identity.ID == current.ID && v.identity.Version != current.Version)) {
			delete(h.views, key)
			removed = append(removed, v)
		}
	}
	h.viewsMu.Unlock()
	for _, v := range removed {
		v.close()
	}
}

func (h *Hub) PruneIdentityViews(ctx context.Context) {
	if h.identityStore == nil {
		return
	}
	h.viewsMu.RLock()
	ids := map[string]bool{}
	for _, v := range h.views {
		if v.identity != nil {
			ids[v.identity.ID] = true
		}
	}
	h.viewsMu.RUnlock()
	for id := range ids {
		current, err := h.identityStore.EffectiveIdentity(ctx, id)
		if err != nil {
			current = configstore.EffectiveIdentity{ID: id}
		}
		h.pruneIdentityViews(&current)
	}
}
