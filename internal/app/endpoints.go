package app

import (
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"strings"

	mcpauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
)

func validRequestID(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, char := range value {
		if char < 0x21 || char > 0x7e {
			return false
		}
	}
	return true
}

func (a *App) serveReady(w http.ResponseWriter, rt *runtime) {
	ready, total, requiredReady, requiredTotal := rt.manager.Status()
	status := http.StatusOK
	state := "ready"
	if !a.auth.Ready() || !rt.ready() {
		status = http.StatusServiceUnavailable
		state = "not_ready"
	}
	writeJSON(w, status, map[string]any{
		"status":              state,
		"backends_ready":      ready,
		"backends_total":      total,
		"required_ready":      requiredReady,
		"required_total":      requiredTotal,
		"auth_verifier_ready": a.auth.Ready(),
	})
}

func (a *App) serveResourceMetadata(w http.ResponseWriter, req *http.Request, rt *runtime) {
	handler := mcpauth.ProtectedResourceMetadataHandler(&oauthex.ProtectedResourceMetadata{
		Resource:               rt.cfg.Server.PublicURL,
		AuthorizationServers:   []string{rt.cfg.Auth.Issuer},
		ScopesSupported:        rt.allScopes(),
		BearerMethodsSupported: []string{"header"},
		ResourceName:           "MCPHub",
	})
	handler.ServeHTTP(w, req)
}

func (a *App) allowOrigin(w http.ResponseWriter, req *http.Request, rt *runtime) bool {
	origin := req.Header.Get("Origin")
	if origin != "" {
		allowed := originMatchesPublicURL(origin, rt.cfg.Server.PublicURL) || slices.Contains(rt.cfg.Server.AllowedOrigins, origin)
		if !allowed {
			http.Error(w, "cross-origin request denied", http.StatusForbidden)
			return false
		}
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Add("Vary", "Origin")
		w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
		requestedHeaders := req.Header.Get("Access-Control-Request-Headers")
		if !corsHeadersAllowed(requestedHeaders) {
			http.Error(w, "CORS request header denied", http.StatusForbidden)
			return false
		}
		if req.Method == http.MethodOptions {
			if requestedMethod := req.Header.Get("Access-Control-Request-Method"); requestedMethod != http.MethodPost {
				http.Error(w, "CORS request method denied", http.StatusForbidden)
				return false
			}
		}
		if requestedHeaders == "" {
			requestedHeaders = strings.Join(corsAllowedHeaders, ", ")
		}
		w.Header().Set("Access-Control-Allow-Headers", requestedHeaders)
		w.Header().Set("Access-Control-Expose-Headers", "Mcp-Session-Id, WWW-Authenticate, X-Request-Id")
		if req.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return false
		}
	}
	if err := rt.origins.Check(req); err != nil {
		http.Error(w, "cross-origin request denied", http.StatusForbidden)
		return false
	}
	return true
}

var corsAllowedHeaders = []string{
	"Authorization",
	"Content-Type",
	"Mcp-Method",
	"Mcp-Name",
	"Mcp-Protocol-Version",
	"Mcp-Session-Id",
	"Traceparent",
	"Tracestate",
	"X-Request-Id",
}

func corsHeadersAllowed(raw string) bool {
	if raw == "" {
		return true
	}
	allowed := make(map[string]struct{}, len(corsAllowedHeaders))
	for _, name := range corsAllowedHeaders {
		allowed[http.CanonicalHeaderKey(name)] = struct{}{}
	}
	for value := range strings.SplitSeq(raw, ",") {
		name := http.CanonicalHeaderKey(strings.TrimSpace(value))
		if name == "" {
			return false
		}
		if _, ok := allowed[name]; !ok {
			if !strings.HasPrefix(name, "Mcp-Param-") || len(name) == len("Mcp-Param-") {
				return false
			}
		}
	}
	return true
}

func originMatchesPublicURL(origin, publicURL string) bool {
	u, err := url.Parse(publicURL)
	if err != nil {
		return false
	}
	return origin == u.Scheme+"://"+u.Host
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
