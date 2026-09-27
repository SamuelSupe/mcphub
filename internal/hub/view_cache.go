package hub

import (
	"context"
	"net/http"
	"time"

	"github.com/SamuelSupe/mcphub/v2/internal/sso"
	mcpauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const maxCachedViews = 512

type requestViewKey struct{}

// StreamableHTTPHandler keeps a view leased until the HTTP request completes.
// Session checks alone cannot protect the interval before SDK session creation.
func (h *Hub) StreamableHTTPHandler(options *mcp.StreamableHTTPOptions) http.Handler {
	handler := mcp.NewStreamableHTTPHandler(func(req *http.Request) *mcp.Server {
		return req.Context().Value(requestViewKey{}).(*view).server
	}, options)
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if h.cfg.Auth.SSO != nil && sso.Identity(mcpauth.TokenInfoFromContext(req.Context())) == nil {
			http.Error(w, "identity required", http.StatusForbidden)
			return
		}
		v := h.acquireView(req)
		if v == nil {
			w.Header().Set("Retry-After", "1")
			http.Error(w, "MCP view capacity exhausted; retry later", http.StatusServiceUnavailable)
			return
		}
		defer h.releaseView(v)
		handler.ServeHTTP(w, req.WithContext(context.WithValue(req.Context(), requestViewKey{}, v)))
	})
}

func (h *Hub) releaseView(v *view) {
	h.viewsMu.Lock()
	v.activeRequests--
	v.lastUsed = time.Now()
	h.viewsMu.Unlock()
}

// Called with viewsMu held. Active HTTP requests, SDK sessions and subscriptions
// retain their view; only an idle view can make room for a new authorization.
func (h *Hub) evictIdleViewLocked() *view {
	var oldest *view
	var oldestKey string
	for key, v := range h.views {
		// Grant views own personal resource links across requests. Their existing
		// expiry/revocation cleanup must keep that history for the grant's lifetime.
		if v.grant != nil || v.activeRequests != 0 || (oldest != nil && !v.lastUsed.Before(oldest.lastUsed)) {
			continue
		}
		active := false
		for range v.server.Sessions() {
			active = true
			break
		}
		if active {
			continue
		}
		v.subscriptionMu.Lock()
		subscribed := len(v.bySession) > 0
		v.subscriptionMu.Unlock()
		if !subscribed {
			oldest, oldestKey = v, key
		}
	}
	if oldest != nil {
		delete(h.views, oldestKey)
	}
	return oldest
}
