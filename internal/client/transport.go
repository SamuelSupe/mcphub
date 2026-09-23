package client

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/oauthex"

	"github.com/SamuelSupe/mcphub/internal/mcpcompat"
)

type requestMetadata struct{ version, method, name string }
type requestMetadataKey struct{}

type permissionError struct{ scopes string }

func (e *permissionError) Error() string {
	if e.scopes == "" {
		return "MCPHub denied this request; check the profile's permissions"
	}
	return fmt.Sprintf("insufficient scope: %q; run mcphub-cli login for this profile with the required --scope values", e.scopes)
}

type authenticatedTransport struct {
	base        http.RoundTripper
	credentials *credentials
}

func (t *authenticatedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.String() != t.credentials.endpoint {
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
		return nil, &permissionError{scopes: scopes}
	}
	return resp, nil
}

func credentialError(err error) bool {
	return errors.Is(err, ErrLoginRequired) || errors.Is(err, ErrProfileChanged)
}

func publicError(err error) error {
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
	return errors.New("MCPHub request failed; the connector did not replay the operation")
}
