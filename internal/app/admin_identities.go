package app

import (
	"errors"
	"net/http"
	"strings"

	"github.com/SamuelSupe/mcphub/v2/internal/config"
	"github.com/SamuelSupe/mcphub/v2/internal/configstore"
)

func (a *App) serveAdminIdentities(w http.ResponseWriter, r *http.Request, suffix string) {
	cfg := a.currentConfig()
	if cfg.Auth.SSO != nil && suffix == "/groups" && r.Method == http.MethodPost {
		var input struct {
			Name     string `json:"name"`
			Provider string `json:"provider"`
		}
		if !decodeAdminJSON(w, r, &input) {
			return
		}
		if !a.identityProviderAllowed(input.Provider, true) {
			writeAPIError(w, 400, "validation_failed", "select a configured identity provider", "provider")
			return
		}
		group, err := a.store.CreateGroup(r.Context(), input.Provider, input.Name)
		if err != nil {
			writeAPIError(w, 400, "group_create_failed", err.Error(), "name")
			return
		}
		writeJSON(w, 201, group)
		return
	}
	if cfg.Auth.Builtin() && suffix == "" && r.Method == "POST" {
		var input struct {
			Username string `json:"username"`
			Name     string `json:"name"`
			Password string `json:"password"`
		}
		if !decodeAdminJSON(w, r, &input) {
			return
		}
		user, err := a.store.CreateLocalAccount(r.Context(), input.Username, input.Name, input.Password, false)
		if err != nil {
			writeAPIError(w, 400, "account_create_failed", err.Error(), "")
			return
		}
		writeJSON(w, 201, user)
		return
	}
	if id, action := localAccountAction(suffix); cfg.Auth.Builtin() && id != "" && action == "password" && r.Method == "POST" {
		var input struct {
			Password string `json:"password"`
		}
		if !decodeAdminJSON(w, r, &input) {
			return
		}
		if err := a.store.SetLocalPassword(r.Context(), id, input.Password, 0, false); err != nil {
			writeAPIError(w, 400, "password_reset_failed", err.Error(), "")
			return
		}
		a.currentRuntime().hub.PruneIdentityViews(r.Context())
		w.WriteHeader(204)
		return
	}
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
			if a.identityProviderAllowed(p.Provider, true) {
				current = append(current, p)
			}
		}
		rt := a.currentRuntime()
		provider := cfg.Auth.SSO.Upstream.Issuer
		if cfg.Auth.Builtin() {
			provider = "MCPHub"
		}
		providers := a.sso.Providers()
		writeJSON(w, 200, map[string]any{"providers": providers, "enabled": true, "builtin": cfg.Auth.Builtin(), "provider": provider, "directory_sync": cfg.Auth.SSO.DirectoryTokenEnv != "", "identities": current, "endpoints": rt.hub.AuthorizationOptions(rt.allScopes()), "scopes": rt.allScopes()})
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
		Groups      *[]string                  `json:"groups"`
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
	if !a.identityProviderAllowed(existing.Provider, true) {
		http.NotFound(w, r)
		return
	}
	if existing.Kind == "user" && len(input.Permissions.Roles)+len(input.Permissions.Scopes)+len(input.Permissions.Access) > 0 {
		writeAPIError(w, 400, "group_permissions_required", configstore.ErrUserPermissions.Error(), "permissions")
		return
	}
	var updated configstore.Identity
	if input.Groups != nil {
		if existing.Kind != "user" {
			writeAPIError(w, 400, "validation_failed", "only users have group memberships", "groups")
			return
		}
		updated, err = a.store.UpdateUser(r.Context(), id, revision, input.Enabled, *input.Groups)
	} else {
		updated, err = a.store.UpdateIdentity(r.Context(), id, revision, input.Enabled, input.Permissions)
	}
	if errors.Is(err, configstore.ErrGroupMembership) {
		writeAPIError(w, 400, "invalid_group_membership", err.Error(), "groups")
		return
	}
	if errors.Is(err, configstore.ErrLastAdministrator) {
		writeAPIError(w, http.StatusConflict, "last_administrator", "不能停用或移除最后一位有效管理员的组权限。请先让另一位用户加入有效的管理员组。", "permissions")
		return
	}
	if err != nil {
		writeStoreError(w, err)
		return
	}
	a.currentRuntime().hub.PruneIdentityViews(r.Context())
	w.Header().Set("ETag", revisionETag(updated.Revision))
	writeJSON(w, 200, updated)
}
