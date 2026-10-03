package sso

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/SamuelSupe/mcphub/v2/internal/config"
	"github.com/SamuelSupe/mcphub/v2/internal/configstore"
	jose "github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/go-ldap/ldap/v3"
	"golang.org/x/oauth2"
)

func TestLDAPStableSubject(t *testing.T) {
	guid := []byte{0, 255, 1, 128, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
	entry := &ldap.Entry{DN: "cn=Alice,dc=example,dc=org", Attributes: []*ldap.EntryAttribute{{Name: "objectGUID", ByteValues: [][]byte{guid}}}}
	subject, err := ldapSubject(entry, "objectGUID")
	if err != nil || subject == "" {
		t.Fatal("binary GUID rejected", err)
	}
	entry.DN = "cn=Renamed Alice,dc=example,dc=org"
	if renamed, err := ldapSubject(entry, "objectGUID"); err != nil || renamed != subject {
		t.Fatal("rename changed the stable identity", err)
	}
	entry.Attributes[0].ByteValues = append(entry.Attributes[0].ByteValues, guid)
	if _, err := ldapSubject(entry, "objectGUID"); err == nil {
		t.Fatal("ambiguous stable identity accepted")
	}
}

// Runs against a real TLS directory, so bind, filter escaping, operational UUID
// attributes and group membership cannot pass through a mock implementation.
func TestLDAPDirectoryOAuth(t *testing.T) {
	raw := os.Getenv("MCPHUB_TEST_LDAP_URL")
	if raw == "" {
		t.Skip("MCPHUB_TEST_LDAP_URL is not set")
	}
	ca, err := os.ReadFile(os.Getenv("MCPHUB_TEST_LDAP_CA"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	store, err := configstore.Open(ctx, filepath.Join(t.TempDir(), "ldap.db"), make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	local, err := store.CreateLocalAccount(ctx, "alice", "Local Alice", "local-alice-password", true)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Auth: config.AuthConfig{Mode: "builtin", SSO: &config.SSOConfig{}}}
	service, err := New(ctx, cfg, store)
	if err != nil {
		t.Fatal(err)
	}
	hub := httptest.NewTLSServer(service)
	defer hub.Close()
	cfg.Auth.Issuer, cfg.Server.PublicURL = hub.URL+"/sso", hub.URL+"/mcp"
	cfg.Auth.SSO.Clients = []config.SSOClient{{ID: "bridge", RedirectURIs: []string{"http://127.0.0.1/oauth/callback"}, Resources: []string{cfg.Server.PublicURL}}}
	p := config.LDAPConnection{Enabled: true, URL: raw, BindDN: "cn=admin,dc=example,dc=org", BindPassword: "fixture-directory-only", UserBaseDN: "ou=people,dc=example,dc=org", UserFilter: "(&(objectClass=person)(uid={{username}}))", UserIDAttribute: "entryUUID", NameAttribute: "cn", GroupBaseDN: "ou=groups,dc=example,dc=org", GroupFilter: "(&(objectClass=groupOfNames)(member={{dn}}))", GroupIDAttribute: "entryUUID", GroupNameAttribute: "cn", RootCAPEM: string(ca)}
	c, err := service.UpdateConnections(ctx, 0, config.EnterpriseConnections{LDAP: p})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.ProbeConnections(ctx, c, "ldap"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.TestConnection(ctx, c, "ldap", local.ID, "alice", "wrong-password", "127.0.0.1"); !errors.Is(err, configstore.ErrCredentials) {
		t.Fatal("LDAP preview accepted a bad user password", err)
	}
	preview, err := service.TestConnection(ctx, c, "ldap", local.ID, "alice", "fixture-alice-password", "127.0.0.1")
	if err != nil || preview.Status != "passed" || preview.Identity.Subject == "" || preview.Identity.Name != "LDAP Alice" || len(preview.Identity.Groups) != 1 {
		t.Fatal("LDAP preview did not verify the user and group mapping", preview, err)
	}
	identitiesBeforeLogin, err := store.Identities(ctx)
	if err != nil || slices.ContainsFunc(identitiesBeforeLogin, func(p configstore.Identity) bool { return p.Provider == c.LDAP.Namespace() }) {
		t.Fatal("LDAP preview provisioned identities", err)
	}
	startTLS := c
	startTLS.LDAP.URL = os.Getenv("MCPHUB_TEST_LDAP_STARTTLS_URL")
	if startTLS.LDAP.URL != "" {
		if err := service.ProbeConnections(ctx, startTLS, "ldap"); err != nil {
			t.Fatal("StartTLS", err)
		}
	}
	untrusted := c
	untrusted.LDAP.RootCAPEM = ""
	if err := service.ProbeConnections(ctx, untrusted, "ldap"); err == nil {
		t.Fatal("untrusted LDAP certificate accepted")
	}
	for _, credentials := range [][2]string{{"alice", ""}, {"alice", "wrong-password"}, {"*)(uid=*)", "fixture-alice-password"}} {
		if _, err := service.authenticateLDAP(ctx, p, credentials[0], credentials[1], "127.0.0.1:1234"); !errors.Is(err, configstore.ErrCredentials) {
			t.Fatal("invalid credentials or filter injection accepted", err)
		}
	}
	browser := hub.Client()
	browser.Jar, _ = cookiejar.New(nil)
	browser.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	login := func() (url.Values, string) {
		t.Helper()
		verifier := oauth2.GenerateVerifier()
		q := url.Values{"client_id": {"bridge"}, "redirect_uri": {"http://127.0.0.1:43101/oauth/callback"}, "resource": {cfg.Server.PublicURL}, "response_type": {"code"}, "code_challenge_method": {"S256"}, "code_challenge": {oauth2.S256ChallengeFromVerifier(verifier)}, "state": {"ldap-caller"}, "scope": {"directory:read openid offline_access"}, "source": {"enterprise"}}
		response, err := browser.Get(hub.URL + "/sso/authorize?" + q.Encode())
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(response.Body)
		response.Body.Close()
		if response.StatusCode != 200 || !strings.Contains(string(body), `value="ldap"`) {
			t.Fatal("LDAP login choice missing", response.StatusCode)
		}
		state := ""
		for _, cookie := range response.Cookies() {
			if strings.HasPrefix(cookie.Name, "__Host-mcphub-sso-") {
				state = strings.TrimPrefix(cookie.Name, "__Host-mcphub-sso-")
			}
		}
		form := url.Values{"state": {state}, "provider": {"ldap"}, "username": {"alice"}, "password": {"fixture-alice-password"}}
		req, _ := http.NewRequest("POST", hub.URL+"/sso/login", strings.NewReader(form.Encode()))
		req.Header.Set("Origin", hub.URL)
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		response, err = browser.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != 303 {
			t.Fatal("LDAP login failed", response.StatusCode)
		}
		target, _ := url.Parse(response.Header.Get("Location"))
		return target.Query(), verifier
	}
	result, _ := login()
	if result.Get("error_description") != "account_access_required" {
		t.Fatal("new LDAP account was not pending", result)
	}
	identities, err := store.Identities(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var user, group configstore.Identity
	for _, identity := range identities {
		if identity.Provider == p.Namespace() {
			if identity.Kind == "user" {
				user = identity
			} else if identity.Kind == "group" {
				group = identity
			}
		}
	}
	if user.ID == "" || user.ID == local.ID || user.Enabled || user.Name != "LDAP Alice" || group.Name != "engineering" || !slices.Contains(user.Groups, group.ID) {
		t.Fatal("LDAP identity isolation or group mapping failed")
	}
	group, err = store.UpdateIdentity(ctx, group.ID, group.Revision, true, config.IdentityPermissions{Scopes: []string{"directory:read"}})
	if err != nil {
		t.Fatal(err)
	}
	user, err = store.UpdateIdentity(ctx, user.ID, user.Revision, true, config.IdentityPermissions{})
	if err != nil {
		t.Fatal(err)
	}
	result, verifier := login()
	response, err := browser.PostForm(hub.URL+"/sso/token", url.Values{"grant_type": {"authorization_code"}, "client_id": {"bridge"}, "redirect_uri": {"http://127.0.0.1:43101/oauth/callback"}, "resource": {cfg.Server.PublicURL}, "code": {result.Get("code")}, "code_verifier": {verifier}})
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var token map[string]any
	if response.StatusCode != 200 || json.NewDecoder(response.Body).Decode(&token) != nil {
		t.Fatal("LDAP OAuth exchange failed", response.StatusCode)
	}
	info, err := service.Verifier(cfg.Server.PublicURL).Verify(ctx, token["access_token"].(string), nil)
	if err != nil || info.UserID != user.ID || !slices.Contains(info.Scopes, "directory:read") {
		t.Fatal("LDAP group access not enforced", err)
	}
	id, err := jwt.ParseSigned(token["id_token"].(string), []jose.SignatureAlgorithm{jose.EdDSA})
	var evidence struct {
		ACR string `json:"acr"`
	}
	if err != nil || id.Claims(service.key.Public(), &evidence) != nil || evidence.ACR != "urn:mcphub:auth:ldap-password" {
		t.Fatal("LDAP password fabricated MFA evidence", err)
	}
	restarted, err := New(ctx, cfg, store)
	if err != nil {
		t.Fatal(err)
	}
	if !restarted.Connections().LDAP.Enabled || restarted.Connections().LDAP.BindPassword != p.BindPassword {
		t.Fatal("LDAP connection lost on restart")
	}
	c = service.Connections()
	c.LDAP.Enabled = false
	if _, err = service.UpdateConnections(ctx, c.Revision, c); err != nil {
		t.Fatal(err)
	}
	c = service.Connections()
	c.LDAP.Enabled = true
	if _, err = service.UpdateConnections(ctx, c.Revision, c); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Verifier(cfg.Server.PublicURL).Verify(ctx, token["access_token"].(string), nil); err == nil {
		t.Fatal("LDAP source re-enable resurrected old access")
	}
	if _, err := store.SSORefreshSession(ctx, token["refresh_token"].(string)); err == nil {
		t.Fatal("LDAP source retained old refresh")
	}
}
