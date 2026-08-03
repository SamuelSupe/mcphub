package hub

import (
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"

	mcpauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/SamuelSupe/mcphub/internal/backend"
	"github.com/SamuelSupe/mcphub/internal/config"
)

type Hub struct {
	cfg     *config.Config
	manager *backend.Manager
	logger  *slog.Logger

	viewsMu sync.RWMutex
	views   map[string]*view

	toolDefinitionsMu    sync.Mutex
	toolDefinitionCaches map[string]*toolDefinitionCache

	issuedMu        sync.RWMutex
	issuedResources map[string]*issuedResourceSet
}

func New(cfg *config.Config, manager *backend.Manager, logger *slog.Logger) *Hub {
	return &Hub{
		cfg:                  cfg,
		manager:              manager,
		logger:               logger,
		views:                make(map[string]*view),
		toolDefinitionCaches: make(map[string]*toolDefinitionCache),
		issuedResources:      make(map[string]*issuedResourceSet),
	}
}

func (h *Hub) ServerForRequest(req *http.Request) *mcp.Server {
	token := mcpauth.TokenInfoFromContext(req.Context())
	var scopes []string
	if token != nil {
		scopes = token.Scopes
	}
	ids, toolScopes := h.manager.AllowedProfile(scopes)
	key := viewCacheKey(ids, toolScopes)

	h.viewsMu.RLock()
	existing := h.views[key]
	h.viewsMu.RUnlock()
	if existing != nil {
		return existing.server
	}

	candidate := newView(h, ids, toolScopes)
	candidate.reconcile()
	inserted := false
	h.viewsMu.Lock()
	if existing = h.views[key]; existing == nil {
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

func (h *Hub) ResourceUpdated(backendID, originalURI string) {
	h.viewsMu.RLock()
	views := make([]*view, 0, len(h.views))
	for _, candidate := range h.views {
		if candidate.allows(backendID) {
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
}

func (h *Hub) MissingScopes(backendID string, scopes []string) ([]string, bool) {
	return h.manager.MissingScopes(backendID, scopes)
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
