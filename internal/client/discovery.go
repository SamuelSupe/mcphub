package client

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
)

func httpsURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
		return nil, errors.New("endpoint must be an absolute HTTPS URL without user information or fragment")
	}
	return u, nil
}

func httpClient(base *http.Client, timeout time.Duration) *http.Client {
	var c http.Client
	if base != nil {
		c = *base
	} else {
		t := http.DefaultTransport.(*http.Transport).Clone()
		t.ResponseHeaderTimeout = 30 * time.Second
		c.Transport = t
	}
	c.Timeout = timeout
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &c
}

func discover(ctx context.Context, server string, requested []string, c *http.Client) (*oauthex.AuthServerMeta, []string, error) {
	u, err := httpsURL(server)
	if err != nil {
		return nil, nil, err
	}
	if u.RawQuery != "" || u.RawPath != "" || u.Path == "" || u.Path == "/" {
		return nil, nil, errors.New("--server must contain the MCP endpoint path without query or encoded characters")
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, server, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := c.Do(req)
	if err != nil {
		return nil, nil, errors.New("cannot contact MCPHub for authentication discovery")
	}
	io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		return nil, nil, errors.New("MCPHub must return an authentication challenge (HTTP 401); check its URL and readiness")
	}
	challenges, err := oauthex.ParseWWWAuthenticate(resp.Header.Values("WWW-Authenticate"))
	if err != nil {
		return nil, nil, errors.New("invalid authentication challenge")
	}
	metadataURL, challengedScopes := "", []string(nil)
	for _, challenge := range challenges {
		if challenge.Scheme == "bearer" {
			metadataURL = challenge.Params["resource_metadata"]
			challengedScopes = strings.Fields(challenge.Params["scope"])
			break
		}
	}
	if metadataURL == "" {
		meta := *u
		meta.Path = "/.well-known/oauth-protected-resource" + u.Path
		metadataURL = meta.String()
	}
	if _, err := httpsURL(metadataURL); err != nil {
		return nil, nil, err
	}
	resource, err := oauthex.GetProtectedResourceMetadata(ctx, metadataURL, server, c)
	if err != nil {
		return nil, nil, errors.New("cannot validate MCPHub protected resource metadata")
	}
	if len(resource.AuthorizationServers) != 1 {
		return nil, nil, errors.New("MCPHub must advertise exactly one authorization server")
	}
	issuer, err := httpsURL(resource.AuthorizationServers[0])
	if err != nil || issuer.RawQuery != "" {
		return nil, nil, errors.New("invalid authorization server issuer")
	}
	meta, err := auth.GetAuthServerMetadata(ctx, issuer.String(), c)
	if err != nil || meta == nil {
		return nil, nil, errors.New("cannot discover OAuth/OIDC metadata with PKCE support")
	}
	if meta.Issuer != issuer.String() {
		return nil, nil, errors.New("authorization metadata issuer does not match")
	}
	for _, raw := range []string{meta.AuthorizationEndpoint, meta.TokenEndpoint} {
		if _, err := httpsURL(raw); err != nil {
			return nil, nil, err
		}
	}
	if !slices.Contains(meta.CodeChallengeMethodsSupported, "S256") {
		return nil, nil, errors.New("identity service must support PKCE S256")
	}
	if len(meta.TokenEndpointAuthMethodsSupported) > 0 && !slices.Contains(meta.TokenEndpointAuthMethodsSupported, "none") {
		return nil, nil, errors.New("identity service must support public clients without a client secret")
	}
	scopes := slices.Clone(requested)
	if len(scopes) == 0 {
		scopes = challengedScopes
	}
	if len(scopes) == 0 {
		scopes = slices.Clone(resource.ScopesSupported)
	}
	if slices.Contains(meta.ScopesSupported, "offline_access") {
		scopes = append(scopes, "offline_access")
	}
	slices.Sort(scopes)
	scopes = slices.Compact(scopes)
	for _, scope := range scopes {
		if scope == "" || strings.ContainsAny(scope, " \t\r\n\"\\") {
			return nil, nil, errors.New("each scope must be a single OAuth scope value")
		}
	}
	return meta, scopes, nil
}
