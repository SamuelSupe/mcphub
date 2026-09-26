package upstream

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/SamuelSupe/mcphub/v2/internal/config"
)

var (
	ErrUnavailable = errors.New("credential service is unavailable; try again later")
	ErrConnect     = errors.New("connect your upstream account in the MCPHub personal portal")
	ErrReconnect   = errors.New("upstream authorization expired or changed; reconnect your account in the MCPHub personal portal")
	ErrConflict    = errors.New("account connection changed; refresh the page and try again")
)

type Vault struct {
	cfg     config.VaultConfig
	client  *http.Client
	mu      sync.Mutex
	token   string
	expires time.Time
}

func NewVault(cfg config.VaultConfig) (*Vault, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = 10 * time.Second
	if cfg.CAFile != "" {
		pem, err := os.ReadFile(cfg.CAFile)
		if err != nil {
			return nil, errors.New("cannot read Vault CA file")
		}
		roots, err := x509.SystemCertPool()
		if err != nil {
			roots = x509.NewCertPool()
		}
		if !roots.AppendCertsFromPEM(pem) {
			return nil, errors.New("invalid Vault CA file")
		}
		transport.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	}
	v := &Vault{cfg: cfg, client: &http.Client{Transport: transport, Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	if cfg.TokenEnv != "" {
		v.token = os.Getenv(cfg.TokenEnv)
		if v.token == "" {
			return nil, errors.New("Vault token environment variable is empty")
		}
	} else if os.Getenv(cfg.RoleIDEnv) == "" || os.Getenv(cfg.SecretIDEnv) == "" {
		return nil, errors.New("Vault AppRole environment variables are empty")
	}
	return v, nil
}

func (v *Vault) Close() { v.client.CloseIdleConnections() }

func (v *Vault) accessToken(ctx context.Context) (string, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.token != "" && (v.cfg.TokenEnv != "" || time.Now().Add(30*time.Second).Before(v.expires)) {
		return v.token, nil
	}
	var response struct {
		Auth struct {
			Token    string `json:"client_token"`
			Duration int64  `json:"lease_duration"`
		} `json:"auth"`
	}
	if err := v.request(ctx, http.MethodPost, "auth/"+v.cfg.AuthMount+"/login", "", map[string]string{"role_id": os.Getenv(v.cfg.RoleIDEnv), "secret_id": os.Getenv(v.cfg.SecretIDEnv)}, &response); err != nil {
		return "", err
	}
	if response.Auth.Token == "" || response.Auth.Duration <= 0 || response.Auth.Duration > 315360000 {
		return "", ErrUnavailable
	}
	v.token = response.Auth.Token
	v.expires = time.Now().Add(time.Duration(response.Auth.Duration) * time.Second)
	return v.token, nil
}

func (v *Vault) request(ctx context.Context, method, path, token string, input, output any) error {
	var body []byte
	var err error
	if input != nil {
		body, err = json.Marshal(input)
		if err != nil {
			return ErrUnavailable
		}
	}
	u, _ := url.Parse(v.cfg.Address)
	u.Path = "/v1/" + path
	req, err := http.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(body))
	if err != nil {
		return ErrUnavailable
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if token != "" {
		req.Header.Set("X-Vault-Token", token)
	}
	if v.cfg.Namespace != "" {
		req.Header.Set("X-Vault-Namespace", v.cfg.Namespace)
	}
	resp, err := v.client.Do(req)
	if err != nil {
		return ErrUnavailable
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil || len(data) > 1<<20 {
		return ErrUnavailable
	}
	if resp.StatusCode == http.StatusNotFound {
		return ErrConnect
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ErrUnavailable
	}
	if output != nil && json.Unmarshal(data, output) != nil {
		return ErrUnavailable
	}
	return nil
}

func (v *Vault) Read(ctx context.Context, path string, target any) (int, error) {
	if !config.ValidVaultPath(path) {
		return 0, ErrUnavailable
	}
	token, err := v.accessToken(ctx)
	if err != nil {
		return 0, err
	}
	var envelope struct {
		Data struct {
			Data     json.RawMessage `json:"data"`
			Metadata struct {
				Version int `json:"version"`
			} `json:"metadata"`
		} `json:"data"`
	}
	if err := v.request(ctx, http.MethodGet, v.cfg.Mount+"/data/"+path, token, nil, &envelope); err != nil {
		return 0, err
	}
	if envelope.Data.Metadata.Version < 1 || json.Unmarshal(envelope.Data.Data, target) != nil {
		return 0, ErrUnavailable
	}
	return envelope.Data.Metadata.Version, nil
}

func (v *Vault) Write(ctx context.Context, path string, value any, version int) error {
	if !config.ValidVaultPath(path) {
		return ErrUnavailable
	}
	token, err := v.accessToken(ctx)
	if err != nil {
		return err
	}
	return v.request(ctx, http.MethodPost, v.cfg.Mount+"/data/"+path, token, map[string]any{"data": value, "options": map[string]int{"cas": version}}, nil)
}

func (v *Vault) Delete(ctx context.Context, path string) error {
	if !config.ValidVaultPath(path) {
		return ErrUnavailable
	}
	token, err := v.accessToken(ctx)
	if err != nil {
		return err
	}
	err = v.request(ctx, http.MethodDelete, v.cfg.Mount+"/metadata/"+path, token, nil, nil)
	if errors.Is(err, ErrConnect) {
		return nil
	}
	return err
}

func (v *Vault) Header(ctx context.Context, c *config.CredentialConfig, path string) (http.Header, error) {
	var values map[string]string
	if _, err := v.Read(ctx, path, &values); err != nil {
		return nil, err
	}
	return credentialHeader(c, values[c.Field])
}

func credentialHeader(c *config.CredentialConfig, token string) (http.Header, error) {
	if c == nil || token == "" || len(token) > 32768 || strings.ContainsAny(token, "\r\n\x00") {
		return nil, ErrReconnect
	}
	value := token
	if c.Scheme != "" {
		value = c.Scheme + " " + token
	}
	return http.Header{c.Header: []string{value}}, nil
}

func (v *Vault) Check(ctx context.Context) error {
	token, err := v.accessToken(ctx)
	if err != nil {
		return err
	}
	return v.request(ctx, http.MethodGet, "auth/token/lookup-self", token, nil, nil)
}
