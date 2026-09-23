package client

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"golang.org/x/oauth2"
)

type LoginOptions struct {
	ServerURL    string
	ClientID     string
	Profile      string
	Scopes       []string
	CallbackPort int
	Output       io.Writer
	OpenBrowser  func(string) error
	HTTPClient   *http.Client
}

func Login(ctx context.Context, store *Store, opts LoginOptions) error {
	if opts.Profile == "" {
		opts.Profile = "default"
	}
	if !profileName.MatchString(opts.Profile) {
		return errors.New("invalid profile name")
	}
	if opts.ServerURL == "" || opts.ClientID == "" {
		return errors.New("--server and --client-id are required for the first login")
	}
	if opts.CallbackPort < 0 || opts.CallbackPort > 65535 {
		return errors.New("--callback-port must be between 0 and 65535")
	}
	if opts.Output == nil {
		opts.Output = io.Discard
	}
	if opts.OpenBrowser == nil {
		opts.OpenBrowser = openBrowser
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	c := httpClient(opts.HTTPClient, 30*time.Second)
	meta, scopes, err := discover(ctx, opts.ServerURL, opts.Scopes, c)
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(opts.CallbackPort)))
	if err != nil {
		return errors.New("cannot listen on the login callback port")
	}
	defer listener.Close()
	state, verifier := rand.Text(), oauth2.GenerateVerifier()
	cfg := &oauth2.Config{
		ClientID:    opts.ClientID,
		Endpoint:    oauth2.Endpoint{AuthURL: meta.AuthorizationEndpoint, TokenURL: meta.TokenEndpoint, AuthStyle: oauth2.AuthStyleInParams},
		RedirectURL: "http://" + listener.Addr().String() + "/oauth/callback",
		Scopes:      scopes,
	}
	code, err := receiveCode(ctx, listener, meta, state, cfg.AuthCodeURL(state, oauth2.S256ChallengeOption(verifier), oauth2.SetAuthURLParam("resource", opts.ServerURL)), opts)
	if err != nil {
		return err
	}
	tokenCtx := context.WithValue(ctx, oauth2.HTTPClient, c)
	token, err := cfg.Exchange(tokenCtx, code, oauth2.VerifierOption(verifier), oauth2.SetAuthURLParam("resource", opts.ServerURL))
	if err != nil {
		return errors.New("authorization code exchange failed; check the registered public client and callback URL")
	}
	if err := prepareToken(token); err != nil {
		return err
	}
	if err := verifyHub(ctx, opts.ServerURL, token.AccessToken, opts.HTTPClient); err != nil {
		return err
	}
	if granted, ok := token.Extra("scope").(string); ok {
		scopes = strings.Fields(granted)
	}
	p := &profile{Version: 1, Session: rand.Text(), ServerURL: opts.ServerURL, Issuer: meta.Issuer, ClientID: opts.ClientID, TokenURL: meta.TokenEndpoint, Scopes: scopes, Token: token}
	if err := store.locked(ctx, opts.Profile, func() error { return store.save(opts.Profile, p) }); err != nil {
		return err
	}
	fmt.Fprintf(opts.Output, "Logged in to profile %q. Configure your MCP client to run: mcphub-cli connect --profile %s\n", opts.Profile, opts.Profile)
	if token.RefreshToken == "" {
		fmt.Fprintln(opts.Output, "No refresh token was issued; run login again when this access token expires.")
	}
	return nil
}

type authorizationResult struct {
	code string
	err  error
}

func receiveCode(ctx context.Context, listener net.Listener, meta *oauthex.AuthServerMeta, state, authURL string, opts LoginOptions) (string, error) {
	results := make(chan authorizationResult, 1)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		if r.Method != http.MethodGet || r.URL.Path != "/oauth/callback" || r.Host != listener.Addr().String() {
			http.NotFound(w, r)
			return
		}
		if len(r.URL.RawQuery) > 8192 {
			http.Error(w, "Invalid callback", http.StatusBadRequest)
			return
		}
		q, err := url.ParseQuery(r.URL.RawQuery)
		if err != nil || len(q["state"]) != 1 || q.Get("state") != state {
			http.Error(w, "Invalid login state", http.StatusBadRequest)
			return
		}
		result := authorizationResult{code: q.Get("code")}
		iss := q.Get("iss")
		switch {
		case len(q["iss"]) > 1 || (meta.AuthorizationResponseIssParameterSupported && iss == "") || (iss != "" && iss != meta.Issuer):
			result.err = errors.New("authorization callback issuer does not match")
		case q.Get("error") != "":
			result.err = errors.New("authorization was denied or failed at the identity service")
		case len(q["code"]) != 1 || result.code == "":
			result.err = errors.New("authorization callback did not contain a code")
		}
		select {
		case results <- result:
		default:
			http.Error(w, "Callback already received", http.StatusConflict)
			return
		}
		if result.err != nil {
			http.Error(w, "Authorization failed. Return to your terminal.", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintln(w, "Authorization received. Return to your terminal to confirm login. You may close this window.")
	})
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, MaxHeaderBytes: 8192}
	defer func() {
		// Deliver the callback page before closing its accepted connection.
		shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			_ = server.Close()
		}
	}()
	serveErrors := make(chan error, 1)
	go func() { serveErrors <- server.Serve(listener) }()
	fmt.Fprintf(opts.Output, "Opening your browser for profile %q.\n", opts.Profile)
	if err := opts.OpenBrowser(authURL); err != nil {
		fmt.Fprintf(opts.Output, "Open this URL in a browser on this computer:\n%s\n", authURL)
	}
	select {
	case result := <-results:
		return result.code, result.err
	case <-serveErrors:
		return "", errors.New("login callback listener stopped")
	case <-ctx.Done():
		return "", errors.New("login canceled or timed out; existing credentials were preserved")
	}
}

func openBrowser(rawURL string) error {
	command := "xdg-open"
	if runtime.GOOS == "darwin" {
		command = "open"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, command, rawURL).Run()
}

type bearerTransport struct {
	base            http.RoundTripper
	endpoint, token string
}

func (t bearerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.String() != t.endpoint {
		return nil, errors.New("refusing to send credentials to a different endpoint")
	}
	copy := req.Clone(req.Context())
	copy.Header = req.Header.Clone()
	copy.Header.Set("Authorization", "Bearer "+t.token)
	return t.base.RoundTrip(copy)
}

func verifyHub(ctx context.Context, endpoint, token string, base *http.Client) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	c := httpClient(base, 0)
	transport := c.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	c.Transport = bearerTransport{base: transport, endpoint: endpoint, token: token}
	cli := mcp.NewClient(&mcp.Implementation{Name: "mcphub-login", Version: "1"}, &mcp.ClientOptions{Capabilities: &mcp.ClientCapabilities{}, MultiRoundTrip: &mcp.MultiRoundTripOptions{Disabled: true}})
	session, err := cli.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: endpoint, HTTPClient: c, DisableStandaloneSSE: true, MaxRetries: -1}, nil)
	if err != nil {
		return errors.New("MCPHub rejected the login or is unavailable; check access-token issuer, audience and readiness")
	}
	defer session.Close()
	return nil
}
