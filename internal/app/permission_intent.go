package app

import (
	"fmt"
	"slices"

	"github.com/SamuelSupe/mcphub/v2/internal/config"
)

// Compile a saved scope snapshot. Catalog changes never broaden it silently.
func (a *App) derivePermissionScopes(p *config.IdentityPermissions) error {
	rt := a.currentRuntime()
	endpoints := rt.hub.AuthorizationOptions(rt.allScopes())
	scopes := []string{}
	for _, access := range p.Access {
		index := slices.IndexFunc(endpoints, func(e config.ClientEndpointOption) bool { return e.ID == access.EndpointID })
		if index < 0 {
			return fmt.Errorf("service %s is unavailable; verify and publish it first", access.EndpointID)
		}
		endpoint := endpoints[index]
		for _, name := range access.Tools {
			index := slices.IndexFunc(endpoint.Tools, func(t config.ClientToolOption) bool { return t.Name == name })
			if index < 0 {
				return fmt.Errorf("tool %s/%s is not published", access.EndpointID, name)
			}
			scopes = append(scopes, endpoint.Tools[index].RequiredScopes...)
		}
		if access.Prompts || access.Resources || access.Subscriptions {
			scopes = append(scopes, endpoint.RequiredScopes...)
		}
	}
	slices.Sort(scopes)
	p.Scopes = slices.Compact(scopes)
	return p.Validate()
}
