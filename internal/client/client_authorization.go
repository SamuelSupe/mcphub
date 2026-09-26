package client

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/SamuelSupe/mcphub/v2/internal/config"
	"github.com/SamuelSupe/mcphub/v2/internal/configstore"
)

type ClientOptions struct {
	ID            string                        `json:"client_instance_id"`
	Name          string                        `json:"client_name"`
	Endpoint      string                        `json:"endpoint_id"`
	Scopes        []string                      `json:"allowed_scopes"`
	Tools         []string                      `json:"allowed_tools"`
	ResourceRules []config.ResourceRule         `json:"resource_rules"`
	AllowWrite    bool                          `json:"allow_write_requests"`
	Capabilities  configstore.GrantCapabilities `json:"capabilities"`
	TTLSeconds    int64                         `json:"ttl_seconds"`
	Changed       []string                      `json:"-"`
	HTTPClient    *http.Client                  `json:"-"`
	OpenBrowser   func(string) error            `json:"-"`
	Output        io.Writer                     `json:"-"`
}

type localClient struct {
	Options ClientOptions `json:"options"`
	Profile string        `json:"profile"`
	Secret  string        `json:"ipc_credential"`
}

type brokerCredentials struct {
	SessionID string                       `json:"session_id"`
	Proof     string                       `json:"session_proof"`
	Clients   map[string]clientCredentials `json:"clients"`
}

type clientCredentials struct {
	IPCHash    string                  `json:"ipc_hash"`
	Credential string                  `json:"credential"`
	Grant      configstore.ClientGrant `json:"grant"`
}

type authorizationDiscovery struct {
	OptionsURL  string `json:"options_url"`
	MaxGrantTTL int64  `json:"max_grant_ttl_seconds"`
	Version     int    `json:"version"`
	Resource    string `json:"resource"`
	RequestsURL string `json:"requests_url"`
	GrantsURL   string `json:"grants_url"`
	SessionsURL string `json:"sessions_url"`
	PortalURL   string `json:"portal_url"`
}

type authorizationClient struct {
	credentials *credentials
	client      *http.Client
	discovery   authorizationDiscovery
}

func newAuthorizationClient(ctx context.Context, store *Store, name string, base *http.Client) (*authorizationClient, error) {
	c, err := bindCredentials(ctx, store, name, base)
	if err != nil {
		return nil, err
	}
	if c.kind == "admin" {
		return nil, errors.New("client authorization requires an MCP profile, not an administrator profile")
	}
	c.control = true
	httpClient := httpClient(base, 30*time.Second)
	transport := httpClient.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	httpClient.Transport = &authenticatedTransport{base: transport, credentials: c}
	a := &authorizationClient{credentials: c, client: httpClient}
	u, _ := url.Parse(c.endpoint)
	u.Path, u.RawPath, u.RawQuery = "/api/v1/client-authorization", "", ""
	if err := a.request(ctx, http.MethodGet, u.String(), "", nil, &a.discovery); err != nil {
		return nil, err
	}
	if a.discovery.Version != 1 || a.discovery.Resource != c.endpoint {
		return nil, errors.New("MCPHub returned incompatible client authorization metadata")
	}
	addresses := []string{a.discovery.RequestsURL, a.discovery.GrantsURL, a.discovery.SessionsURL, a.discovery.PortalURL}
	if a.discovery.OptionsURL != "" {
		addresses = append(addresses, a.discovery.OptionsURL)
	}
	for _, raw := range addresses {
		target, err := httpsURL(raw)
		if err != nil || target.Host != u.Host || target.RawQuery != "" {
			return nil, errors.New("MCPHub returned an untrusted authorization address")
		}
	}
	return a, nil
}

func (a *authorizationClient) request(ctx context.Context, method, target, grant string, body, out any) error {
	var data []byte
	if body != nil {
		var err error
		data, err = json.Marshal(body)
		if err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if grant != "" {
		req.Header.Set(configstore.GrantHeader, grant)
	}
	resp, err := a.client.Do(req)
	if err != nil {
		return publicError(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("MCPHub client authorization returned HTTP %d; the request was not replayed", resp.StatusCode)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out)
}

type grantSnapshot struct {
	Grant           configstore.ClientGrant `json:"grant"`
	EffectiveScopes []string                `json:"effective_scopes"`
}

func (a *authorizationClient) current(ctx context.Context, secret, etag string) (grantSnapshot, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.discovery.GrantsURL+"/current", nil)
	if err != nil {
		return grantSnapshot{}, "", err
	}
	req.Header.Set(configstore.GrantHeader, secret)
	req.Header.Set("If-None-Match", etag)
	resp, err := a.client.Do(req)
	if err != nil {
		return grantSnapshot{}, "", publicError(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotModified {
		return grantSnapshot{}, etag, nil
	}
	if resp.StatusCode != http.StatusOK {
		return grantSnapshot{}, "", fmt.Errorf("authorization status returned HTTP %d", resp.StatusCode)
	}
	var value grantSnapshot
	err = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&value)
	return value, resp.Header.Get("ETag"), err
}

func (s *Store) savePrivateJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	f, err := privateFile(filepath.Join(s.Dir, ".broker-"+rand.Text()), os.O_RDWR|os.O_CREATE|os.O_EXCL)
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, err := f.Write(append(data, '\n')); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return replaceCredentialFile(f.Name(), path)
}

func (s *Store) localClient(name, id string) (localClient, error) {
	if !profileName.MatchString(name) || !profileName.MatchString(id) {
		return localClient{}, errors.New("invalid profile or client ID")
	}
	f, err := privateFile(filepath.Join(s.Dir, "client-"+id+".json"), os.O_RDONLY)
	if err != nil {
		return localClient{}, errors.New("client entry not found; run mcphub-cli client add")
	}
	defer f.Close()
	var entry localClient
	err = json.NewDecoder(io.LimitReader(f, 128<<10)).Decode(&entry)
	if err != nil || entry.Profile != name || entry.Options.ID != id || len(entry.Secret) < 32 {
		return localClient{}, errors.New("client entry does not belong to this profile")
	}
	return entry, nil
}

func AuthorizeClient(ctx context.Context, store *Store, name string, opts ClientOptions) (configstore.ClientGrant, error) {
	var g configstore.ClientGrant
	err := store.locked(ctx, "pair-"+configstore.SecretHash(name)[:32], func() error {
		var err error
		g, err = authorizeClient(ctx, store, name, opts)
		return err
	})
	return g, err
}
func authorizeClient(ctx context.Context, store *Store, name string, opts ClientOptions) (configstore.ClientGrant, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	if opts.Output == nil {
		opts.Output = os.Stderr
	}
	if opts.OpenBrowser == nil {
		opts.OpenBrowser = openBrowser
	}
	var entry localClient
	if opts.ID == "" {
		opts.ID = "ci_" + rand.Text()
		entry = localClient{Options: opts, Profile: name, Secret: rand.Text() + rand.Text()}
	} else {
		var err error
		entry, err = store.localClient(name, opts.ID)
		if err != nil {
			return configstore.ClientGrant{}, err
		}
		for _, changed := range opts.Changed {
			switch changed {
			case "name":
				entry.Options.Name = opts.Name
			case "endpoint":
				entry.Options.Endpoint = opts.Endpoint
			case "scope":
				entry.Options.Scopes = opts.Scopes
			case "tool":
				entry.Options.Tools = opts.Tools
			case "resource":
				entry.Options.ResourceRules = opts.ResourceRules
			case "allow-write-requests":
				entry.Options.AllowWrite = opts.AllowWrite
			case "prompts":
				entry.Options.Capabilities.Prompts = opts.Capabilities.Prompts
			case "resources":
				entry.Options.Capabilities.Resources = opts.Capabilities.Resources
			case "subscriptions":
				entry.Options.Capabilities.Subscriptions = opts.Capabilities.Subscriptions
			case "ttl":
				entry.Options.TTLSeconds = opts.TTLSeconds
			}
		}
		entry.Options.HTTPClient, entry.Options.OpenBrowser, entry.Options.Output = opts.HTTPClient, opts.OpenBrowser, opts.Output
		opts = entry.Options
	}
	if opts.Name == "" || opts.Endpoint == "" {
		return configstore.ClientGrant{}, errors.New("--name and --endpoint are required")
	}
	if opts.Capabilities == (configstore.GrantCapabilities{}) {
		opts.Capabilities.Tools = true
		entry.Options.Capabilities = opts.Capabilities
	}
	a, err := newAuthorizationClient(ctx, store, name, opts.HTTPClient)
	if err != nil {
		return configstore.ClientGrant{}, err
	}
	// Keep one authorization flow per profile: new requests share a proven
	// broker session even when multiple terminals attempt to pair concurrently.
	var request struct {
		ClientOptions
		SessionID string `json:"broker_session_id"`
		Proof     string `json:"broker_session_proof"`
	}
	request.ClientOptions = opts
	var created struct {
		Request  configstore.ClientGrant `json:"request"`
		URL      string                  `json:"confirmation_url"`
		Exchange string                  `json:"exchange_credential"`
		Proof    string                  `json:"broker_session_proof"`
	}
	err = store.locked(ctx, name, func() error {
		p, err := store.load(name)
		if err != nil {
			return err
		}
		if p.Session != a.credentials.session {
			return ErrProfileChanged
		}
		if p.Broker != nil {
			request.SessionID, request.Proof = p.Broker.SessionID, p.Broker.Proof
		}
		return nil
	})
	if err != nil {
		return configstore.ClientGrant{}, err
	}
	err = a.request(ctx, http.MethodPost, a.discovery.RequestsURL, "", request, &created)
	var denied *permissionError
	if errors.As(err, &denied) && denied.code == string(configstore.ErrBrokerSessionEnded) && request.SessionID != "" {
		// Explicit consent creates a new session; ended grants are never transferred.
		request.SessionID, request.Proof = "", ""
		err = a.request(ctx, http.MethodPost, a.discovery.RequestsURL, "", request, &created)
	}
	if err != nil {
		return configstore.ClientGrant{}, err
	}
	confirmation, err := httpsURL(created.URL)
	portal, _ := url.Parse(a.discovery.PortalURL)
	if err != nil || confirmation.Host != portal.Host || confirmation.Path != portal.Path || confirmation.Query().Get("request") != created.Request.GrantID {
		return configstore.ClientGrant{}, errors.New("untrusted confirmation URL")
	}
	err = store.locked(ctx, name, func() error {
		p, err := store.load(name)
		if err != nil {
			return err
		}
		if p.Session != a.credentials.session {
			return ErrProfileChanged
		}
		if p.Broker == nil || request.SessionID == "" {
			p.Broker = &brokerCredentials{SessionID: created.Request.SessionID, Proof: created.Proof, Clients: map[string]clientCredentials{}}
		}
		if p.Broker.SessionID != created.Request.SessionID {
			return errors.New("another pairing created a broker session; retry authorization")
		}
		return store.save(name, p)
	})
	if err != nil {
		return configstore.ClientGrant{}, err
	}
	fmt.Fprintf(opts.Output, "Client: %s (%s)\nPairing code: %s\nReview authorization: %s\n", opts.Name, opts.ID, created.Request.PairingCode, created.URL)
	if err := opts.OpenBrowser(created.URL); err != nil {
		fmt.Fprintln(opts.Output, "Could not open the browser; open the review URL above to continue.")
	}
	timer := time.NewTicker(time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return configstore.ClientGrant{}, errors.New("client authorization canceled or timed out; no new credential was installed")
		case <-timer.C:
		}
		var current configstore.ClientGrant
		if err := a.request(ctx, http.MethodGet, a.discovery.RequestsURL+"/"+created.Request.GrantID, "", nil, &current); err != nil {
			return configstore.ClientGrant{}, err
		}
		if current.Status == "pending" {
			continue
		}
		if current.Status != "confirmed" {
			return configstore.ClientGrant{}, fmt.Errorf("client authorization %s", current.Status)
		}
		var exchanged struct {
			Grant      configstore.ClientGrant `json:"grant"`
			Credential string                  `json:"credential"`
		}
		if err := a.request(ctx, http.MethodPost, a.discovery.RequestsURL+"/"+current.GrantID+"/exchange", "", map[string]string{"exchange_credential": created.Exchange}, &exchanged); err != nil {
			return configstore.ClientGrant{}, err
		}
		entry.Options = opts
		if err := store.savePrivateJSON(filepath.Join(store.Dir, "client-"+opts.ID+".json"), entry); err != nil {
			return configstore.ClientGrant{}, err
		}
		err = store.locked(ctx, name, func() error {
			p, err := store.load(name)
			if err != nil {
				return err
			}
			if p.Session != a.credentials.session || p.Broker == nil || p.Broker.SessionID != exchanged.Grant.SessionID {
				return ErrProfileChanged
			}
			if p.Broker.Clients == nil {
				p.Broker.Clients = map[string]clientCredentials{}
			}
			p.Broker.Clients[opts.ID] = clientCredentials{IPCHash: configstore.SecretHash(entry.Secret), Credential: exchanged.Credential, Grant: exchanged.Grant}
			return store.save(name, p)
		})
		return exchanged.Grant, err
	}
}

type ClientStatus struct {
	configstore.ClientGrant
	EffectiveScopes    []string
	AuthorizationError string
}

func ListClients(ctx context.Context, store *Store, name string, base *http.Client) ([]ClientStatus, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var cached []clientCredentials
	err := store.locked(ctx, name, func() error {
		p, err := store.load(name)
		if err != nil {
			return err
		}
		if p.Broker != nil {
			for _, c := range p.Broker.Clients {
				cached = append(cached, c)
			}
		}
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	a, onlineErr := newAuthorizationClient(ctx, store, name, base)
	values := make([]ClientStatus, 0, len(cached))
	for _, c := range cached {
		value := ClientStatus{ClientGrant: c.Grant}
		if onlineErr == nil {
			if err := a.request(ctx, http.MethodGet, a.discovery.RequestsURL+"/"+c.Grant.GrantID, "", nil, &value.ClientGrant); err != nil {
				onlineErr = err
			} else {
				snapshot, _, err := a.current(ctx, c.Credential, "")
				var denied *permissionError
				if errors.As(err, &denied) {
					value.AuthorizationError = denied.code
				} else if err != nil {
					onlineErr = err
				} else {
					value.EffectiveScopes = snapshot.EffectiveScopes
				}
			}
		}
		values = append(values, value)
	}
	return values, onlineErr == nil, nil
}

func RevokeClient(ctx context.Context, store *Store, name, id string, base *http.Client) error {
	a, err := newAuthorizationClient(ctx, store, name, base)
	if err != nil {
		return err
	}
	var entry clientCredentials
	err = store.locked(ctx, name, func() error {
		p, err := store.load(name)
		if err != nil {
			return err
		}
		if p.Broker == nil {
			return errors.New("no broker session")
		}
		var ok bool
		entry, ok = p.Broker.Clients[id]
		if !ok {
			return errors.New("unknown client")
		}
		return nil
	})
	if err != nil {
		return err
	}
	if err := a.request(ctx, http.MethodPost, a.discovery.GrantsURL+"/"+entry.Grant.GrantID+"/revoke", entry.Credential, nil, nil); err != nil {
		return err
	}
	return store.locked(ctx, name, func() error {
		p, err := store.load(name)
		if err != nil {
			return err
		}
		if p.Broker != nil {
			delete(p.Broker.Clients, id)
		}
		return store.save(name, p)
	})
}

func controlPath(path string) bool {
	for _, base := range []string{"/api/v1/client-authorization", "/api/v1/client-authorization-options", "/api/v1/client-authorization-requests", "/api/v1/client-grants", "/api/v1/broker-sessions"} {
		if path == base || strings.HasPrefix(path, base+"/") {
			return true
		}
	}
	return false
}
