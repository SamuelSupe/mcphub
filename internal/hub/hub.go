package hub

import (
	"context"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"

	mcpauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/SamuelSupe/mcphub/internal/backend"
	"github.com/SamuelSupe/mcphub/internal/config"
	"github.com/SamuelSupe/mcphub/internal/httptool"
)

type Hub struct {
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

	issuedMu        sync.RWMutex
	issuedResources map[string]*issuedResourceSet
}

func New(cfg *config.Config, manager *backend.Manager, logger *slog.Logger) *Hub {
	httpTools, _ := httptool.NewManager(context.Background(), nil, logger)
	return NewWithHTTPTools(cfg, manager, httpTools, logger)
}

func NewWithHTTPTools(cfg *config.Config, manager *backend.Manager, httpTools *httptool.Manager, logger *slog.Logger) *Hub {
	return &Hub{
		cfg:                  cfg,
		manager:              manager,
		httpTools:            httpTools,
		httpToolsGeneration:  1,
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
	backendIDs, backendToolScopes := h.manager.AllowedProfile(scopes)
	groupIDs, groupToolScopes := h.currentHTTPTools().AllowedProfile(scopes)
	toolScopes := append(backendToolScopes, groupToolScopes...)
	slices.Sort(toolScopes)
	toolScopes = slices.Compact(toolScopes)
	key := viewCacheKey(append(append(slices.Clone(backendIDs), "|groups|"), groupIDs...), toolScopes)

	h.viewsMu.RLock()
	existing := h.views[key]
	h.viewsMu.RUnlock()
	if existing != nil {
		return existing.server
	}

	candidate := newViewWithHTTP(h, backendIDs, groupIDs, toolScopes, scopes)
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
	h.currentHTTPTools().Close()
}

func (h *Hub) HTTPToolScopes() []string { return h.currentHTTPTools().AllScopes() }

func (h *Hub) ReplaceHTTPTools(manager *httptool.Manager) {
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
