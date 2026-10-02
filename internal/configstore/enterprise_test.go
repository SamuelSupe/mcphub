package configstore

import (
	"bytes"
	"errors"
	"testing"
	"time"

	"github.com/SamuelSupe/mcphub/v2/internal/config"
)

func TestEnterpriseConnectionsLifecycle(t *testing.T) {
	s, key, path := newTestStore(t)
	testEnterpriseConnectionsLifecycle(t, s)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(t.Context(), path, key)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	c, err := reopened.EnterpriseConnections(t.Context())
	if err != nil || c.Revision != 3 || !c.LDAP.Enabled || c.OIDC.ClientSecret != "encrypted-oidc-secret" {
		t.Fatal("connection lost on reopen", err)
	}
}

func testEnterpriseConnectionsLifecycle(t *testing.T, s *Store) {
	t.Helper()
	ctx := t.Context()
	previous := config.EnterpriseConnections{}
	next := config.EnterpriseConnections{
		OIDC: config.OIDCConnection{Enabled: true, Provider: config.IdentityProvider{Protocol: "oidc", Issuer: "https://directory.example.com", ClientID: "app"}, ClientSecret: "encrypted-oidc-secret"},
		LDAP: config.LDAPConnection{Enabled: true, URL: "ldaps://ldap.example.com", BindPassword: "encrypted-ldap-password"},
	}
	current, err := s.SaveEnterpriseConnections(ctx, previous, next)
	if err != nil || current.Revision != 1 {
		t.Fatal("initial connection save", err)
	}
	var sealed []byte
	if err := s.db.QueryRowContext(ctx, "SELECT value FROM metadata WHERE key=?", enterpriseKey).Scan(&sealed); err != nil || bytes.Contains(sealed, []byte(next.OIDC.ClientSecret)) || bytes.Contains(sealed, []byte(next.LDAP.BindPassword)) {
		t.Fatal("connection credentials stored without encryption", err)
	}
	loaded, err := s.EnterpriseConnections(ctx)
	if err != nil || loaded.OIDC.ClientSecret != next.OIDC.ClientSecret || loaded.LDAP.BindPassword != next.LDAP.BindPassword {
		t.Fatal("connection round trip", err)
	}
	if _, err := s.SaveEnterpriseConnections(ctx, previous, next); !errors.Is(err, ErrConflict) {
		t.Fatal("stale connection update accepted", err)
	}
	user, err := s.SyncIdentity(ctx, next.LDAP.Namespace(), "stable-guid", "LDAP user", nil, nil, false, false)
	if err != nil {
		t.Fatal(err)
	}
	user, err = s.UpdateUser(ctx, user.ID, user.Revision, true, nil)
	if err != nil {
		t.Fatal(err)
	}
	session, refresh, err := s.CreateSSOSession(ctx, SSOSession{UserID: user.ID, CredentialVersion: user.CredentialVersion, ClientID: "bridge", Resource: "mcp", ExpiresAt: time.Now().Add(time.Hour)}, true)
	if err != nil {
		t.Fatal(err)
	}
	var affected []SSOSession
	var unaffected SSOSession
	for _, source := range []string{next.LDAP.Namespace(), next.OIDC.Provider.Namespace()} {
		other, err := s.SyncIdentity(ctx, source, "another-user", "", nil, nil, false, false)
		if err != nil {
			t.Fatal(err)
		}
		other, err = s.UpdateUser(ctx, other.ID, other.Revision, true, nil)
		if err != nil {
			t.Fatal(err)
		}
		ss, _, err := s.CreateSSOSession(ctx, SSOSession{UserID: other.ID, CredentialVersion: other.CredentialVersion, ClientID: "bridge", Resource: "mcp", ExpiresAt: time.Now().Add(time.Hour)}, true)
		if err != nil {
			t.Fatal(err)
		}
		if source == next.LDAP.Namespace() {
			affected = append(affected, ss)
		} else {
			unaffected = ss
		}
	}
	changed := loaded
	changed.LDAP.Enabled = false
	current, err = s.SaveEnterpriseConnections(ctx, loaded, changed)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SSOSession(ctx, session.ID); err == nil {
		t.Fatal("disabled connection retained its session")
	}
	if _, err := s.SSORefreshSession(ctx, refresh); err == nil {
		t.Fatal("disabled connection retained its refresh")
	}
	for _, ss := range affected {
		if _, err := s.SSOSession(ctx, ss.ID); err == nil {
			t.Fatal("source update retained another user's session")
		}
	}
	if _, err := s.SSOSession(ctx, unaffected.ID); err != nil {
		t.Fatal("source update revoked an unrelated provider", err)
	}
	newUser, err := s.Identity(ctx, user.ID)
	if err != nil || newUser.CredentialVersion <= user.CredentialVersion || !newUser.Enabled {
		t.Fatal("connection revocation did not preserve identity with a new credential epoch", err)
	}
	changed = current
	changed.LDAP.Enabled = true
	if _, err := s.SaveEnterpriseConnections(ctx, current, changed); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.CreateSSOSession(ctx, session, true); !errors.Is(err, ErrIdentityDenied) {
		t.Fatal("source re-enable revived the old credential epoch", err)
	}
}
