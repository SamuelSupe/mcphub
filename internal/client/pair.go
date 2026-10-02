package client

import (
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

	"github.com/SamuelSupe/mcphub/v2/internal/configstore"
)

type PairOptions struct {
	Server   string
	Profile  string
	ClientID string
	ClientOptions
}

// PairStatus is safe to pass to an Agent. It intentionally has no credential fields.
type PairStatus struct {
	Status                  string    `json:"status"`
	RequestID               string    `json:"request_id"`
	UserCode                string    `json:"user_code,omitempty"`
	VerificationURI         string    `json:"verification_uri,omitempty"`
	VerificationURIComplete string    `json:"verification_uri_complete,omitempty"`
	ExpiresAt               time.Time `json:"expires_at,omitempty"`
	Interval                int64     `json:"interval,omitempty"`
	Profile                 string    `json:"profile"`
	ClientID                string    `json:"client_instance_id"`
	Server                  string    `json:"server"`
	Endpoint                string    `json:"endpoint_id,omitempty"`
	NextStep                string    `json:"next_step"`
}
type pendingPair struct {
	Public         PairStatus    `json:"public"`
	DeviceCode     string        `json:"device_code,omitempty"`
	Issuer         string        `json:"issuer"`
	TokenURL       string        `json:"token_url"`
	OAuthClient    string        `json:"oauth_client_id"`
	ProfileSession string        `json:"profile_session"`
	NextPoll       time.Time     `json:"next_poll"`
	Options        ClientOptions `json:"options"`
}

func (s *Store) pairPath(id string) (string, error) {
	if !strings.HasPrefix(id, "pr_") || !profileName.MatchString(id) {
		return "", errors.New("invalid pairing request ID")
	}
	return filepath.Join(s.Dir, "pair-"+id+".json"), nil
}
func deviceForm(ctx context.Context, c *http.Client, target string, values url.Values, out any) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, strings.NewReader(values.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := c.Do(req)
	if err != nil {
		return "", errors.New("pairing service could not be reached")
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		var result struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&result)
		switch result.Error {
		case "authorization_pending", "slow_down", "access_denied", "expired_token", "invalid_grant", "temporarily_unavailable":
			return result.Error, nil
		}
		return "", fmt.Errorf("pairing service returned HTTP %d", response.StatusCode)
	}
	if err = json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(out); err != nil {
		return "", errors.New("invalid pairing response")
	}
	return "", nil
}
func trustedPairURL(raw string, resource *url.URL, path string) bool {
	u, e := httpsURL(raw)
	return e == nil && u.Host == resource.Host && u.Path == path && u.RawQuery == "" && u.RawPath == ""
}
func PairStart(ctx context.Context, store *Store, opts PairOptions) (PairStatus, error) {
	if opts.Profile == "" {
		opts.Profile = "default"
	}
	if opts.ClientID == "" {
		opts.ClientID = "mcpbridge"
	}
	if opts.Name == "" {
		return PairStatus{}, errors.New("a client name is required")
	}
	if opts.TTLSeconds != 0 && opts.TTLSeconds < 60 {
		return PairStatus{}, errors.New("--ttl must be at least 60 seconds, or 0 for the default")
	}
	if !profileName.MatchString(opts.Profile) {
		return PairStatus{}, errors.New("invalid profile name")
	}
	opts.ID = "ci_" + rand.Text()
	opts.Capabilities = configstore.GrantCapabilities{Tools: true}
	c := httpClient(opts.HTTPClient, 30*time.Second)
	meta, scopes, err := discover(ctx, opts.Server, opts.Scopes, c, false)
	if err != nil {
		return PairStatus{}, err
	}
	u, _ := httpsURL(opts.Server)
	issuer, err := httpsURL(meta.Issuer)
	if err != nil || issuer.Host != u.Host {
		return PairStatus{}, errors.New("device pairing requires MCPHub builtin authentication; use login and client add for an external issuer")
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(meta.Issuer, "/")+"/.well-known/oauth-authorization-server", nil)
	resp, err := c.Do(req)
	if err != nil {
		return PairStatus{}, errors.New("cannot discover device authorization")
	}
	defer resp.Body.Close()
	var deviceMeta struct {
		Issuer    string `json:"issuer"`
		DeviceURL string `json:"device_authorization_endpoint"`
		TokenURL  string `json:"token_endpoint"`
	}
	if resp.StatusCode != 200 || json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&deviceMeta) != nil || deviceMeta.Issuer != meta.Issuer || deviceMeta.TokenURL != meta.TokenEndpoint || !trustedPairURL(deviceMeta.DeviceURL, u, issuer.Path+"/device_authorization") || !trustedPairURL(meta.TokenEndpoint, u, issuer.Path+"/token") {
		return PairStatus{}, errors.New("device authorization is not available; use mcpbridge login then client add")
	}
	var snapshot string
	var session, proof string
	err = store.locked(ctx, opts.Profile, func() error {
		p, e := store.load(opts.Profile)
		if errors.Is(e, os.ErrNotExist) {
			return nil
		}
		if e != nil {
			return e
		}
		if p.Kind == "admin" || p.ServerURL != opts.Server || p.Issuer != meta.Issuer || p.ClientID != opts.ClientID {
			return errors.New("this profile belongs to another server or login purpose; choose a new profile")
		}
		snapshot = p.Session
		if p.Broker != nil {
			session, proof = p.Broker.SessionID, p.Broker.Proof
		}
		return nil
	})
	if err != nil {
		return PairStatus{}, err
	}
	extension := struct {
		Version int `json:"version"`
		ClientOptions
		SessionID string `json:"broker_session_id,omitempty"`
		Proof     string `json:"broker_session_proof,omitempty"`
	}{1, opts.ClientOptions, session, proof}
	raw, _ := json.Marshal(extension)
	var result struct {
		DeviceCode string `json:"device_code"`
		UserCode   string `json:"user_code"`
		URI        string `json:"verification_uri"`
		Complete   string `json:"verification_uri_complete"`
		Expires    int64  `json:"expires_in"`
		Interval   int64  `json:"interval"`
		Extension  struct {
			Version   int    `json:"version"`
			RequestID string `json:"request_id"`
		} `json:"mcphub"`
	}
	code, err := deviceForm(ctx, c, deviceMeta.DeviceURL, url.Values{"client_id": {opts.ClientID}, "resource": {opts.Server}, "scope": {strings.Join(scopes, " ")}, "mcphub": {string(raw)}}, &result)
	if err != nil {
		return PairStatus{}, err
	}
	if code != "" {
		return PairStatus{}, errors.New("pairing request rejected: " + code)
	}
	path, err := store.pairPath(result.Extension.RequestID)
	complete, e := httpsURL(result.Complete)
	if err != nil || e != nil || result.Extension.Version != 1 || result.Expires < 1 || result.Expires > 600 || result.Interval < 1 || result.Interval > 60 || len(result.DeviceCode) < 32 || len(result.DeviceCode) > 256 || !trustedPairURL(result.URI, u, "/client-auth/device") || complete.Host != u.Host || complete.Path != "/client-auth/device" || complete.Query().Get("user_code") != result.UserCode || len(complete.Query()) != 1 || len(result.UserCode) != 9 {
		return PairStatus{}, errors.New("untrusted or incompatible pairing response")
	}
	status := PairStatus{Status: "pending_user", RequestID: result.Extension.RequestID, UserCode: result.UserCode, VerificationURI: result.URI, VerificationURIComplete: result.Complete, ExpiresAt: time.Now().Add(time.Duration(result.Expires) * time.Second), Interval: result.Interval, Profile: opts.Profile, ClientID: opts.ID, Server: opts.Server, Endpoint: opts.Endpoint, NextStep: "Open verification_uri_complete, compare user_code, sign in and select access. Then run pair finish --request " + result.Extension.RequestID + " --wait --json."}
	pending := pendingPair{Public: status, DeviceCode: result.DeviceCode, Issuer: meta.Issuer, TokenURL: meta.TokenEndpoint, OAuthClient: opts.ClientID, ProfileSession: snapshot, Options: opts.ClientOptions, NextPoll: time.Now().Add(time.Duration(status.Interval) * time.Second)}
	err = store.savePrivateJSON(path, pending)
	return status, err
}
