package hub

import (
	"context"
	"crypto/rand"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	mcpauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/SamuelSupe/mcphub/v2/internal/backend"
	"github.com/SamuelSupe/mcphub/v2/internal/config"
	"github.com/SamuelSupe/mcphub/v2/internal/configstore"
	"github.com/SamuelSupe/mcphub/v2/internal/httptool"
	"github.com/SamuelSupe/mcphub/v2/internal/ratelimit"
	"github.com/SamuelSupe/mcphub/v2/internal/sso"
	"github.com/SamuelSupe/mcphub/v2/internal/upstream"
)

type Hub struct {
	credentials         *upstream.Manager
	credentialContext   context.Context
	identityStore       *configstore.Store
	grantStore          *configstore.Store
	approvalStore       *configstore.Store
	approvalLimits      *ratelimit.Registry
	approvalGeneration  string
	approvalsRetired    atomic.Bool
	cfg                 *config.Config
	manager             *backend.Manager
	httpToolsMu         sync.RWMutex
	httpTools           *httptool.Manager
	httpToolsGeneration uint64
	logger              *slog.Logger

	viewsMu sync.RWMutex
	views   map[string]*view

	toolDefinitionsMu    sync.Mutex
	toolDefinitionCaches map[string]*toolDefinitionCache

	resourceRegistry
}

func New(cfg *config.Config, manager *backend.Manager, logger *slog.Logger) *Hub {
	httpTools, _ := httptool.NewManager(context.Background(), nil, logger)
	return NewWithHTTPTools(cfg, manager, httpTools, logger)
}

func NewWithHTTPTools(cfg *config.Config, manager *backend.Manager, httpTools *httptool.Manager, logger *slog.Logger) *Hub {
	return &Hub{
		approvalGeneration:   rand.Text(),
		cfg:                  cfg,
		manager:              manager,
		httpTools:            httpTools,
		httpToolsGeneration:  1,
		logger:               logger,
		views:                make(map[string]*view),
		toolDefinitionCaches: make(map[string]*toolDefinitionCache),
		resourceRegistry:     resourceRegistry{issuedResources: make(map[string]*issuedResourceSet)},
	}
}

// ConfigureApprovals is called before the runtime starts serving requests.
func (h *Hub) ConfigureApprovals(store *configstore.Store, limits *ratelimit.Registry) {
	if h.cfg.Admin.Enabled && h.cfg.Admin.Remote() {
		h.approvalStore, h.approvalLimits = store, limits
	}
}

func (h *Hub) RetireApprovals() { h.approvalsRetired.Store(true) }

func (h *Hub) ServerForRequest(req *http.Request) *mcp.Server {
	token := mcpauth.TokenInfoFromContext(req.Context())
	identity := sso.Identity(token)
	if h.cfg.Auth.SSO != nil && identity == nil {
		return nil
	}
	var scopes []string
	if token != nil {
		scopes = token.Scopes
	}
	grant, _ := req.Context().Value(clientGrantKey{}).(*configstore.ClientGrant)
	if grant != nil {
		scopes = grant.EffectiveScopes(scopes)
	}
	backendIDs, backendToolScopes := h.manager.AllowedProfile(scopes)
	groupIDs, groupToolScopes := h.currentHTTPTools().AllowedProfile(scopes)
	backendIDs = h.filterGrantEndpoints(backendIDs, grant)
	groupIDs = h.filterGrantEndpoints(groupIDs, grant)
	if identity != nil {
		backendIDs = filterIdentityEndpoints(backendIDs, identity)
		groupIDs = filterIdentityEndpoints(groupIDs, identity)
		h.pruneIdentityViews(identity)
	}
	toolScopes := append(backendToolScopes, groupToolScopes...)
	slices.Sort(toolScopes)
	toolScopes = slices.Compact(toolScopes)
	// HTTP policies can change without replacing this Hub. Retain all grants in
	// the key so a shared view cannot inherit another caller's newly relevant scope.
	grants := slices.Clone(scopes)
	slices.Sort(grants)
	key := viewCacheKey(append(append(slices.Clone(backendIDs), "|groups|"), groupIDs...), slices.Compact(grants))

	if grant != nil {
		key += "|" + grant.Issuer + "|" + grant.Subject + "|" + grant.GrantID + "|" + strconv.FormatInt(grant.Revision, 10)
	}
	if identity != nil {
		key += "|identity|" + identity.ID + "|" + identity.Version
	}
	h.viewsMu.RLock()
	existing := h.views[key]
	h.viewsMu.RUnlock()
	if existing != nil {
		return existing.server
	}

	candidate := newViewWithHTTP(h, backendIDs, groupIDs, toolScopes, scopes)
	candidate.grant = grant
	candidate.identity = identity
	candidate.preparePersonalClients(req.Context())
	if identity != nil {
		for _, id := range backendIDs {
			if !identity.Permissions.AllowsCapability(id, "resources") {
				candidate.server.RemoveResourceTemplates(issuedResourceTemplate(id))
			}
		}
	}
	if grant != nil && !grant.Capabilities.Resources {
		for name := range candidate.internalTemplates {
			candidate.server.RemoveResourceTemplates(name)
		}
	}
	candidate.reconcile()
	inserted := false
	h.viewsMu.Lock()
	if existing = h.views[key]; existing == nil {
		if len(h.views) >= 512 {
			h.viewsMu.Unlock()
			candidate.close()
			return nil
		}
		h.views[key] = candidate
		existing = candidate
		inserted = true
	}
	h.viewsMu.Unlock()
	if !inserted {
		candidate.close()
	} else {
		// A catalog callback can race the first reconcile before this view is
		// visible. Reconcile once after insertion so that update cannot be lost.
		candidate.reconcile()
	}
	return existing.server
}

func (h *Hub) ReconcileBackend(id string) {
	h.viewsMu.RLock()
	views := make([]*view, 0, len(h.views))
	for _, candidate := range h.views {
		if candidate.allows(id) {
			views = append(views, candidate)
		}
	}
	h.viewsMu.RUnlock()
	for _, candidate := range views {
		candidate.reconcile()
	}
}

func (h *Hub) ReconcileToolGroup(id string) {
	h.viewsMu.RLock()
	views := make([]*view, 0, len(h.views))
	for _, candidate := range h.views {
		if candidate.allowsHTTPGroup(id) {
			views = append(views, candidate)
		}
	}
	h.viewsMu.RUnlock()
	for _, candidate := range views {
		candidate.reconcile()
	}
}

func (h *Hub) ResourceUpdated(backendID, originalURI string) {
	h.viewsMu.RLock()
	views := make([]*view, 0, len(h.views))
	for _, candidate := range h.views {
		if candidate.allows(backendID) && !candidate.personalEndpoint(backendID) {
			views = append(views, candidate)
		}
	}
	h.viewsMu.RUnlock()
	for _, candidate := range views {
		candidate.resourceUpdated(backendID, originalURI)
	}
}

func (h *Hub) Close() {
	h.viewsMu.RLock()
	views := make([]*view, 0, len(h.views))
	for _, candidate := range h.views {
		views = append(views, candidate)
	}
	h.viewsMu.RUnlock()
	for _, candidate := range views {
		candidate.close()
	}
	h.currentHTTPTools().Close()
}

func (h *Hub) HTTPToolScopes() []string { return h.currentHTTPTools().AllScopes() }

func (h *Hub) ReplaceHTTPTools(manager *httptool.Manager) {
	h.CloseGrantViews()
	h.httpToolsMu.Lock()
	previous := h.httpTools
	h.httpTools = manager
	h.httpToolsGeneration++
	h.httpToolsMu.Unlock()
	h.viewsMu.RLock()
	views := make([]*view, 0, len(h.views))
	for _, candidate := range h.views {
		views = append(views, candidate)
	}
	h.viewsMu.RUnlock()
	for _, candidate := range views {
		candidate.reconcile()
	}
	if previous != nil {
		previous.Close()
	}
}

func (h *Hub) currentHTTPTools() *httptool.Manager {
	h.httpToolsMu.RLock()
	defer h.httpToolsMu.RUnlock()
	return h.httpTools
}

func (h *Hub) currentHTTPToolsSnapshot() (*httptool.Manager, uint64) {
	h.httpToolsMu.RLock()
	defer h.httpToolsMu.RUnlock()
	return h.httpTools, h.httpToolsGeneration
}

func (h *Hub) MissingScopes(backendID string, scopes []string) ([]string, bool) {
	if missing, known := h.manager.MissingScopes(backendID, scopes); known {
		return missing, true
	}
	return h.currentHTTPTools().MissingScopes(backendID, scopes)
}

func (h *Hub) HTTPToolRateLimits() map[string]ratelimit.Config {
	return h.currentHTTPTools().RateLimits()
}

func viewCacheKey(ids, toolScopes []string) string {
	var key strings.Builder
	appendValues := func(values []string) {
		for _, value := range values {
			key.WriteString(strconv.Itoa(len(value)))
			key.WriteByte(':')
			key.WriteString(value)
		}
	}
	appendValues(ids)
	key.WriteByte('|')
	appendValues(toolScopes)
	return key.String()
}
