package app

import (
	"context"
	"crypto/rand"
	"errors"
	"net/http"
	"time"

	mcpauth "github.com/modelcontextprotocol/go-sdk/auth"

	"github.com/SamuelSupe/mcphub/v2/internal/config"
	"github.com/SamuelSupe/mcphub/v2/internal/configstore"
	"github.com/SamuelSupe/mcphub/v2/internal/hub"
	"github.com/SamuelSupe/mcphub/v2/internal/sso"
)

func (a *App) configureNativeAuthorization() {
	a.sso.ConfigureNativeConsent(func(ctx context.Context, identity configstore.EffectiveIdentity, scopes []string) []config.ClientEndpointOption {
		return a.currentRuntime().hub.AuthorizationOptions(scopes, &identity)
	}, func(ctx context.Context, identity configstore.EffectiveIdentity, name string, selections []sso.NativeSelection, ttl time.Duration) ([]configstore.GrantBinding, error) {
		a.reloadMu.Lock()
		defer a.reloadMu.Unlock()
		rt := a.currentRuntime()
		grants := []configstore.ClientGrant{}
		for _, selection := range selections {
			grant := configstore.ClientGrant{GrantBinding: configstore.GrantBinding{ClientID: "ci_" + rand.Text()}, Issuer: rt.cfg.Auth.Issuer, Subject: identity.ID, Resource: rt.cfg.Server.PublicURL, ClientName: name, EndpointID: selection.Endpoint, AllowedTools: selection.Tools, Capabilities: selection.Capabilities, AllowWriteRequests: selection.Write, ResourceRules: selection.Resources}
			if err := rt.hub.PrepareClientGrant(&grant, identity.Permissions.EffectiveScopes(rt.cfg.Admin), &identity); err != nil {
				return nil, err
			}
			var err error
			grant.EndpointUID, grant.EndpointPolicy, err = a.store.ClientEndpointPolicy(ctx, grant.EndpointID)
			if err != nil {
				return nil, err
			}
			grants = append(grants, grant)
		}
		return a.store.CreateNativeConnection(ctx, identity, grants, ttl, rt.cfg.ClientAuthorization.GrantTTL())
	})
}

func (a *App) nativeGrantContext(req *http.Request) (*http.Request, error) {
	info := mcpauth.TokenInfoFromContext(req.Context())
	if sso.Identity(info) == nil {
		return req, nil
	}
	bindings, _ := info.Extra["native_grants"].([]configstore.GrantBinding)
	if bindings == nil {
		return req, nil
	}
	if req.Header.Get(configstore.GrantHeader) != "" {
		return req, configstore.ErrGrantInvalid
	}
	grants := map[string]configstore.ClientGrant{}
	issuer, _ := info.Extra["issuer"].(string)
	for _, binding := range bindings {
		grant, err := a.store.NativeGrant(req.Context(), binding, issuer, info.UserID, a.currentConfig().Server.PublicURL)
		if err != nil {
			var denied configstore.GrantError
			if errors.As(err, &denied) {
				continue
			}
			return req, err
		}
		grants[grant.EndpointID] = grant
	}
	return req.WithContext(hub.WithClientGrants(req.Context(), grants)), nil
}
