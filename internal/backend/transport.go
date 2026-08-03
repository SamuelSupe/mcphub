package backend

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"

	"github.com/SamuelSupe/mcphub/internal/config"
)

func newHTTPClient(ctx context.Context, cfg config.BackendConfig) (*http.Client, error) {
	base := http.DefaultTransport.(*http.Transport).Clone()
	tracker := newConnectionTracker(ctx)
	dialContext := base.DialContext
	if dialContext == nil {
		dialer := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
		dialContext = dialer.DialContext
	}
	base.DialContext = tracker.wrapDial(dialContext)
	if base.DialTLSContext != nil {
		base.DialTLSContext = tracker.wrapDial(base.DialTLSContext)
	}
	base.MaxIdleConns = 100
	base.MaxIdleConnsPerHost = 20
	base.IdleConnTimeout = 90 * time.Second
	base.TLSHandshakeTimeout = 10 * time.Second
	base.ResponseHeaderTimeout = min(cfg.RequestTimeout.Duration, 30*time.Second)

	headeredTransport := &headerTransport{
		base:    base,
		headers: config.HeaderMap(cfg.Headers),
	}
	client := &http.Client{
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	if cfg.OAuth == nil {
		client.Transport = &lifecycleTransport{ctx: ctx, base: headeredTransport}
		return client, nil
	}

	authClient := &http.Client{
		Transport: base,
		Timeout:   min(cfg.RequestTimeout.Duration, 30*time.Second),
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	discoveryCtx, cancel := context.WithTimeout(ctx, min(cfg.RequestTimeout.Duration, 30*time.Second))
	tokenEndpoint, err := discoverOAuthTokenEndpoint(discoveryCtx, cfg.OAuth.Issuer, authClient)
	cancel()
	if err != nil {
		return nil, fmt.Errorf("discover OAuth issuer: %w", err)
	}

	tokenConfig := clientcredentials.Config{
		ClientID:     cfg.OAuth.ClientID,
		ClientSecret: cfg.OAuth.ClientSecret,
		TokenURL:     tokenEndpoint,
		Scopes:       cfg.OAuth.Scopes,
		AuthStyle:    oauth2.AuthStyleAutoDetect,
	}
	tokenCtx := context.WithValue(ctx, oauth2.HTTPClient, authClient)
	source := redactingTokenSource{source: tokenConfig.TokenSource(tokenCtx)}
	token, err := source.Token()
	if err != nil {
		return nil, err
	}
	client.Transport = &lifecycleTransport{ctx: ctx, base: &oauth2.Transport{
		Base:   headeredTransport,
		Source: oauth2.ReuseTokenSource(token, source),
	}}
	return client, nil
}

const maximumOAuthMetadataBytes = 1 << 20

type oauthServerMetadata struct {
	Issuer        string `json:"issuer"`
	TokenEndpoint string `json:"token_endpoint"`
}

func discoverOAuthTokenEndpoint(ctx context.Context, issuer string, client *http.Client) (string, error) {
	if err := validateOAuthEndpointURL(issuer, true); err != nil {
		return "", fmt.Errorf("issuer: %w", err)
	}
	issuerURL, _ := url.Parse(issuer)
	allowInsecureTokenEndpoint := issuerURL.Scheme == "http" && isLoopbackOAuthHost(issuerURL.Hostname())
	metadataURLs, err := oauthMetadataURLs(issuer)
	if err != nil {
		return "", err
	}
	for _, metadataURL := range metadataURLs {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, metadataURL, nil)
		if err != nil {
			return "", err
		}
		req.Header.Set("Accept", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			return "", err
		}
		body, readErr := io.ReadAll(io.LimitReader(resp.Body, maximumOAuthMetadataBytes+1))
		closeErr := resp.Body.Close()
		if readErr != nil {
			return "", fmt.Errorf("read metadata: %w", readErr)
		}
		if closeErr != nil {
			return "", fmt.Errorf("close metadata response: %w", closeErr)
		}
		if resp.StatusCode >= 400 && resp.StatusCode < 500 {
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return "", fmt.Errorf("metadata endpoint returned HTTP %d", resp.StatusCode)
		}
		if len(body) > maximumOAuthMetadataBytes {
			return "", fmt.Errorf("metadata response exceeds %d bytes", maximumOAuthMetadataBytes)
		}
		var metadata oauthServerMetadata
		if err := json.Unmarshal(body, &metadata); err != nil {
			return "", fmt.Errorf("decode metadata: %w", err)
		}
		if metadata.Issuer != issuer {
			return "", fmt.Errorf("metadata issuer %q does not match %q", metadata.Issuer, issuer)
		}
		if metadata.TokenEndpoint == "" {
			return "", fmt.Errorf("OAuth issuer %q returned no token endpoint", issuer)
		}
		if err := validateOAuthEndpointURL(metadata.TokenEndpoint, allowInsecureTokenEndpoint); err != nil {
			return "", fmt.Errorf("token endpoint: %w", err)
		}
		return metadata.TokenEndpoint, nil
	}
	return "", fmt.Errorf("OAuth issuer %q exposed no authorization server metadata", issuer)
}

func oauthMetadataURLs(issuer string) ([]string, error) {
	issuerURL, err := url.Parse(issuer)
	if err != nil {
		return nil, fmt.Errorf("parse OAuth issuer: %w", err)
	}
	path := strings.Trim(issuerURL.Path, "/")
	metadataURL := *issuerURL
	metadataURL.RawPath = ""
	if path == "" {
		metadataURL.Path = "/.well-known/oauth-authorization-server"
		first := metadataURL.String()
		metadataURL.Path = "/.well-known/openid-configuration"
		return []string{first, metadataURL.String()}, nil
	}
	metadataURL.Path = "/.well-known/oauth-authorization-server/" + path
	first := metadataURL.String()
	metadataURL.Path = "/.well-known/openid-configuration/" + path
	second := metadataURL.String()
	metadataURL.Path = "/" + path + "/.well-known/openid-configuration"
	return []string{first, second, metadataURL.String()}, nil
}

func validateOAuthEndpointURL(raw string, allowLoopbackHTTP bool) error {
	u, err := url.Parse(raw)
	if err != nil || !u.IsAbs() || u.Host == "" {
		return fmt.Errorf("must be an absolute URL")
	}
	if u.User != nil || u.Fragment != "" {
		return fmt.Errorf("must not contain user information or a fragment")
	}
	if u.Scheme == "https" {
		return nil
	}
	if u.Scheme == "http" && allowLoopbackHTTP && isLoopbackOAuthHost(u.Hostname()) {
		return nil
	}
	return fmt.Errorf("must use HTTPS")
}

func isLoopbackOAuthHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

type redactingTokenSource struct {
	source oauth2.TokenSource
}

func (s redactingTokenSource) Token() (*oauth2.Token, error) {
	token, err := s.source.Token()
	if err != nil {
		return nil, errors.New("OAuth token request failed")
	}
	return token, nil
}

type connectionTracker struct {
	ctx context.Context

	mu     sync.Mutex
	closed bool
	active map[*trackedConnection]struct{}
}

type trackedConnection struct {
	net.Conn
	release   func()
	closeOnce sync.Once
	closeErr  error
}

func newConnectionTracker(ctx context.Context) *connectionTracker {
	tracker := &connectionTracker{
		ctx:    ctx,
		active: make(map[*trackedConnection]struct{}),
	}
	context.AfterFunc(ctx, tracker.close)
	return tracker
}

func (t *connectionTracker) wrapDial(next func(context.Context, string, string) (net.Conn, error)) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		conn, err := next(ctx, network, address)
		if err != nil {
			return nil, err
		}
		tracked := &trackedConnection{Conn: conn}
		tracked.release = sync.OnceFunc(func() {
			t.mu.Lock()
			delete(t.active, tracked)
			t.mu.Unlock()
		})

		t.mu.Lock()
		if t.closed {
			t.mu.Unlock()
			_ = tracked.Close()
			if err := t.ctx.Err(); err != nil {
				return nil, err
			}
			return nil, net.ErrClosed
		}
		t.active[tracked] = struct{}{}
		t.mu.Unlock()
		return tracked, nil
	}
}

func (t *connectionTracker) close() {
	t.mu.Lock()
	t.closed = true
	active := make([]*trackedConnection, 0, len(t.active))
	for conn := range t.active {
		active = append(active, conn)
	}
	t.active = nil
	t.mu.Unlock()
	for _, conn := range active {
		_ = conn.Close()
	}
}

func (c *trackedConnection) Close() error {
	c.closeOnce.Do(func() {
		c.closeErr = c.Conn.Close()
		c.release()
	})
	return c.closeErr
}

type headerTransport struct {
	base    http.RoundTripper
	headers http.Header
}

type lifecycleTransport struct {
	ctx  context.Context
	base http.RoundTripper

	watchOnce sync.Once
	mu        sync.Mutex
	responses map[*lifecycleBody]struct{}
}

func (t *lifecycleTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	t.watchOnce.Do(func() {
		context.AfterFunc(t.ctx, t.closeResponses)
	})
	ctx, cancel := context.WithCancel(req.Context())
	stop := context.AfterFunc(t.ctx, cancel)
	if t.ctx.Err() != nil {
		cancel()
	}
	cloned := req.Clone(ctx)
	response, err := t.base.RoundTrip(cloned)
	if err != nil {
		stop()
		cancel()
		return nil, err
	}
	if response.Body == nil {
		stop()
		cancel()
		return response, nil
	}
	body := &lifecycleBody{ReadCloser: response.Body}
	body.release = sync.OnceFunc(func() {
		t.forgetResponse(body)
		stop()
		cancel()
	})
	if !t.rememberResponse(body) {
		_ = body.Close()
		return nil, t.ctx.Err()
	}
	response.Body = body
	return response, nil
}

type lifecycleBody struct {
	io.ReadCloser
	release   func()
	closeOnce sync.Once
	closeErr  error
}

func (b *lifecycleBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err != nil {
		b.release()
	}
	return n, err
}

func (b *lifecycleBody) Close() error {
	b.closeOnce.Do(func() {
		b.closeErr = b.ReadCloser.Close()
		b.release()
	})
	return b.closeErr
}

func (t *lifecycleTransport) rememberResponse(body *lifecycleBody) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.ctx.Err() != nil {
		return false
	}
	if t.responses == nil {
		t.responses = make(map[*lifecycleBody]struct{})
	}
	t.responses[body] = struct{}{}
	return true
}

func (t *lifecycleTransport) forgetResponse(body *lifecycleBody) {
	t.mu.Lock()
	delete(t.responses, body)
	t.mu.Unlock()
}

func (t *lifecycleTransport) closeResponses() {
	t.mu.Lock()
	responses := make([]*lifecycleBody, 0, len(t.responses))
	for body := range t.responses {
		responses = append(responses, body)
	}
	t.responses = nil
	t.mu.Unlock()
	for _, body := range responses {
		_ = body.Close()
	}
}

func (t *headerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	cloned := req.Clone(req.Context())
	cloned.Header = req.Header.Clone()
	for name, values := range t.headers {
		cloned.Header.Del(name)
		for _, value := range values {
			cloned.Header.Add(name, value)
		}
	}
	return t.base.RoundTrip(cloned)
}
