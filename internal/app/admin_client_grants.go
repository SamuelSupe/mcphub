package app

import (
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/SamuelSupe/mcphub/v2/internal/configstore"
)

func (a *App) serveAdminClientGrants(w http.ResponseWriter, req *http.Request, suffix string) {
	if !a.currentConfig().ClientAuthorization.Enabled {
		if suffix == "" && req.Method == http.MethodGet {
			writeJSON(w, 200, map[string]any{"enabled": false, "grants": []any{}, "next_cursor": ""})
			return
		}
		http.NotFound(w, req)
		return
	}
	if suffix == "" && req.Method == http.MethodGet {
		params := req.URL.Query()
		limit := 25
		var err error
		if params.Has("limit") {
			limit, err = strconv.Atoi(params.Get("limit"))
		}
		query := configstore.ClientGrantQuery{Issuer: a.currentConfig().Auth.Issuer, Subject: params.Get("subject"), ClientID: params.Get("client"), Endpoint: params.Get("endpoint"), Status: params.Get("status"), Cursor: params.Get("cursor"), Limit: limit}
		if err != nil || limit < 1 || limit > 100 || len(query.Subject) > 1024 || len(query.ClientID) > 128 || len(query.Endpoint) > 32 || len(query.Cursor) > 512 || !slices.Contains([]string{"", "pending", "confirmed", "active", "expired", "revoked", "denied", "reconfirmation_required"}, query.Status) {
			writeAPIError(w, 400, "validation_failed", "客户端授权筛选条件无效", "")
			return
		}
		grants, next, err := a.store.QueryClientGrants(req.Context(), query)
		if err != nil {
			if errors.Is(err, configstore.ErrGrantCursor) {
				writeAPIError(w, 400, "validation_failed", "客户端授权筛选条件无效", "cursor")
				return
			}
			writeGrantError(w, err)
			return
		}
		writeJSON(w, 200, map[string]any{"enabled": true, "grants": grants, "next_cursor": next})
		return
	}
	parts := strings.Split(strings.Trim(suffix, "/"), "/")
	if len(parts) != 2 || parts[1] != "revoke" || req.Method != http.MethodPost {
		http.NotFound(w, req)
		return
	}
	var input struct {
		Subject string `json:"subject"`
	}
	if !decodeAdminJSON(w, req, &input) {
		return
	}
	if err := a.store.RevokeClientGrant(req.Context(), parts[0], a.currentConfig().Auth.Issuer, input.Subject); err != nil {
		writeGrantError(w, err)
		return
	}
	a.currentRuntime().hub.PruneClientGrantViews(req.Context())
	w.WriteHeader(http.StatusNoContent)
}
