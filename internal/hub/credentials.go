package hub

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"time"

	"github.com/SamuelSupe/mcphub/v2/internal/backend"
	"github.com/SamuelSupe/mcphub/v2/internal/config"
	"github.com/SamuelSupe/mcphub/v2/internal/configstore"
	"github.com/SamuelSupe/mcphub/v2/internal/diagnostics"
	"github.com/SamuelSupe/mcphub/v2/internal/upstream"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type personalBackend struct {
	client  *backend.Client
	binding configstore.CredentialBinding
	cancel  context.CancelFunc
}

func (h *Hub) ConfigureCredentials(ctx context.Context, m *upstream.Manager) {
	h.credentialContext, h.credentials = ctx, m
}

func credentialEndpoint(c config.BackendConfig) upstream.Endpoint {
	return upstream.Endpoint{ID: c.ID, UID: c.EndpointUID, URL: c.URL, Credentials: c.Credentials}
}

func (v *view) personalEndpoint(id string) bool {
	c, ok := v.hub.cfg.Backend(id)
	return ok && c.Credentials != nil && c.Credentials.Mode == "personal"
}

func (v *view) backendClient(ctx context.Context, id string) (*backend.Client, error) {
	shared, ok := v.hub.manager.Client(id)
	if !ok {
		return nil, &backendUnavailableError{id}
	}
	if !v.personalEndpoint(id) {
		return shared, nil
	}
	g := v.serviceGrant(id)
	if v.hub.credentials == nil || g == nil {
		return nil, upstream.ErrConnect
	}
	e := credentialEndpoint(shared.Config())
	b, err := v.hub.credentials.Binding(ctx, e, g.Issuer, g.Subject)
	if err != nil {
		return nil, err
	}
	v.credentialMu.Lock()
	defer v.credentialMu.Unlock()
	if v.credentialsClosed {
		return nil, upstream.ErrReconnect
	}
	if previous := v.personalClients[id]; previous != nil {
		if previous.binding.ID != b.ID || previous.binding.Revision != b.Revision {
			return nil, upstream.ErrReconnect
		}
		if previous.client.Ready() {
			return previous.client, nil
		}
		previous.cancel()
		previous.client.Close()
		delete(v.personalClients, id)
	}
	parent := v.hub.credentialContext
	if parent == nil {
		parent = context.Background()
	}
	sessionCtx, cancel := context.WithDeadline(parent, g.ExpiresAt)
	sessionCtx, releaseGrant, err := v.admitGrant(WithClientGrant(sessionCtx, *g))
	if err != nil {
		cancel()
		return nil, err
	}
	releaseIdentity := func() {}
	if v.identity != nil {
		sessionCtx, releaseIdentity, err = v.hub.identityStore.AdmitIdentity(sessionCtx, *v.identity)
		if err != nil {
			releaseGrant()
			cancel()
			return nil, err
		}
	}
	cleanup := func() { cancel(); releaseGrant(); releaseIdentity() }
	client := backend.NewClient(shared.Config(), v.hub.cfg.Server.RefreshInterval.Duration, v.hub.logger, func(string) { go v.reconcile() }, func(id, uri string) { v.resourceUpdated(id, uri) })
	authorize := v.hub.credentials.Authorize(e, b)
	client.Authorize = func(ctx context.Context) (http.Header, error) {
		if sessionCtx.Err() != nil {
			return nil, upstream.ErrReconnect
		}
		if err := v.hub.grantStore.ValidateClientGrant(ctx, *g); err != nil {
			return nil, upstream.ErrReconnect
		}
		return authorize(ctx)
	}
	// Connecting is cancellable by the inbound request; the successful session
	// survives that request and is owned by this user's grant.
	stop := context.AfterFunc(ctx, cancel)
	err = client.ConnectOnce(sessionCtx)
	stop()
	if err != nil {
		cleanup()
		client.Close()
		return nil, err
	}
	v.personalClients[id] = &personalBackend{client: client, binding: b, cancel: cleanup}
	go client.Run(sessionCtx)
	go func() { <-sessionCtx.Done(); client.Close(); cleanup() }()
	return client, nil
}

func (v *view) preparePersonalClients(ctx context.Context) {
	for _, id := range v.ids {
		if v.personalEndpoint(id) {
			_, _ = v.backendClient(ctx, id)
		}
	}
}

func (v *view) catalogClient(id string) (*backend.Client, bool) {
	if !v.personalEndpoint(id) {
		return v.hub.manager.Client(id)
	}
	v.credentialMu.Lock()
	defer v.credentialMu.Unlock()
	if p := v.personalClients[id]; p != nil && p.client.Ready() {
		return p.client, true
	}
	return nil, false
}

func (v *view) backendToolDefinitions(id string) map[string]toolDefinition {
	if client, ok := v.catalogClient(id); ok {
		return v.hub.toolDefinitions(id, client.Config(), client.Catalog(), false)
	}
	return v.hub.backendToolDefinitions(id, false)
}

func (v *view) closeCredentials() {
	v.credentialMu.Lock()
	v.credentialsClosed = true
	clients := v.personalClients
	v.personalClients = map[string]*personalBackend{}
	v.credentialMu.Unlock()
	for _, p := range clients {
		p.cancel()
		p.client.Close()
	}
}

func (h *Hub) CloseCredentialViews(issuer, subject, endpoint string) {
	h.viewsMu.Lock()
	var removed []*view
	for key, v := range h.views {
		if g := v.serviceGrant(endpoint); g != nil && g.Issuer == issuer && g.Subject == subject {
			delete(h.views, key)
			delete(h.grantResources, g.GrantID)
			if v.grants != nil && v.activeRequests > 0 {
				v.retired = true
				continue
			}
			removed = append(removed, v)
		}
	}
	h.viewsMu.Unlock()
	for _, v := range removed {
		v.close()
	}
}

func (v *view) credentialDiagnostics(ctx context.Context, id string) {
	if !v.personalEndpoint(id) {
		return
	}
	v.credentialMu.Lock()
	p := v.personalClients[id]
	v.credentialMu.Unlock()
	if p != nil {
		diagnostics.Update(ctx, func(r *diagnostics.Record) { r.CredentialID = p.binding.ID; r.UpstreamAccount = p.binding.Account })
	}
}

func (v *view) accountRequired(id string, err error) *mcp.CallToolResult {
	if errors.Is(err, upstream.ErrUnavailable) {
		result := toolFailure(upstream.ErrUnavailable.Error())
		result.StructuredContent = map[string]any{"status": "credential_unavailable", "endpoint": id, "action": "retry_later"}
		return result
	}
	u, _ := url.Parse(v.hub.cfg.Server.PublicURL)
	u.Path, u.RawQuery, u.Fragment = "/client-auth/", "", ""
	result := toolFailure(err.Error() + ". Open " + u.String())
	result.StructuredContent = map[string]any{"status": "account_required", "endpoint": id, "action": "connect_account", "portal_url": u.String()}
	return result
}

func (v *view) resourceRegistryFor(id string) *resourceRegistry {
	if v.personalEndpoint(id) {
		if registry := v.grantResources[id]; registry != nil {
			return registry
		}
		return &v.personalResources
	}
	return &v.hub.resourceRegistry
}

// Personal resource capabilities belong to the service grant. Replacing a
// connection view after revoking another service must retain these capabilities.
type grantResourceHistory struct {
	registry  *resourceRegistry
	expiresAt time.Time
}

func (v *view) prepareGrantResources() bool {
	if v.grants == nil {
		return true
	}
	h := v.hub
	h.viewsMu.Lock()
	defer h.viewsMu.Unlock()
	now := time.Now()
	for id, history := range h.grantResources {
		if !now.Before(history.expiresAt) {
			delete(h.grantResources, id)
		}
	}
	v.grantResources = map[string]*resourceRegistry{}
	for id, grant := range v.grants {
		if !v.personalEndpoint(id) || !grant.Capabilities.Resources {
			continue
		}
		history, ok := h.grantResources[grant.GrantID]
		if !ok {
			if len(h.grantResources) >= maxCachedViews*16 {
				return false
			}
			history = grantResourceHistory{registry: &resourceRegistry{issuedResources: map[string]*issuedResourceSet{}}, expiresAt: grant.ExpiresAt}
			h.grantResources[grant.GrantID] = history
		}
		v.grantResources[id] = history.registry
	}
	return true
}
