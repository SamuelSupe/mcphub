package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
)

type AdminRequestOptions struct {
	Method, Path, IfMatch string
	Body                  []byte
	Output, Diagnostics   io.Writer
	HTTPClient            *http.Client
}

func (c *credentials) allows(u *url.URL) bool {
	if c.kind != "admin" {
		return u.String() == c.endpoint
	}
	base, err := url.Parse(c.endpoint)
	return err == nil && base.Scheme == "https" && base.Path == "" && base.RawQuery == "" && u.Scheme == base.Scheme && u.Host == base.Host && u.User == nil && u.Fragment == "" && u.RawPath == "" && strings.HasPrefix(u.Path, "/api/v1/") && path.Clean(u.Path) == u.Path
}

func verifyAdmin(ctx context.Context, endpoint, token string, base *http.Client) error {
	c := httpClient(base, 30*time.Second)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"/api/v1/me", nil)
	if err != nil {
		return errors.New("invalid administration URL")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := c.Do(req)
	if err != nil {
		return errors.New("cannot verify administrator login")
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusForbidden {
		return errors.New("administrator permission is required; ask your identity-service administrator to grant the configured admin scope")
	}
	if resp.StatusCode != http.StatusOK {
		return errors.New("administrator login was rejected; check issuer, admin audience and readiness")
	}
	return nil
}

// AdminRequest sends one management operation. Only a 401 can cause one retry;
// network failures never replay writes. ETags are emitted separately to stderr.
func AdminRequest(ctx context.Context, store *Store, name string, opts AdminRequestOptions) error {
	if name == "" {
		name = "default"
	}
	switch opts.Method {
	case http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete:
	default:
		return errors.New("admin method must be get, post, put or delete")
	}
	u, err := url.Parse(opts.Path)
	if err != nil || u.IsAbs() || u.Host != "" || !strings.HasPrefix(u.Path, "/") || u.RawPath != "" || u.Fragment != "" || path.Clean(u.Path) != u.Path {
		return errors.New("admin path must be a relative API path, for example /backends")
	}
	if len(opts.Body) > 6<<20 || (len(opts.Body) > 0 && !json.Valid(opts.Body)) {
		return errors.New("admin request body must be a JSON document of at most 6 MiB")
	}
	if (opts.Method == http.MethodGet || opts.Method == http.MethodDelete) && len(opts.Body) > 0 {
		return errors.New("get and delete do not accept a request body")
	}
	bound, err := bindCredentials(ctx, store, name, opts.HTTPClient)
	if err != nil {
		return err
	}
	if bound.kind != "admin" {
		return errors.New("use a separate administrator profile: mcphub-cli login --admin --server https://admin.example.com --client-id CLIENT --profile PROFILE")
	}
	c := httpClient(opts.HTTPClient, 2*time.Minute)
	base := c.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	c.Transport = &authenticatedTransport{base: base, credentials: bound}
	req, err := http.NewRequestWithContext(ctx, opts.Method, bound.endpoint+"/api/v1"+u.String(), bytes.NewReader(opts.Body))
	if err != nil {
		return errors.New("invalid administration request")
	}
	req.Header.Set("Accept", "application/json")
	if len(opts.Body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	if opts.IfMatch != "" {
		req.Header.Set("If-Match", opts.IfMatch)
	}
	resp, err := c.Do(req)
	if err != nil {
		return publicError(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, (8<<20)+1))
	if err != nil || len(data) > 8<<20 {
		return errors.New("cannot read management response; the operation was not replayed")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var body struct {
			Error struct{ Code, Message string }
		}
		_ = json.Unmarshal(data, &body)
		if body.Error.Code != "" {
			return fmt.Errorf("management API HTTP %d (%s): %s", resp.StatusCode, body.Error.Code, body.Error.Message)
		}
		return fmt.Errorf("management API returned HTTP %d", resp.StatusCode)
	}
	if etag := resp.Header.Get("ETag"); etag != "" && opts.Diagnostics != nil {
		fmt.Fprintf(opts.Diagnostics, "ETag: %s\n", etag)
	}
	if opts.Output != nil && len(data) > 0 {
		_, err = opts.Output.Write(data)
	}
	return err
}
