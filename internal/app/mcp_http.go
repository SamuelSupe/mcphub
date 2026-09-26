package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	mcpauth "github.com/modelcontextprotocol/go-sdk/auth"

	"github.com/SamuelSupe/mcphub/v2/internal/configstore"
	"github.com/SamuelSupe/mcphub/v2/internal/diagnostics"
	"github.com/SamuelSupe/mcphub/v2/internal/hub"
	"github.com/SamuelSupe/mcphub/v2/internal/mcpcompat"
	"github.com/SamuelSupe/mcphub/v2/internal/sso"
)

func (a *App) serveMCP(w http.ResponseWriter, req *http.Request, rt *runtime) {
	if !a.auth.Ready() {
		http.Error(w, "authorization verifier unavailable", http.StatusServiceUnavailable)
		return
	}
	handler := mcpauth.RequireBearerToken(
		a.auth.Verify,
		&mcpauth.RequireBearerTokenOptions{
			ResourceMetadataURL: rt.cfg.ResourceMetadataURL(),
			ClockSkew:           30 * time.Second,
		},
	)(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		diagnostics.Update(req.Context(), func(r *diagnostics.Record) { r.Subject = mcpauth.TokenInfoFromContext(req.Context()).UserID })
		if identity := sso.Identity(mcpauth.TokenInfoFromContext(req.Context())); identity != nil {
			ctx, endToken := context.WithDeadline(req.Context(), mcpauth.TokenInfoFromContext(req.Context()).Expiration)
			defer endToken()
			ctx, release, err := a.store.AdmitIdentity(ctx, *identity)
			if err != nil {
				http.Error(w, "user permission denied", http.StatusForbidden)
				return
			}
			defer release()
			req = req.WithContext(ctx)
		}
		if len(req.Header.Values(configstore.GrantHeader)) > 1 {
			writeGrantError(w, configstore.ErrGrantInvalid)
			return
		}
		if secret := req.Header.Get(configstore.GrantHeader); secret != "" {
			if a.store == nil || !rt.cfg.ClientAuthorization.Enabled {
				writeGrantError(w, configstore.ErrGrantInvalid)
				return
			}
			info := mcpauth.TokenInfoFromContext(req.Context())
			ctx, endToken := context.WithDeadline(req.Context(), info.Expiration)
			defer endToken()
			req = req.WithContext(ctx)
			issuer, _ := info.Extra["issuer"].(string)
			grant, err := a.store.AuthenticateClientGrant(req.Context(), secret, issuer, info.UserID, rt.cfg.Server.PublicURL)
			if err != nil {
				writeGrantError(w, err)
				return
			}
			ctx, release, err := a.store.AdmitClientGrant(req.Context(), grant.GrantBinding, issuer, info.UserID)
			if err != nil {
				writeGrantError(w, err)
				return
			}
			defer release()
			req = req.WithContext(hub.WithClientGrant(ctx, grant))
			diagnostics.Update(req.Context(), func(r *diagnostics.Record) {
				r.ClientID, r.GrantID, r.Endpoint = grant.ClientID, grant.GrantID, grant.EndpointID
			})
		}
		envelope, ok := a.authorizeMCPRequest(w, req, rt)
		if !ok {
			return
		}
		if envelope.Method != "resources/unsubscribe" {
			release, rejected := a.limits.Acquire(envelope.backendIDs)
			if rejected != nil {
				diagnostics.Outcome(req.Context(), "rate_limited", "endpoint_limit")
				w.Header().Set("Retry-After", strconv.Itoa(rejected.RetryAfter))
				w.Header().Set("Cache-Control", "no-store")
				writeJSON(w, http.StatusTooManyRequests, map[string]any{"jsonrpc": "2.0", "id": envelope.ID, "error": map[string]any{"code": -32000, "message": "Endpoint rate limit exceeded", "data": rejected}})
				return
			}
			defer release()
		}
		if envelope.Method == "subscriptions/listen" {
			serveMCPResponse(w, req, rt.mcpHandler)
			return
		}
		ctx, cancel := context.WithTimeout(req.Context(), rt.cfg.Server.RequestTimeout.Duration)
		defer cancel()
		serveMCPResponse(w, req.WithContext(ctx), rt.mcpHandler)
	}))
	handler.ServeHTTP(w, req)
}

func serveMCPResponse(w http.ResponseWriter, req *http.Request, handler http.Handler) {
	controller := http.NewResponseController(w)
	if deadline, ok := req.Context().Deadline(); ok {
		_ = controller.SetWriteDeadline(deadline)
	}
	cancellationFinished := make(chan struct{})
	stopCancellation := context.AfterFunc(req.Context(), func() {
		// Context cancellation alone cannot interrupt a Write blocked on a slow
		// client. Expiring the connection deadline makes runtime drain limits and
		// client disconnects effective for both ordinary and subscription replies.
		_ = controller.SetWriteDeadline(time.Now())
		close(cancellationFinished)
	})
	defer func() {
		if !stopCancellation() {
			<-cancellationFinished
		}
		_ = controller.SetWriteDeadline(time.Time{})
	}()
	handler.ServeHTTP(w, req)
}

func (a *App) authorizeMCPRequest(w http.ResponseWriter, req *http.Request, rt *runtime) (rpcEnvelope, bool) {
	if req.Method != http.MethodPost {
		return rpcEnvelope{}, true
	}
	controller := http.NewResponseController(w)
	_ = controller.SetReadDeadline(time.Now().Add(rt.cfg.Server.RequestTimeout.Duration))
	defer controller.SetReadDeadline(time.Time{})
	if req.ContentLength > rt.cfg.Server.MaxRequestBodyBytes {
		http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
		return rpcEnvelope{}, false
	}
	body, err := io.ReadAll(io.LimitReader(req.Body, rt.cfg.Server.MaxRequestBodyBytes+1))
	if err != nil {
		var networkError net.Error
		if errors.As(err, &networkError) && networkError.Timeout() {
			http.Error(w, "request body timeout", http.StatusRequestTimeout)
			return rpcEnvelope{}, false
		}
		http.Error(w, "read request body", http.StatusBadRequest)
		return rpcEnvelope{}, false
	}
	if int64(len(body)) > rt.cfg.Server.MaxRequestBodyBytes {
		http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
		return rpcEnvelope{}, false
	}
	_ = req.Body.Close()
	if normalized, changed := mcpcompat.NormalizeCancellation(body, req.Header.Get("Mcp-Protocol-Version")); changed {
		body = normalized
	}
	req.Body = io.NopCloser(bytes.NewReader(body))
	req.ContentLength = int64(len(body))

	var envelope rpcEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		return rpcEnvelope{}, true
	}
	envelope.backendIDs = envelope.parseBackendIDs()
	diagnostics.Update(req.Context(), func(r *diagnostics.Record) {
		switch envelope.Method {
		case "initialize", "notifications/initialized", "ping", "tools/list", "tools/call", "prompts/list", "prompts/get", "resources/list", "resources/read", "resources/templates/list", "resources/subscribe", "resources/unsubscribe", "subscriptions/listen", "completion/complete", "server/discover", "notifications/cancelled", "notifications/cancel":
			r.Method = envelope.Method
		default:
			r.Method = "other"
		}
		if len(envelope.backendIDs) == 1 {
			if _, known := rt.hub.MissingScopes(envelope.backendIDs[0], nil); known {
				r.Endpoint = envelope.backendIDs[0]
			}
		}
		if name, ok := envelope.toolName(); ok {
			if endpoint, tool, known := rt.hub.ToolTarget(name); known {
				r.Endpoint, r.Tool = endpoint, tool
			}
		}
	})
	for _, id := range envelope.backendIDs {
		if req.Header.Get(configstore.GrantHeader) == "" && rt.hub.RequiresClientGrant(id) {
			writeGrantError(w, configstore.ErrGrantRequired)
			return envelope, false
		}
	}
	token := mcpauth.TokenInfoFromContext(req.Context())
	var scopes []string
	if token != nil {
		scopes = token.Scopes
	}
	if err := rt.hub.CheckIdentityRequest(sso.Identity(token), envelope.Method, envelope.Params, envelope.backendIDs); err != nil {
		diagnostics.Outcome(req.Context(), "policy_denied", "user_permission_denied")
		http.Error(w, "user permission denied", http.StatusForbidden)
		return envelope, false
	}
	missingSet := make(map[string]struct{})
	for _, backendID := range envelope.backendIDs {
		missing, known := rt.hub.MissingScopes(backendID, scopes)
		if !known {
			continue
		}
		for _, scope := range missing {
			missingSet[scope] = struct{}{}
		}
	}
	if toolName, ok := envelope.toolName(); ok {
		missing, known := rt.hub.MissingToolScopes(toolName, scopes)
		if known {
			for _, scope := range missing {
				missingSet[scope] = struct{}{}
			}
		}
	}
	if len(missingSet) == 0 {
		if grant := hub.ClientGrantFromContext(req.Context()); grant != nil {
			for _, id := range envelope.backendIDs {
				if !strings.EqualFold(id, grant.EndpointID) {
					writeGrantError(w, configstore.ErrGrantInsufficient)
					return envelope, false
				}
			}
			if err := rt.hub.CheckClientGrantRequest(*grant, envelope.Method, envelope.Params, scopes); err != nil {
				_ = a.store.RecordClientGrantDenial(req.Context(), *grant, "client_scope_or_resource_denied")
				writeGrantError(w, err)
				return envelope, false
			}
		}
		return envelope, true
	}
	missing := make([]string, 0, len(missingSet))
	diagnostics.Outcome(req.Context(), "scope_denied", "insufficient_scope")
	for scope := range missingSet {
		missing = append(missing, scope)
	}
	slices.Sort(missing)
	w.Header().Set("WWW-Authenticate", fmt.Sprintf(
		"Bearer error=%q, resource_metadata=%q, scope=%q",
		"insufficient_scope",
		rt.cfg.ResourceMetadataURL(),
		strings.Join(missing, " "),
	))
	http.Error(w, "insufficient scope", http.StatusForbidden)
	return envelope, false
}

type rpcEnvelope struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`

	backendIDs []string
}

func (e rpcEnvelope) toolName() (string, bool) {
	if e.Method != "tools/call" {
		return "", false
	}
	var params struct {
		Name string `json:"name"`
	}
	if json.Unmarshal(e.Params, &params) != nil || params.Name == "" {
		return "", false
	}
	return params.Name, true
}

func (e rpcEnvelope) parseBackendIDs() []string {
	var values []string
	switch e.Method {
	case "tools/call", "prompts/get":
		var params struct {
			Name string `json:"name"`
		}
		if json.Unmarshal(e.Params, &params) == nil {
			if id, _, ok := strings.Cut(params.Name, "."); ok {
				values = append(values, id)
			}
		}
	case "resources/read", "resources/subscribe", "resources/unsubscribe":
		var params struct {
			URI string `json:"uri"`
		}
		if json.Unmarshal(e.Params, &params) == nil {
			if id := backendFromResourceURI(params.URI); id != "" {
				values = append(values, id)
			}
		}
	case "completion/complete":
		var params struct {
			Ref struct {
				Type string `json:"type"`
				Name string `json:"name"`
				URI  string `json:"uri"`
			} `json:"ref"`
		}
		if json.Unmarshal(e.Params, &params) == nil {
			if params.Ref.Type == "ref/prompt" {
				if id, _, ok := strings.Cut(params.Ref.Name, "."); ok {
					values = append(values, id)
				}
			} else if id := backendFromResourceURI(params.Ref.URI); id != "" {
				values = append(values, id)
			}
		}
	case "subscriptions/listen":
		var params struct {
			Notifications struct {
				Resources []string `json:"resourceSubscriptions"`
			} `json:"notifications"`
		}
		if json.Unmarshal(e.Params, &params) == nil {
			for _, resource := range params.Notifications.Resources {
				if id := backendFromResourceURI(resource); id != "" {
					values = append(values, id)
				}
			}
		}
	}
	slices.Sort(values)
	return slices.Compact(values)
}

func backendFromResourceURI(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "mcphub" {
		return ""
	}
	return u.Host
}
