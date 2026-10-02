package client

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/SamuelSupe/mcphub/v2/internal/config"
	"github.com/SamuelSupe/mcphub/v2/internal/configstore"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/oauth2"
)

func PairFinish(ctx context.Context, store *Store, id string, wait bool, base *http.Client) (PairStatus, error) {
	var status PairStatus
	err := store.locked(ctx, "pair-"+configstore.SecretHash(id)[:32], func() error { var e error; status, e = finishPair(ctx, store, id, wait, base); return e })
	return status, err
}
func finishPair(ctx context.Context, store *Store, id string, wait bool, base *http.Client) (PairStatus, error) {
	path, err := store.pairPath(id)
	if err != nil {
		return PairStatus{}, err
	}
	f, err := privateFile(path, os.O_RDONLY)
	if err != nil {
		return PairStatus{}, errors.New("pairing request not found on this computer")
	}
	var p pendingPair
	err = json.NewDecoder(io.LimitReader(f, 256<<10)).Decode(&p)
	f.Close()
	if err != nil || p.Public.RequestID != id {
		return PairStatus{}, errors.New("invalid private pairing record")
	}
	if p.Public.Status == "ready" {
		return verifyPairReady(ctx, store, p.Public, base)
	}
	if p.DeviceCode == "" {
		if _, err := store.localClient(p.Public.Profile, p.Public.ClientID); err == nil {
			p.Public.Status = "ready"
			return verifyPairReady(ctx, store, p.Public, base)
		}
		return p.Public, nil
	}
	resource, err := httpsURL(p.Public.Server)
	if err != nil {
		return PairStatus{}, err
	}
	issuer, err := httpsURL(p.Issuer)
	if err != nil || issuer.Host != resource.Host || !trustedPairURL(p.TokenURL, resource, issuer.Path+"/token") {
		return PairStatus{}, errors.New("untrusted private pairing record")
	}
	c := httpClient(base, 30*time.Second)
	privateCode := p.DeviceCode
	installed := false
	defer func() {
		if installed || p.Public.Status == "pending_user" {
			return
		}
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		var result map[string]any
		_, _ = deviceForm(cleanup, c, p.TokenURL, url.Values{"grant_type": {configstore.DeviceGrantType}, "device_code": {privateCode}, "client_id": {p.OAuthClient}, "mcphub_cancel": {"1"}}, &result)
	}()
	saveState := func(state, message string) error {
		p.Public.Status, p.Public.NextStep = state, message
		if state != "pending_user" {
			p.DeviceCode = ""
		}
		return store.savePrivateJSON(path, p)
	}
	for {
		if !time.Now().Before(p.Public.ExpiresAt) {
			err = saveState("expired", "Start a new pairing request.")
			return p.Public, err
		}
		if time.Now().Before(p.NextPoll) {
			if !wait {
				return p.Public, nil
			}
			timer := time.NewTimer(time.Until(p.NextPoll))
			select {
			case <-ctx.Done():
				timer.Stop()
				_ = saveState("canceled", "Pairing canceled; start again explicitly.")
				return p.Public, ctx.Err()
			case <-timer.C:
			}
		}
		var result struct {
			Access    string `json:"access_token"`
			Refresh   string `json:"refresh_token"`
			Type      string `json:"token_type"`
			Expires   int64  `json:"expires_in"`
			Scope     string `json:"scope"`
			Extension struct {
				Version   int    `json:"version"`
				Replaces  string `json:"replaces_broker_session_id"`
				GrantID   string `json:"grant_id"`
				SessionID string `json:"broker_session_id"`
				Proof     string `json:"broker_session_proof"`
				Exchange  string `json:"exchange_credential"`
			} `json:"mcphub"`
		}
		code, e := deviceForm(ctx, c, p.TokenURL, url.Values{"grant_type": {configstore.DeviceGrantType}, "device_code": {p.DeviceCode}, "client_id": {p.OAuthClient}, "resource": {p.Public.Server}}, &result)
		if e != nil {
			if ctx.Err() != nil {
				_ = saveState("canceled", "Pairing canceled; start again explicitly.")
				return p.Public, ctx.Err()
			}
			p.Public.Interval = min(60, max(5, p.Public.Interval*2))
			p.NextPoll = time.Now().Add(time.Duration(p.Public.Interval) * time.Second)
			p.Public.NextStep = "Service unavailable. Retry status after the polling interval; no operation was replayed."
			if err = store.savePrivateJSON(path, p); err != nil {
				return p.Public, err
			}
			if !wait {
				return p.Public, nil
			}
			continue
		}
		p.NextPoll = time.Now().Add(time.Duration(p.Public.Interval) * time.Second)
		switch code {
		case "authorization_pending", "slow_down", "temporarily_unavailable":
			if code == "slow_down" {
				p.Public.Interval += 5
				p.NextPoll = time.Now().Add(time.Duration(p.Public.Interval) * time.Second)
			}
			if code == "temporarily_unavailable" {
				p.Public.Interval = min(60, max(5, p.Public.Interval*2))
				p.NextPoll = time.Now().Add(time.Duration(p.Public.Interval) * time.Second)
			}
			if err = store.savePrivateJSON(path, p); err != nil {
				return p.Public, err
			}
			if !wait {
				return p.Public, nil
			}
			continue
		case "access_denied":
			err = saveState("denied", "Access denied. Start again only after reviewing permissions.")
			return p.Public, err
		case "expired_token":
			err = saveState("expired", "Start a new pairing request.")
			return p.Public, err
		case "invalid_grant":
			err = saveState("unavailable", "This request was already consumed or is invalid. Start a new pairing; credentials cannot be retrieved again.")
			return p.Public, err
		}
		// After a successful response, never repeat redemption, even if local installation fails.
		p.DeviceCode = ""
		p.Public.Status = "unavailable"
		p.Public.NextStep = "Credential delivery failed. Start a new pairing; the previous profile was preserved."
		if err = store.savePrivateJSON(path, p); err != nil {
			return p.Public, errors.New("cannot record one-time delivery; previous profile preserved")
		}
		if result.Type != "Bearer" || result.Access == "" || result.Expires <= 0 || result.Expires > 3600 || result.Extension.Version != 1 || len(result.Extension.Proof) < 32 || len(result.Extension.Exchange) < 32 {
			return p.Public, errors.New("invalid credential delivery; start a new pairing")
		}
		token := (&oauth2.Token{AccessToken: result.Access, RefreshToken: result.Refresh, TokenType: "Bearer", Expiry: time.Now().Add(time.Duration(result.Expires) * time.Second)}).WithExtra(map[string]any{"scope": result.Scope})
		origin := *resource
		origin.Path, origin.RawPath, origin.RawQuery = "", "", ""
		transport := &pairTransport{base: c.Transport, origin: origin.Host, token: token.AccessToken}
		apiClient := httpClient(c, 30*time.Second)
		apiClient.Transport = transport
		a := authorizationClient{client: apiClient}
		var exchanged struct {
			Grant      configstore.ClientGrant `json:"grant"`
			Credential string                  `json:"credential"`
		}
		target := origin.String() + "/api/v1/client-authorization-requests/" + result.Extension.GrantID + "/exchange"
		if err = a.request(ctx, http.MethodPost, target, "", map[string]string{"exchange_credential": result.Extension.Exchange}, &exchanged); err != nil {
			return p.Public, errors.New("authorization exchange failed; previous profile preserved; start again")
		}
		if exchanged.Grant.ClientID != p.Public.ClientID || exchanged.Grant.SessionID != result.Extension.SessionID || exchanged.Grant.Resource != p.Public.Server || len(exchanged.Credential) < 32 {
			return p.Public, errors.New("authorization binding mismatch; start again")
		}
		revoke := func() {
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()
			_ = a.request(cleanup, http.MethodPost, origin.String()+"/api/v1/client-grants/"+exchanged.Grant.GrantID+"/revoke", "", map[string]string{}, nil)
		}
		transport.grant = exchanged.Credential
		remote, err := mcp.NewClient(&mcp.Implementation{Name: "mcpbridge-pair-check", Version: "1"}, nil).Connect(ctx, &mcp.StreamableClientTransport{Endpoint: p.Public.Server, HTTPClient: apiClient, DisableStandaloneSSE: true, MaxRetries: -1}, nil)
		if err == nil {
			_, err = remote.ListTools(ctx, nil)
			remote.Close()
		}
		if err != nil {
			revoke()
			return p.Public, errors.New("MCP connection check failed; previous profile preserved; start a new pairing")
		}
		local := localClient{Profile: p.Public.Profile, Options: p.Options, Secret: rand.Text() + rand.Text()}
		local.Options.Endpoint = exchanged.Grant.EndpointID
		err = store.locked(ctx, p.Public.Profile, func() error {
			old, e := store.load(p.Public.Profile)
			if errors.Is(e, os.ErrNotExist) {
				if p.ProfileSession != "" {
					return ErrProfileChanged
				}
				old = &profile{Version: 1, Session: rand.Text(), ServerURL: p.Public.Server, Issuer: p.Issuer, ClientID: p.OAuthClient, TokenURL: p.TokenURL}
			} else if e != nil {
				return e
			} else if old.Session != p.ProfileSession || old.Kind == "admin" || old.ServerURL != p.Public.Server || old.Issuer != p.Issuer || old.ClientID != p.OAuthClient {
				return ErrProfileChanged
			}
			if old.Token != nil && tokenSubject(old.Token.AccessToken) != exchanged.Grant.Subject {
				return errors.New("this profile belongs to a different user; choose a new profile")
			}
			if old.Broker != nil && old.Broker.SessionID != exchanged.Grant.SessionID && old.Broker.SessionID != result.Extension.Replaces {
				return errors.New("another pairing changed this profile; start again")
			}
			candidate := *old
			candidate.Token = token
			candidate.Scopes = strings.Fields(result.Scope)
			if old.Broker == nil || old.Broker.SessionID == result.Extension.Replaces {
				candidate.Broker = &brokerCredentials{SessionID: result.Extension.SessionID, Proof: result.Extension.Proof, Clients: map[string]clientCredentials{}}
			} else {
				b := *old.Broker
				b.Clients = make(map[string]clientCredentials, len(old.Broker.Clients)+1)
				for k, v := range old.Broker.Clients {
					b.Clients[k] = v
				}
				candidate.Broker = &b
			}
			// Retain an existing token that already covers the new grant, preserving other clients' scopes.
			if old.Token != nil && old.Token.Valid() && !slices.ContainsFunc(exchanged.Grant.AllowedScopes, func(v string) bool { return !slices.Contains(old.Scopes, v) }) {
				candidate.Token, candidate.Scopes = old.Token, old.Scopes
			}
			candidate.Broker.Clients[local.Options.ID] = clientCredentials{IPCHash: configstore.SecretHash(local.Secret), Credential: exchanged.Credential, Grant: exchanged.Grant}
			entryPath := filepath.Join(store.Dir, "client-"+local.Options.ID+".json")
			if e = store.savePrivateJSON(entryPath, local); e != nil {
				return e
			}
			if e = store.save(p.Public.Profile, &candidate); e != nil {
				os.Remove(entryPath)
				return e
			}
			return nil
		})
		if err != nil {
			revoke()
			return p.Public, errors.New("could not install paired client; previous profile preserved; use a new profile or pair again")
		}
		installed = true
		p.Public.Status, p.Public.Endpoint, p.Public.NextStep = "ready", exchanged.Grant.EndpointID, "Connection checked. Use mcpbridge connect --profile "+p.Public.Profile+" --client "+p.Public.ClientID+"."
		err = store.savePrivateJSON(path, p)
		return p.Public, err
	}
}
func tokenSubject(raw string) string {
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return ""
	}
	data, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var v struct {
		Subject string `json:"sub"`
	}
	_ = json.Unmarshal(data, &v)
	return v.Subject
}

type pairTransport struct {
	base                 http.RoundTripper
	origin, token, grant string
}

func (t *pairTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Scheme != "https" || r.URL.Host != t.origin {
		return nil, errors.New("credential destination mismatch")
	}
	copy := r.Clone(r.Context())
	copy.Header = copy.Header.Clone()
	copy.Header.Set("Authorization", "Bearer "+t.token)
	if t.grant != "" {
		copy.Header.Set(configstore.GrantHeader, t.grant)
	}
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(copy)
}
func verifyPairReady(ctx context.Context, store *Store, status PairStatus, base *http.Client) (PairStatus, error) {
	a, err := newAuthorizationClient(ctx, store, status.Profile, base)
	if err != nil {
		status.Status = "unavailable"
		status.NextStep = "Connection unavailable. Start authentication explicitly; do not replay business calls."
		return status, nil
	}
	var entry clientCredentials
	err = store.locked(ctx, status.Profile, func() error {
		p, e := store.load(status.Profile)
		if e != nil {
			return e
		}
		if p.Broker == nil {
			return ErrProfileChanged
		}
		entry = p.Broker.Clients[status.ClientID]
		return nil
	})
	if err == nil {
		var snapshot grantSnapshot
		snapshot, _, err = a.current(ctx, entry.Credential, "", false)
		if err == nil && slices.ContainsFunc(snapshot.Grant.AllowedScopes, func(scope string) bool { return !slices.Contains(snapshot.EffectiveScopes, scope) }) {
			err = errors.New("group scope was revoked")
		}
		if err == nil {
			var options struct {
				Endpoints []config.ClientEndpointOption `json:"endpoints"`
			}
			err = a.request(ctx, http.MethodGet, a.discovery.OptionsURL, "", nil, &options)
			allowed := false
			for _, endpoint := range options.Endpoints {
				if endpoint.ID != entry.Grant.EndpointID {
					continue
				}
				allowed = !slices.ContainsFunc(entry.Grant.AllowedTools, func(name string) bool {
					return !slices.ContainsFunc(endpoint.Tools, func(tool config.ClientToolOption) bool { return tool.Name == name })
				})
			}
			if err == nil && !allowed {
				err = errors.New("group tool access was revoked")
			}
		}
	}
	if err != nil {
		status.Status = "unavailable"
		status.NextStep = "Authorization expired or was revoked. Start authentication explicitly; do not replay business calls."
	}
	return status, nil
}
