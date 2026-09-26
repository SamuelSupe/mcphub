package client

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/oauthex"

	"github.com/SamuelSupe/mcphub/v2/internal/mcpcompat"
)

type requestMetadata struct{ version, method, name string }
type requestMetadataKey struct{}

type permissionError struct{ scopes, code string }

type rateLimitError struct{ retryAfter int }

func (e *rateLimitError) Error() string {
	if e.retryAfter > 0 {
		return fmt.Sprintf("MCPHub endpoint rate limit exceeded (HTTP 429); retry after %d seconds", e.retryAfter)
	}
	return "MCPHub endpoint rate limit exceeded (HTTP 429); retry later"
}

func (e *permissionError) Error() string {
	if e.code != "" {
		return e.code + "; run mcphub-cli client authorize for this entry; token refresh cannot grant client permissions"
	}
	if e.scopes == "" {
		return "MCPHub denied this request; check the profile's permissions"
	}
	return fmt.Sprintf("insufficient scope: %q; run mcphub-cli login for this profile with the required --scope values", e.scopes)
}

type authenticatedTransport struct {
	grant       func(context.Context) (string, error)
	base        http.RoundTripper
	credentials *credentials
}

func (t *authenticatedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if !t.credentials.allows(req.URL) {
		return nil, errors.New("refusing to forward credentials to another endpoint")
	}
	token, err := t.credentials.token(req.Context(), "")
	if err != nil {
		return nil, err
	}
	send := func(token string, retry bool) (*http.Response, error) {
		copy := req.Clone(req.Context())
		copy.Header = req.Header.Clone()
		if retry && req.Body != nil {
			if req.GetBody == nil {
				return nil, errors.New("authentication retry requires a replayable request")
			}
			body, err := req.GetBody()
			if err != nil {
				return nil, err
			}
			copy.Body = body
		}
		copy.Header.Set("Authorization", "Bearer "+token)
		if t.grant != nil {
			secret, err := t.grant(req.Context())
			if err != nil {
				return nil, err
			}
			copy.Header.Set("MCPHub-Grant", secret)
		}
		meta, _ := req.Context().Value(requestMetadataKey{}).(requestMetadata)
		if copy.Header.Get("Mcp-Protocol-Version") == "" && meta.version != "" {
			copy.Header.Set("Mcp-Protocol-Version", meta.version)
		}
		if meta.version >= "2026-07-28" {
			copy.Header.Set("Mcp-Method", meta.method)
			if meta.name != "" {
				copy.Header.Set("Mcp-Name", meta.name)
			}
		}
		if t.credentials.kind == "admin" {
			return t.base.RoundTrip(copy)
		}
		return (&mcpcompat.RoundTripper{Base: t.base}).RoundTrip(copy)
	}
	resp, err := send(token, false)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusUnauthorized {
		resp.Body.Close()
		token, err = t.credentials.token(req.Context(), token)
		if err != nil {
			return nil, err
		}
		resp, err = send(token, true)
		if err != nil {
			return nil, err
		}
	}
	if resp.StatusCode == http.StatusUnauthorized {
		resp.Body.Close()
		return nil, ErrLoginRequired
	}
	if resp.StatusCode == http.StatusForbidden {
		resp.Body.Close()
		var scopes string
		challenges, _ := oauthex.ParseWWWAuthenticate(resp.Header.Values("WWW-Authenticate"))
		for _, c := range challenges {
			if c.Scheme == "bearer" {
				scopes = strings.Join(strings.Fields(c.Params["scope"]), " ")
				break
			}
		}
		if len(scopes) > 1024 {
			scopes = ""
		}
		code := resp.Header.Get("MCPHub-Authorization-Error")
		if !strings.HasPrefix(code, "client_") || len(code) > 128 {
			code = ""
		}
		return nil, &permissionError{scopes: scopes, code: code}
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		resp.Body.Close()
		retryAfter, _ := strconv.Atoi(resp.Header.Get("Retry-After"))
		return nil, &rateLimitError{retryAfter: retryAfter}
	}
	return resp, nil
}

func credentialError(err error) bool {
	return errors.Is(err, ErrLoginRequired) || errors.Is(err, ErrProfileChanged)
}

func publicError(err error) error {
	var limited *rateLimitError
	if errors.As(err, &limited) {
		return limited
	}
	if errors.Is(err, ErrLoginRequired) {
		return ErrLoginRequired
	}
	if errors.Is(err, ErrProfileChanged) {
		return ErrProfileChanged
	}
	var permission *permissionError
	if errors.As(err, &permission) {
		return permission
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	return errors.New("MCPHub request failed; the operation was not replayed")
}
