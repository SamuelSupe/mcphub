package app

import (
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/SamuelSupe/mcphub/v2/internal/config"
)

func (a *App) serveOAuthClients(w http.ResponseWriter, r *http.Request) {
	if a.sso == nil || !a.currentConfig().ClientAuthorization.Enabled {
		if r.Method == http.MethodGet {
			writeJSON(w, 200, map[string]any{"enabled": false, "revision": 0, "clients": []any{}, "built_in": []any{}})
		} else {
			http.NotFound(w, r)
		}
		return
	}
	current, err := a.store.NativeClients(r.Context())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if r.Method == "GET" {
		w.Header().Set("ETag", revisionETag(current.Revision))
		writeJSON(w, 200, map[string]any{"enabled": true, "revision": current.Revision, "clients": current.Clients, "built_in": a.currentConfig().Auth.SSO.Clients})
		return
	}
	if r.Method != "PUT" {
		http.NotFound(w, r)
		return
	}
	if r.Header.Get("If-Match") != revisionETag(current.Revision) {
		writeAPIError(w, 409, "revision_conflict", "配置已变化，请刷新后重试。", "")
		return
	}
	revision := current.Revision
	var input struct {
		Clients []config.SSOClient `json:"clients"`
	}
	if !decodeAdminJSON(w, r, &input) {
		return
	}
	if input.Clients == nil || len(input.Clients) > 64 {
		writeAPIError(w, 400, "validation_failed", "clients 必须是数组，最多注册 64 个 OAuth 客户端", "clients")
		return
	}
	ids := map[string]bool{}
	for _, client := range a.currentConfig().Auth.SSO.Clients {
		ids[client.ID] = true
	}
	for i := range input.Clients {
		client := &input.Clients[i]
		if client.ID == "" || len(client.ID) > 128 || strings.ContainsAny(client.ID+client.Name, "\r\n\t\x00") || ids[client.ID] || client.Name == "" || len(client.Name) > 128 || len(client.RedirectURIs) < 1 || len(client.RedirectURIs) > 16 {
			writeAPIError(w, 400, "validation_failed", "客户端 ID、名称或回调地址无效或重复", "clients")
			return
		}
		ids[client.ID] = true
		for _, raw := range client.RedirectURIs {
			u, err := url.Parse(raw)
			loopback := err == nil && slices.Contains([]string{"127.0.0.1", "::1"}, u.Hostname())
			if err != nil || u.Host == "" || (u.Scheme != "https" && !(u.Scheme == "http" && loopback)) || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path == "" || len(raw) > 2048 {
				writeAPIError(w, 400, "validation_failed", "回调需要 HTTPS 或 loopback HTTP 地址，不能包含查询参数或片段", "redirect_uris")
				return
			}
		}
		client.RequireConsent = true
		client.Resources = []string{a.currentConfig().Server.PublicURL}
	}
	updated, err := a.store.SaveNativeClients(r.Context(), revision, input.Clients)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	a.currentRuntime().hub.PruneClientGrantViews(r.Context())
	w.Header().Set("ETag", revisionETag(updated.Revision))
	writeJSON(w, 200, updated)
}
