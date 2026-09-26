package upstream

import (
	"context"
	"crypto/rand"
	"errors"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/SamuelSupe/mcphub/v2/internal/authn"
	"github.com/SamuelSupe/mcphub/v2/internal/backend"
	"github.com/SamuelSupe/mcphub/v2/internal/config"
	"github.com/SamuelSupe/mcphub/v2/internal/configstore"
	"golang.org/x/oauth2"
)

type Endpoint struct {
	ID, UID, URL string
	Credentials  *config.CredentialConfig
}

func (e Endpoint) Policy() string { return config.CredentialPolicy(e.URL, e.Credentials) }

type Secret struct {
	Token   *oauth2.Token `json:"token"`
	Scopes  []string      `json:"scopes"`
	Invalid bool          `json:"invalid,omitempty"`
}

type pendingSecret struct {
	value   Secret
	version int
}

type credentialLock struct {
	sync.Mutex
	users int
}

type Manager struct {
	Vault   *Vault
	Store   *configstore.Store
	HTTP    *http.Client
	locksMu sync.Mutex
	locks   map[string]*credentialLock
	pending sync.Map
	loginMu sync.Mutex
	logins  map[string]Login
}

func New(cfg *config.VaultConfig, store *configstore.Store) (*Manager, error) {
	if cfg == nil {
		return nil, nil
	}
	vault, err := NewVault(*cfg)
	if err != nil {
		return nil, err
	}
	return &Manager{Vault: vault, Store: store, HTTP: authn.LoginHTTPClient(), logins: map[string]Login{}, locks: map[string]*credentialLock{}}, nil
}

func (m *Manager) Close() {
	if m == nil {
		return
	}
	m.Vault.Close()
	m.HTTP.CloseIdleConnections()
}

func (m *Manager) lock(key string) func() {
	m.locksMu.Lock()
	mu := m.locks[key]
	if mu == nil {
		mu = &credentialLock{}
		m.locks[key] = mu
	}
	mu.users++
	m.locksMu.Unlock()
	mu.Lock()
	return func() {
		mu.Unlock()
		m.locksMu.Lock()
		mu.users--
		if mu.users == 0 {
			delete(m.locks, key)
		}
		m.locksMu.Unlock()
	}
}

func (m *Manager) path(id string) string { return m.Vault.cfg.Prefix + "/accounts/" + id }

func (m *Manager) Shared(e Endpoint) func(context.Context) (http.Header, error) {
	if m == nil || e.Credentials == nil {
		return nil
	}
	path := e.Credentials.Path
	if e.Credentials.Mode == "personal" {
		path = e.Credentials.DiscoveryPath
	}
	if path == "" {
		return nil
	}
	return func(ctx context.Context) (http.Header, error) {
		headers, err := m.Vault.Header(ctx, e.Credentials, path)
		if err != nil {
			return nil, ErrUnavailable
		}
		return headers, nil
	}
}

func (m *Manager) Binding(ctx context.Context, e Endpoint, issuer, subject string) (configstore.CredentialBinding, error) {
	if m == nil || issuer == "" || subject == "" || e.UID == "" {
		return configstore.CredentialBinding{}, ErrConnect
	}
	b, err := m.Store.CredentialBinding(ctx, issuer, subject, e.UID)
	if err != nil {
		return b, ErrUnavailable
	}
	if !b.Connected {
		return b, ErrConnect
	}
	if b.Policy != e.Policy() {
		return b, ErrReconnect
	}
	return b, nil
}

func (m *Manager) Authorize(e Endpoint, b configstore.CredentialBinding) func(context.Context) (http.Header, error) {
	return func(ctx context.Context) (http.Header, error) {
		unlock := m.lock(b.ID)
		defer unlock()
		current, err := m.Binding(ctx, e, b.Issuer, b.Subject)
		if err != nil {
			return nil, err
		}
		if current.ID != b.ID || current.Revision != b.Revision {
			return nil, ErrReconnect
		}
		var secret Secret
		version, err := m.Vault.Read(ctx, m.path(b.ID), &secret)
		if err != nil {
			return nil, err
		}
		if pending, ok := m.pending.Load(b.ID); ok {
			p := pending.(pendingSecret)
			// Persist rotated credentials before any caller may use them. A failed
			// Vault write must never cause a second consumption of the old refresh token.
			if version == p.version {
				if err := m.Vault.Write(ctx, m.path(b.ID), p.value, p.version); err != nil {
					return nil, err
				}
				secret, version = p.value, version+1
			} else if version != p.version+1 || !sameSecret(secret, p.value) {
				return nil, ErrReconnect
			}
			m.pending.Delete(b.ID)
		}
		if secret.Invalid || secret.Token == nil || secret.Token.AccessToken == "" {
			return nil, ErrReconnect
		}
		if !secret.Token.Expiry.IsZero() && !time.Now().Add(30*time.Second).Before(secret.Token.Expiry) {
			if secret.Token.RefreshToken == "" || e.Credentials.OAuth == nil {
				if !time.Now().Before(secret.Token.Expiry) {
					return nil, ErrReconnect
				}
			} else {
				updated, err := m.refresh(ctx, e, secret)
				if err != nil {
					if errors.Is(err, ErrReconnect) {
						secret.Invalid = true
						m.pending.Store(b.ID, pendingSecret{secret, version})
						if m.Vault.Write(ctx, m.path(b.ID), secret, version) == nil {
							m.pending.Delete(b.ID)
						}
					}
					return nil, err
				}
				m.pending.Store(b.ID, pendingSecret{updated, version})
				if err := m.Vault.Write(ctx, m.path(b.ID), updated, version); err != nil {
					return nil, err
				}
				m.pending.Delete(b.ID)
				secret = updated
			}
		}
		if secret.Invalid || e.Credentials.OAuth != nil && !containsScopes(secret.Scopes, e.Credentials.OAuth.Scopes) {
			return nil, ErrReconnect
		}
		current, err = m.Binding(ctx, e, b.Issuer, b.Subject)
		if err != nil {
			return nil, err
		}
		if current.ID != b.ID || current.Revision != b.Revision {
			return nil, ErrReconnect
		}
		return credentialHeader(e.Credentials, secret.Token.AccessToken)
	}
}

func (m *Manager) Connect(ctx context.Context, e Endpoint, b configstore.CredentialBinding, secret Secret) (configstore.CredentialBinding, error) {
	unlock := m.lock("binding:" + b.Issuer + "\x00" + b.Subject + "\x00" + e.UID)
	defer unlock()
	if secret.Token == nil {
		return b, ErrReconnect
	}
	if _, err := credentialHeader(e.Credentials, secret.Token.AccessToken); err != nil {
		return b, err
	}
	old, err := m.Store.CredentialBinding(ctx, b.Issuer, b.Subject, e.UID)
	if err != nil {
		return b, ErrUnavailable
	}
	if old.Revision != b.Revision {
		return b, ErrConflict
	}
	b.ID, b.EndpointID, b.EndpointUID, b.Policy, b.Connected = "cr_"+rand.Text(), e.ID, e.UID, e.Policy(), true
	b.ExpiresAt, b.Renewable, b.Scopes = secret.Token.Expiry, secret.Token.RefreshToken != "", slices.Clone(secret.Scopes)
	if err := m.Store.ScheduleCredentialCleanup(ctx, b.ID, time.Now().Add(10*time.Minute)); err != nil {
		return b, ErrUnavailable
	}
	if err := m.Vault.Write(ctx, m.path(b.ID), secret, 0); err != nil {
		return b, err
	}
	updated, err := m.Store.SaveCredentialBinding(configstore.WithActor(ctx, b.Subject), b, b.Revision)
	if err != nil {
		_ = m.Vault.Delete(ctx, m.path(b.ID))
		if errors.Is(err, configstore.ErrConflict) {
			return b, ErrConflict
		}
		return b, ErrUnavailable
	}
	if old.ID != "" {
		_ = m.cleanup(ctx, old.ID)
	}
	return updated, nil
}

func (m *Manager) Disconnect(ctx context.Context, e Endpoint, issuer, subject string, expected int64) error {
	unlock := m.lock("binding:" + issuer + "\x00" + subject + "\x00" + e.UID)
	defer unlock()
	b, err := m.Store.CredentialBinding(ctx, issuer, subject, e.UID)
	if err != nil {
		return ErrUnavailable
	}
	if b.Revision != expected {
		return ErrConflict
	}
	if !b.Connected {
		return nil
	}
	b.Connected = false
	if _, err := m.Store.SaveCredentialBinding(configstore.WithActor(ctx, subject), b, b.Revision); err != nil {
		return ErrUnavailable
	}
	// Stop admission before contacting Vault, including when Vault is offline.
	return m.cleanup(ctx, b.ID)
}

func containsScopes(have, need []string) bool {
	for _, scope := range need {
		if !slices.Contains(have, scope) {
			return false
		}
	}
	return true
}

func VerifyBackend(ctx context.Context, cfg config.BackendConfig, secret Secret) error {
	if secret.Token == nil {
		return ErrReconnect
	}
	headers, err := credentialHeader(cfg.Credentials, secret.Token.AccessToken)
	if err != nil {
		return err
	}
	_, err = backend.Probe(ctx, cfg, time.Minute, func(context.Context) (http.Header, error) { return headers, nil })
	if err != nil {
		return ErrReconnect
	}
	return nil
}

func (m *Manager) Status(ctx context.Context, e Endpoint, b configstore.CredentialBinding) (configstore.CredentialBinding, string) {
	if !b.Connected {
		return b, "not_connected"
	}
	if b.Policy != e.Policy() {
		b.Renewable = false
		return b, "reconnect_required"
	}
	var secret Secret
	if _, err := m.Vault.Read(ctx, m.path(b.ID), &secret); err != nil {
		if errors.Is(err, ErrConnect) {
			return b, "reconnect_required"
		}
		return b, "unavailable"
	}
	if secret.Token == nil || secret.Invalid {
		b.Renewable = false
		return b, "reconnect_required"
	}
	b.ExpiresAt, b.Renewable, b.Scopes = secret.Token.Expiry, secret.Token.RefreshToken != "" && e.Credentials.OAuth != nil, slices.Clone(secret.Scopes)
	if !b.ExpiresAt.IsZero() && time.Now().After(b.ExpiresAt) && !b.Renewable {
		return b, "expired"
	}
	return b, "connected"
}

func (m *Manager) cleanup(ctx context.Context, id string) error {
	unlock := m.lock(id)
	defer unlock()
	m.pending.Delete(id)
	if err := m.Vault.Delete(ctx, m.path(id)); err != nil {
		return err
	}
	return m.Store.FinishCredentialCleanup(ctx, id)
}

func sameSecret(a, b Secret) bool {
	if a.Token == nil || b.Token == nil {
		return false
	}
	return a.Invalid == b.Invalid && slices.Equal(a.Scopes, b.Scopes) && a.Token.AccessToken == b.Token.AccessToken && a.Token.RefreshToken == b.Token.RefreshToken && a.Token.Expiry.Equal(b.Token.Expiry)
}
func (m *Manager) Maintain(ctx context.Context) error {
	ids, err := m.Store.CredentialCleanup(ctx)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err := m.cleanup(ctx, id); err != nil {
			return err
		}
	}
	return nil
}
