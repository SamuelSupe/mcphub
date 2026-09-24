package authn

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
)

// LoginMetadata validates the issuer and endpoints before a browser or token
// exchange is allowed to use discovery data. Public clients cannot use secrets.
func LoginMetadata(ctx context.Context, issuer string, client *http.Client, public bool) (*oauthex.AuthServerMeta, error) {
	u, err := url.Parse(issuer)
	if err != nil || !secureEndpoint(u) || u.RawQuery != "" {
		return nil, errors.New("invalid authorization server issuer")
	}
	meta, err := auth.GetAuthServerMetadata(ctx, issuer, client)
	if err != nil || meta == nil {
		return nil, errors.New("cannot discover OAuth/OIDC metadata with PKCE support")
	}
	if meta.Issuer != issuer {
		return nil, errors.New("authorization metadata issuer does not match")
	}
	for _, raw := range []string{meta.AuthorizationEndpoint, meta.TokenEndpoint} {
		u, err := url.Parse(raw)
		if err != nil || !secureEndpoint(u) {
			return nil, errors.New("authorization endpoints must use HTTPS")
		}
	}
	if !slices.Contains(meta.CodeChallengeMethodsSupported, "S256") {
		return nil, errors.New("identity service must support PKCE S256")
	}
	if public && len(meta.TokenEndpointAuthMethodsSupported) > 0 && !slices.Contains(meta.TokenEndpointAuthMethodsSupported, "none") {
		return nil, errors.New("identity service must support public clients without a client secret")
	}
	return meta, nil
}

func secureEndpoint(u *url.URL) bool {
	return u.Scheme == "https" && u.Hostname() != "" && u.User == nil && u.Fragment == ""
}

func LoginHTTPClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = 15 * time.Second
	return &http.Client{Transport: transport, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}
