package app

import (
	"net/http"
	"strings"

	"github.com/SamuelSupe/mcphub/v2/internal/config"
)

func (a *App) serveAdminIdentities(w http.ResponseWriter, r *http.Request, suffix string) {
	cfg := a.currentConfig()
	if suffix == "" && r.Method == http.MethodGet {
		if cfg.Auth.SSO == nil {
			writeJSON(w, 200, map[string]any{"enabled": false, "identities": []any{}})
			return
		}
		identities, err := a.store.Identities(r.Context())
		if err != nil {
			writeStoreError(w, err)
			return
		}
		// Old providers remain in the audit database, but cannot be granted access
		// accidentally after an operator changes the configured identity source.
		current := identities[:0]
		for _, p := range identities {
			if p.Provider == cfg.Auth.SSO.Upstream.Namespace() {
				current = append(current, p)
			}
		}
		rt := a.currentRuntime()
		writeJSON(w, 200, map[string]any{"enabled": true, "provider": cfg.Auth.SSO.Upstream.Issuer, "directory_sync": cfg.Auth.SSO.DirectoryTokenEnv != "", "identities": current, "endpoints": rt.hub.AuthorizationOptions(rt.allScopes()), "scopes": rt.allScopes()})
		return
	}
	if cfg.Auth.SSO == nil {
		http.NotFound(w, r)
		return
	}
	id := strings.TrimPrefix(suffix, "/")
	if id == "" || strings.Contains(id, "/") || r.Method != http.MethodPut {
		http.NotFound(w, r)
		return
	}
	revision, ok := requireRevision(w, r)
	if !ok {
		return
	}
	var input struct {
		Enabled     bool                       `json:"enabled"`
		Permissions config.IdentityPermissions `json:"permissions"`
	}
	if !decodeAdminJSON(w, r, &input) {
		return
	}
	if err := input.Permissions.Validate(); err != nil {
		writeAPIError(w, 400, "validation_failed", err.Error(), "permissions")
		return
	}
	existing, err := a.store.Identity(r.Context(), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if existing.Provider != cfg.Auth.SSO.Upstream.Namespace() {
		http.NotFound(w, r)
		return
	}
	updated, err := a.store.UpdateIdentity(r.Context(), id, revision, input.Enabled, input.Permissions)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	a.currentRuntime().hub.PruneIdentityViews(r.Context())
	w.Header().Set("ETag", revisionETag(updated.Revision))
	writeJSON(w, 200, updated)
}
