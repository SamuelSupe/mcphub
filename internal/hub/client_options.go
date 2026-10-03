package hub

import (
	"slices"
	"strings"

	"github.com/SamuelSupe/mcphub/v2/internal/config"
	"github.com/SamuelSupe/mcphub/v2/internal/configstore"
)

// AuthorizationOptions is a consent catalog, not an MCP data-plane grant.
// Only published tools already covered by the caller's token are disclosed.
func (h *Hub) AuthorizationOptions(scopes []string, identities ...*configstore.EffectiveIdentity) []config.ClientEndpointOption {
	ids := h.currentHTTPTools().IDs()
	for _, b := range h.cfg.Backends {
		ids = append(ids, b.ID)
	}
	slices.Sort(ids)
	values := []config.ClientEndpointOption{}
	for _, id := range ids {
		g := configstore.ClientGrant{EndpointID: id, AllowWriteRequests: true, Capabilities: configstore.GrantCapabilities{Tools: true}}
		toolsAvailable := h.PrepareClientGrant(&g, scopes, identities...) == nil
		defs := h.backendToolDefinitions(id, false)
		if len(defs) == 0 {
			defs = h.httpToolDefinitions(id)
		}
		entry := config.ClientEndpointOption{ID: id, Tools: []config.ClientToolOption{}}
		endpointScopes, _ := h.MissingScopes(id, nil)
		entry.RequiredScopes = endpointScopes
		if _, ok := h.cfg.Backend(id); ok {
			entry.Prompts, entry.Resources, entry.Subscriptions = true, true, true
			if len(identities) > 0 && identities[0] != nil {
				p := identities[0].Permissions
				entry.Prompts, entry.Resources, entry.Subscriptions = p.AllowsCapability(id, "prompts"), p.AllowsCapability(id, "resources"), p.AllowsCapability(id, "subscriptions")
			}
		}
		missing, known := h.MissingScopes(id, scopes)
		if !known || len(missing) > 0 || (!toolsAvailable && !entry.Prompts && !entry.Resources && !entry.Subscriptions) {
			continue
		}
		for _, d := range defs {
			if !toolsAvailable || !slices.Contains(g.AllowedTools, d.original) {
				continue
			}
			required := slices.Concat(endpointScopes, d.requiredScopes)
			slices.Sort(required)
			entry.Tools = append(entry.Tools, config.ClientToolOption{Name: d.original, Effect: d.effect, RequiredScopes: slices.Compact(required), ResourceRules: d.resourceRules})
		}
		slices.SortFunc(entry.Tools, func(a, b config.ClientToolOption) int { return strings.Compare(a.Name, b.Name) })
		values = append(values, entry)
	}
	return values
}
