package hub

import (
	"context"
	"encoding/json"
	"maps"
	"slices"
	"strings"

	"github.com/SamuelSupe/mcphub/v2/internal/configstore"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type clientGrantKey struct{}

func WithClientGrant(ctx context.Context, grant configstore.ClientGrant) context.Context {
	return context.WithValue(ctx, clientGrantKey{}, &grant)
}

func ClientGrantFromContext(ctx context.Context) *configstore.ClientGrant {
	g, _ := ctx.Value(clientGrantKey{}).(*configstore.ClientGrant)
	return g
}

func (h *Hub) CheckClientGrantRequest(g configstore.ClientGrant, method string, params json.RawMessage, tokenScopes []string) error {
	if strings.HasPrefix(method, "tools/") && !g.Capabilities.Tools {
		return configstore.ErrGrantInsufficient
	}
	if strings.HasPrefix(method, "prompts/") && !g.Capabilities.Prompts {
		return configstore.ErrGrantInsufficient
	}
	if strings.HasPrefix(method, "resources/") && !g.Capabilities.Resources {
		return configstore.ErrGrantInsufficient
	}
	if (method == "subscriptions/listen" || method == "resources/subscribe") && !g.Capabilities.Subscriptions {
		return configstore.ErrGrantInsufficient
	}
	if method != "tools/call" {
		return nil
	}
	var call mcp.CallToolParamsRaw
	if json.Unmarshal(params, &call) != nil {
		return configstore.ErrGrantInsufficient
	}
	if call.Name == statusApprovalTool || call.Name == resumeApprovalTool || call.Name == cancelApprovalTool {
		return nil
	}
	defs := h.backendToolDefinitions(g.EndpointID, false)
	if len(defs) == 0 {
		defs = h.httpToolDefinitions(g.EndpointID)
	}
	d, ok := defs[call.Name]
	if !ok || !g.AllowsTool(d.backendID, d.original, d.effect, call.Arguments) {
		return configstore.ErrGrantInsufficient
	}
	if g.ToolPolicies[d.original] != grantToolPolicy(d) {
		return configstore.ErrGrantReconfirmation
	}
	effective := g.EffectiveScopes(tokenScopes)
	missing, known := h.MissingScopes(g.EndpointID, effective)
	if !known || len(missing) > 0 || slices.ContainsFunc(d.requiredScopes, func(scope string) bool { return !slices.Contains(effective, scope) }) {
		return configstore.ErrGrantInsufficient
	}
	return nil
}

func (h *Hub) ConfigureClientAuthorization(store *configstore.Store) {
	if h.cfg.Auth.SSO != nil {
		h.identityStore = store
	}
	if h.cfg.ClientAuthorization.Enabled {
		h.grantStore = store
	}
}

func (h *Hub) RequiresClientGrant(id string) bool {
	if h.cfg.ClientAuthorization.RequireClientGrant {
		return true
	}
	if backend, ok := h.cfg.Backend(id); ok {
		return backend.RequireClientGrant
	}
	return h.currentHTTPTools().RequiresClientGrant(id)
}

func (h *Hub) filterGrantEndpoints(ids []string, g *configstore.ClientGrant) []string {
	return slices.DeleteFunc(slices.Clone(ids), func(id string) bool {
		if g != nil {
			return id != g.EndpointID
		}
		return h.RequiresClientGrant(id)
	})
}

func grantToolPolicy(d toolDefinition) string {
	if d.httpTool {
		return d.grantPolicy
	}
	tool := *d.tool
	tool.Description, tool.Title = "", ""
	value, _ := fingerprint([]any{tool, d.target, d.effect, d.requiredScopes, d.resourceRules, d.approvalRules})
	return value
}

func (h *Hub) PrepareClientGrant(g *configstore.ClientGrant, scopes []string, identities ...*configstore.EffectiveIdentity) error {
	var identity *configstore.EffectiveIdentity
	if len(identities) > 0 {
		identity = identities[0]
	}
	if identity != nil {
		p := identity.Permissions
		if !p.AllowsEndpoint(g.EndpointID) || (g.Capabilities.Prompts && !p.AllowsCapability(g.EndpointID, "prompts")) || (g.Capabilities.Resources && !p.AllowsCapability(g.EndpointID, "resources")) || (g.Capabilities.Subscriptions && !p.AllowsCapability(g.EndpointID, "subscriptions")) {
			return configstore.ErrGrantInsufficient
		}
	}
	missing, known := h.MissingScopes(g.EndpointID, scopes)
	if !known || len(missing) != 0 {
		return configstore.ErrGrantInsufficient
	}
	defs := h.backendToolDefinitions(g.EndpointID, false)
	if len(defs) == 0 {
		defs = h.httpToolDefinitions(g.EndpointID)
	}
	if identity != nil {
		defs = maps.Clone(defs)
		for name, d := range defs {
			if !identity.Permissions.AllowsTool(d.backendID, d.original, d.effect) {
				delete(defs, name)
			}
		}
	}
	if len(g.AllowedScopes) == 0 {
		g.AllowedScopes, _ = h.MissingScopes(g.EndpointID, nil)
		for _, d := range defs {
			if !g.Capabilities.Tools || (d.effect != "read" && !g.AllowWriteRequests) || (len(g.AllowedTools) > 0 && !slices.Contains(g.AllowedTools, d.original)) {
				continue
			}
			if !slices.ContainsFunc(d.requiredScopes, func(scope string) bool { return !slices.Contains(scopes, scope) }) {
				g.AllowedScopes = append(g.AllowedScopes, d.requiredScopes...)
			}
		}
		slices.Sort(g.AllowedScopes)
		g.AllowedScopes = slices.Compact(g.AllowedScopes)
	}
	for _, scope := range g.AllowedScopes {
		if !slices.Contains(scopes, scope) {
			return configstore.ErrGrantInsufficient
		}
	}
	missing, _ = h.MissingScopes(g.EndpointID, g.AllowedScopes)
	if len(missing) != 0 {
		return configstore.ErrGrantInsufficient
	}
	allowed := make(map[string]toolDefinition)
	for _, d := range defs {
		if d.effect != "read" && !g.AllowWriteRequests {
			continue
		}
		if slices.ContainsFunc(d.requiredScopes, func(scope string) bool { return !slices.Contains(g.AllowedScopes, scope) }) {
			continue
		}
		allowed[d.original] = d
	}
	if len(g.AllowedTools) == 0 && g.Capabilities.Tools {
		g.AllowedTools = sortedKeys(allowed)
	}
	g.ToolPolicies = make(map[string]string, len(g.AllowedTools))
	for _, name := range g.AllowedTools {
		d, ok := allowed[name]
		if !ok || name == "*" {
			return configstore.ErrGrantInsufficient
		}
		g.ToolPolicies[name] = grantToolPolicy(d)
	}
	if g.Capabilities.Tools && len(g.AllowedTools) == 0 {
		return configstore.ErrGrantInsufficient
	}
	if _, ok := h.cfg.Backend(g.EndpointID); !ok && (g.Capabilities.Prompts || g.Capabilities.Resources || g.Capabilities.Subscriptions) {
		return configstore.ErrGrantInsufficient
	}
	return nil
}

func (v *view) grantAllowsDefinition(d toolDefinition) bool {
	if v.identity != nil && !v.identity.Permissions.AllowsTool(d.backendID, d.original, d.effect) {
		return false
	}
	if v.grant == nil {
		return !v.hub.RequiresClientGrant(d.backendID)
	}
	g := v.grant
	return g.EndpointID == d.backendID && g.Capabilities.Tools && slices.Contains(g.AllowedTools, d.original) && (d.effect == "read" || g.AllowWriteRequests) && g.ToolPolicies[d.original] == grantToolPolicy(d)
}

func (v *view) ownsApproval(a configstore.Approval) bool {
	if v.grant == nil {
		return a.Intent.ClientGrant == nil
	}
	return a.Intent.ClientGrant != nil && *a.Intent.ClientGrant == v.grant.GrantBinding
}

func (v *view) grantMiddleware(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		if v.grant == nil {
			return next(ctx, method, req)
		}
		g := v.grant
		deny := func() (mcp.Result, error) {
			return nil, &jsonrpc.Error{Code: -32003, Message: string(configstore.ErrGrantInsufficient)}
		}
		extra := req.GetExtra()
		if extra == nil || extra.TokenInfo == nil || extra.TokenInfo.UserID != g.Subject || extra.TokenInfo.Extra["issuer"] != g.Issuer {
			return deny()
		}
		if id := backendForParams(req.GetParams()); id != "" && !strings.EqualFold(id, g.EndpointID) {
			return deny()
		}
		if strings.HasPrefix(method, "prompts/") && !g.Capabilities.Prompts {
			return deny()
		}
		if strings.HasPrefix(method, "resources/") && !g.Capabilities.Resources {
			return deny()
		}
		if (method == "subscriptions/listen" || method == "resources/subscribe") && !g.Capabilities.Subscriptions {
			return deny()
		}
		if method == "completion/complete" && !g.Capabilities.Prompts && !g.Capabilities.Resources {
			return deny()
		}
		callCtx, release, err := v.hub.grantStore.AdmitClientGrant(ctx, g.GrantBinding, g.Issuer, g.Subject)
		if err != nil {
			return nil, &jsonrpc.Error{Code: -32003, Message: err.Error()}
		}
		defer release()
		return next(callCtx, method, req)
	}
}

func (v *view) admitGrant(ctx context.Context) (context.Context, func(), error) {
	if v.grant == nil {
		return ctx, func() {}, nil
	}
	return v.hub.grantStore.AdmitClientGrant(ctx, v.grant.GrantBinding, v.grant.Issuer, v.grant.Subject)
}

func (h *Hub) CloseGrantViews() {
	h.viewsMu.Lock()
	var views []*view
	for key, v := range h.views {
		if v.grant != nil {
			views = append(views, v)
			delete(h.views, key)
		}
	}
	h.viewsMu.Unlock()
	for _, v := range views {
		v.close()
	}
}

func (h *Hub) PruneClientGrantViews(ctx context.Context) {
	if h.grantStore == nil {
		return
	}
	h.viewsMu.RLock()
	views := make(map[string]*view)
	for key, v := range h.views {
		if v.grant != nil {
			views[key] = v
		}
	}
	h.viewsMu.RUnlock()
	for key, v := range views {
		if h.grantStore.ValidateClientGrant(ctx, *v.grant) == nil {
			continue
		}
		h.viewsMu.Lock()
		if h.views[key] == v {
			delete(h.views, key)
		}
		h.viewsMu.Unlock()
		v.close()
	}
}
