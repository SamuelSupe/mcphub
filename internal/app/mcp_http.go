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
	"strings"
	"time"

	mcpauth "github.com/modelcontextprotocol/go-sdk/auth"

	"github.com/SamuelSupe/mcphub/internal/mcpcompat"
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
		method, ok := a.authorizeMCPRequest(w, req, rt)
		if !ok {
			return
		}
		if method == "subscriptions/listen" {
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

func (a *App) authorizeMCPRequest(w http.ResponseWriter, req *http.Request, rt *runtime) (string, bool) {
	if req.Method != http.MethodPost {
		return "", true
	}
	controller := http.NewResponseController(w)
	_ = controller.SetReadDeadline(time.Now().Add(rt.cfg.Server.RequestTimeout.Duration))
	defer controller.SetReadDeadline(time.Time{})
	if req.ContentLength > rt.cfg.Server.MaxRequestBodyBytes {
		http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
		return "", false
	}
	body, err := io.ReadAll(io.LimitReader(req.Body, rt.cfg.Server.MaxRequestBodyBytes+1))
	if err != nil {
		var networkError net.Error
		if errors.As(err, &networkError) && networkError.Timeout() {
			http.Error(w, "request body timeout", http.StatusRequestTimeout)
			return "", false
		}
		http.Error(w, "read request body", http.StatusBadRequest)
		return "", false
	}
	if int64(len(body)) > rt.cfg.Server.MaxRequestBodyBytes {
		http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
		return "", false
	}
	_ = req.Body.Close()
	if normalized, changed := mcpcompat.NormalizeCancellation(body, req.Header.Get("Mcp-Protocol-Version")); changed {
		body = normalized
	}
	req.Body = io.NopCloser(bytes.NewReader(body))
	req.ContentLength = int64(len(body))

	var envelope rpcEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		return "", true
	}
	backendIDs := envelope.backendIDs()
	token := mcpauth.TokenInfoFromContext(req.Context())
	var scopes []string
	if token != nil {
		scopes = token.Scopes
	}
	missingSet := make(map[string]struct{})
	for _, backendID := range backendIDs {
		missing, known := rt.hub.MissingScopes(backendID, scopes)
		if !known {
			continue
		}
		for _, scope := range missing {
			missingSet[scope] = struct{}{}
		}
	}
	if len(missingSet) == 0 {
		return envelope.Method, true
	}
	missing := make([]string, 0, len(missingSet))
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
	return envelope.Method, false
}

type rpcEnvelope struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

func (e rpcEnvelope) backendIDs() []string {
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
