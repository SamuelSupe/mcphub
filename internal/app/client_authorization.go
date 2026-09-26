package app

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/SamuelSupe/mcphub/v2/internal/config"
	"github.com/SamuelSupe/mcphub/v2/internal/configstore"
	"github.com/SamuelSupe/mcphub/v2/internal/sso"
	mcpauth "github.com/modelcontextprotocol/go-sdk/auth"
)

type clientAuthorizationInput struct {
	SessionID     string                        `json:"broker_session_id"`
	SessionProof  string                        `json:"broker_session_proof"`
	ClientID      string                        `json:"client_instance_id"`
	ClientName    string                        `json:"client_name"`
	EndpointID    string                        `json:"endpoint_id"`
	Scopes        []string                      `json:"allowed_scopes"`
	Tools         []string                      `json:"allowed_tools"`
	ResourceRules []config.ResourceRule         `json:"resource_rules"`
	AllowWrite    bool                          `json:"allow_write_requests"`
	Capabilities  configstore.GrantCapabilities `json:"capabilities"`
	TTLSeconds    int64                         `json:"ttl_seconds"`
}

func writeGrantError(w http.ResponseWriter, err error) {
	var denied configstore.GrantError
	if errors.As(err, &denied) {
		w.Header().Set("MCPHub-Authorization-Error", string(denied))
		writeAPIError(w, http.StatusForbidden, string(denied), "Client authorization is unavailable or insufficient; explicitly authorize this client again.", "")
		return
	}
	writeAPIError(w, http.StatusServiceUnavailable, "client_authorization_unavailable", "Client authorization could not be verified; no operation was admitted.", "")
}

func (a *App) serveClientAuthorization(w http.ResponseWriter, req *http.Request, rt *runtime) {
	setAdminSecurityHeaders(w)
	w.Header().Set("Cache-Control", "no-store")
	if req.Host != strings.TrimPrefix(a.userAuth.cfg.PublicURL, "https://") {
		http.Error(w, "Invalid host", http.StatusForbidden)
		return
	}
	if strings.HasPrefix(req.URL.Path, "/client-auth/") {
		path := strings.TrimPrefix(req.URL.Path, "/client-auth")
		if strings.HasPrefix(path, "/auth/") {
			copy := req.Clone(req.Context())
			copy.URL.Path = path
			if path == "/auth/session" && req.Method == http.MethodGet {
				a.userAuth.serveSession(w, req)
				return
			}
			if a.userAuth.servePublic(w, copy) {
				return
			}
		}
		if !strings.HasPrefix(path, "/api/") {
			a.serveClientPortal(w, req)
			return
		}
		if req.Header.Get("Authorization") != "" {
			writeGrantError(w, configstore.ErrGrantInvalid)
			return
		}
		info, session, ok := a.userAuth.authorize(w, req)
		if !ok {
			return
		}
		if session == nil {
			writeGrantError(w, configstore.ErrGrantInvalid)
			return
		}
		a.clientAuthorizationRoute(w, req, rt, info, true, strings.TrimPrefix(path, "/api"))
		return
	}
	if !a.auth.Ready() {
		writeGrantError(w, errors.New("identity unavailable"))
		return
	}
	handler := mcpauth.RequireBearerToken(a.auth.Verify, &mcpauth.RequireBearerTokenOptions{ResourceMetadataURL: rt.cfg.ResourceMetadataURL(), ClockSkew: 30 * time.Second})(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		a.clientAuthorizationRoute(w, req, rt, mcpauth.TokenInfoFromContext(req.Context()), false, strings.TrimPrefix(req.URL.Path, "/api/v1"))
	}))
	handler.ServeHTTP(w, req)
}

func (a *App) clientAuthorizationRoute(w http.ResponseWriter, req *http.Request, rt *runtime, info *mcpauth.TokenInfo, browser bool, path string) {
	issuer, _ := info.Extra["issuer"].(string)
	if info.UserID == "" || issuer != rt.cfg.Auth.Issuer {
		writeGrantError(w, configstore.ErrGrantInvalid)
		return
	}
	if path == "/client-authorization" && req.Method == http.MethodGet && !browser {
		origin := a.userAuth.cfg.PublicURL
		writeJSON(w, http.StatusOK, map[string]any{"version": 1, "resource": rt.cfg.Server.PublicURL, "requests_url": origin + "/api/v1/client-authorization-requests", "grants_url": origin + "/api/v1/client-grants", "sessions_url": origin + "/api/v1/broker-sessions", "portal_url": origin + "/client-auth/", "options_url": origin + "/api/v1/client-authorization-options", "max_grant_ttl_seconds": int64(rt.cfg.ClientAuthorization.GrantTTL() / time.Second)})
		return
	}
	if path == "/client-authorization-options" && req.Method == http.MethodGet && !browser {
		writeJSON(w, 200, map[string]any{"endpoints": rt.hub.AuthorizationOptions(info.Scopes, sso.Identity(info))})
		return
	}
	if path == "/client-grants" && req.Method == http.MethodGet && browser {
		grants, err := a.store.ListClientGrants(req.Context(), issuer, info.UserID)
		if err != nil {
			writeGrantError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"grants": grants})
		return
	}
	if path == "/client-grants/current" && req.Method == http.MethodGet && !browser {
		g, err := a.store.AuthenticateClientGrant(req.Context(), req.Header.Get(configstore.GrantHeader), issuer, info.UserID, rt.cfg.Server.PublicURL)
		if err != nil {
			writeGrantError(w, err)
			return
		}
		value := struct {
			Grant           configstore.ClientGrant `json:"grant"`
			EffectiveScopes []string                `json:"effective_scopes"`
		}{g, g.EffectiveScopes(info.Scopes)}
		data, _ := json.Marshal(value)
		etag := `"` + configstore.SecretHash(string(data)) + `"`
		w.Header().Set("ETag", etag)
		if req.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		writeJSON(w, http.StatusOK, value)
		return
	}
	if path == "/client-authorization-requests" && req.Method == http.MethodPost && !browser {
		var input clientAuthorizationInput
		if !decodeAdminJSON(w, req, &input) {
			return
		}
		g := configstore.ClientGrant{GrantBinding: configstore.GrantBinding{SessionID: input.SessionID, ClientID: input.ClientID}, Issuer: issuer, Subject: info.UserID, Resource: rt.cfg.Server.PublicURL, ClientName: input.ClientName, EndpointID: input.EndpointID, AllowedScopes: input.Scopes, AllowedTools: input.Tools, ResourceRules: input.ResourceRules, AllowWriteRequests: input.AllowWrite, Capabilities: input.Capabilities}
		if g.Capabilities == (configstore.GrantCapabilities{}) {
			g.Capabilities.Tools = true
		}
		a.reloadMu.Lock()
		defer a.reloadMu.Unlock()
		if a.currentRuntime() != rt {
			writeGrantError(w, configstore.ErrGrantReconfirmation)
			return
		}
		if err := rt.hub.PrepareClientGrant(&g, info.Scopes, sso.Identity(info)); err != nil {
			writeGrantError(w, err)
			return
		}
		uid, policy, err := a.store.ClientEndpointPolicy(req.Context(), g.EndpointID)
		if err != nil {
			writeGrantError(w, err)
			return
		}
		g.EndpointUID, g.EndpointPolicy = uid, policy
		ttl := time.Duration(input.TTLSeconds) * time.Second
		maximum := rt.cfg.ClientAuthorization.GrantTTL()
		if input.TTLSeconds == 0 {
			ttl = maximum
		}
		if input.TTLSeconds < 0 || input.TTLSeconds > int64(maximum/time.Second) {
			writeGrantError(w, configstore.ErrGrantInsufficient)
			return
		}
		g, exchange, proof, err := a.store.CreateClientGrant(req.Context(), g, input.SessionProof, ttl, maximum)
		if err != nil {
			writeGrantError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"request": g, "confirmation_url": a.userAuth.cfg.PublicURL + "/client-auth/?request=" + g.GrantID, "exchange_credential": exchange, "broker_session_proof": proof})
		return
	}
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) < 2 || len(parts) > 3 || len(parts[1]) > 128 {
		http.NotFound(w, req)
		return
	}
	id := parts[1]
	if parts[0] == "client-authorization-requests" {
		g, err := a.store.GetClientGrant(req.Context(), id, issuer, info.UserID)
		if err != nil {
			writeGrantError(w, err)
			return
		}
		if len(parts) == 2 && req.Method == http.MethodGet {
			writeJSON(w, http.StatusOK, g)
			return
		}
		if len(parts) == 3 && req.Method == http.MethodPost {
			switch parts[2] {
			case "confirm", "deny":
				if !browser {
					writeGrantError(w, configstore.ErrGrantInvalid)
					return
				}
				if parts[2] == "confirm" {
					if slices.ContainsFunc(g.AllowedScopes, func(scope string) bool { return !slices.Contains(info.Scopes, scope) }) {
						writeGrantError(w, configstore.ErrGrantInsufficient)
						return
					}
				}
				a.reloadMu.Lock()
				defer a.reloadMu.Unlock()
				if err := a.store.DecideClientGrant(req.Context(), id, issuer, info.UserID, parts[2] == "confirm"); err != nil {
					writeGrantError(w, err)
					return
				}
				w.WriteHeader(http.StatusNoContent)
				return
			case "exchange":
				if browser {
					writeGrantError(w, configstore.ErrGrantInvalid)
					return
				}
				var input struct {
					Credential string `json:"exchange_credential"`
				}
				if !decodeAdminJSON(w, req, &input) {
					return
				}
				if slices.ContainsFunc(g.AllowedScopes, func(scope string) bool { return !slices.Contains(info.Scopes, scope) }) {
					writeGrantError(w, configstore.ErrGrantInsufficient)
					return
				}
				a.reloadMu.Lock()
				defer a.reloadMu.Unlock()
				g, credential, err := a.store.ExchangeClientGrant(req.Context(), id, issuer, info.UserID, input.Credential)
				if err != nil {
					writeGrantError(w, err)
					return
				}
				rt.hub.PruneClientGrantViews(req.Context())
				writeJSON(w, http.StatusOK, map[string]any{"grant": g, "credential": credential})
				return
			}
		}
	}
	if len(parts) == 3 && parts[2] == "revoke" && req.Method == http.MethodPost {
		switch parts[0] {
		case "client-grants":
			if !browser {
				g, _ := a.store.AuthenticateClientGrant(req.Context(), req.Header.Get(configstore.GrantHeader), issuer, info.UserID, rt.cfg.Server.PublicURL)
				if g.GrantID != id {
					writeGrantError(w, configstore.ErrGrantInvalid)
					return
				}
			}
			if err := a.store.RevokeClientGrant(req.Context(), id, issuer, info.UserID); err != nil {
				writeGrantError(w, err)
				return
			}
		case "broker-sessions":
			var input struct {
				Proof string `json:"broker_session_proof"`
			}
			if !browser && !decodeAdminJSON(w, req, &input) {
				return
			}
			if err := a.store.RevokeBrokerSession(req.Context(), id, issuer, info.UserID, input.Proof, browser); err != nil {
				writeGrantError(w, err)
				return
			}
		default:
			http.NotFound(w, req)
			return
		}
		rt.hub.PruneClientGrantViews(req.Context())
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.NotFound(w, req)
}
