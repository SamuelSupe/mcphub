package configstore

import (
	"bytes"
	"context"
	"encoding/base32"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/SamuelSupe/mcphub/v2/internal/config"
)

// Measures authorization latency both under password hashing load and with many groups.
func BenchmarkEffectiveIdentity(b *testing.B) {
	for _, scenario := range []struct {
		name           string
		groups, logins int
	}{{"idle", 1, 0}, {"password-load", 1, 4}, {"64-groups", 64, 0}} {
		b.Run(scenario.name, func(b *testing.B) {
			ctx, cancel := context.WithCancel(b.Context())
			s, err := Open(ctx, filepath.Join(b.TempDir(), "config.db"), make([]byte, 32))
			if err != nil {
				b.Fatal(err)
			}
			defer s.Close()
			user, err := s.SyncIdentity(ctx, config.LocalIdentityProvider, "benchmark", "", nil, nil, false, false)
			if err != nil {
				b.Fatal(err)
			}
			var groups []string
			for i := range scenario.groups {
				g, err := s.CreateGroup(ctx, config.LocalIdentityProvider, fmt.Sprintf("Readers %d", i))
				if err != nil {
					b.Fatal(err)
				}
				if _, err = s.UpdateIdentity(ctx, g.ID, g.Revision, true, config.IdentityPermissions{Scopes: []string{fmt.Sprintf("read:%d", i)}}); err != nil {
					b.Fatal(err)
				}
				groups = append(groups, g.ID)
			}
			if _, err = s.UpdateUser(ctx, user.ID, user.Revision, true, groups); err != nil {
				b.Fatal(err)
			}
			var logins sync.WaitGroup
			for range scenario.logins {
				logins.Go(func() {
					for ctx.Err() == nil {
						_, _, _ = s.AuthenticateLocal(ctx, "missing", "benchmark-password", "", false)
					}
				})
			}
			b.ResetTimer()
			for b.Loop() {
				if _, err := s.EffectiveIdentity(ctx, user.ID); err != nil {
					b.Fatal(err)
				}
			}
			b.StopTimer()
			cancel()
			logins.Wait()
		})
	}
}

func TestLocalAccountLifecycle(t *testing.T) {
	s, _, _ := newTestStore(t)
	testLocalAccountLifecycle(t, s)
}

func testLocalAccountLifecycle(t *testing.T, s *Store) {
	t.Helper()
	ctx := t.Context()
	s.ConfigureIdentityProtection(config.AuthConfig{Mode: "builtin"}, config.AdminConfig{Mode: "local", RequiredScopes: []string{"manage"}})
	defer s.ConfigureIdentityProtection(config.AuthConfig{}, config.AdminConfig{})
	admin, err := s.CreateLocalAccount(ctx, "Local.Admin", "Administrator", "initial-long-password", true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateLocalAccount(ctx, "second-admin", "", "initial-long-password", true); err == nil {
		t.Fatal("initialization reused")
	}
	if _, err := s.UpdateIdentity(ctx, admin.ID, admin.Revision, false, admin.Permissions); !errors.Is(err, ErrLastAdministrator) {
		t.Fatal("last local administrator disabled", err)
	}
	user, err := s.CreateLocalAccount(ctx, "employee", "Employee", "initial-long-password", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(admin.Groups) != 1 || len(admin.Permissions.Roles) != 0 {
		t.Fatal("administrator was not initialized through a group")
	}
	if _, err := s.UpdateUser(ctx, admin.ID, admin.Revision, true, nil); !errors.Is(err, ErrLastAdministrator) {
		t.Fatal("last administrator left its group", err)
	}
	readers, err := s.CreateGroup(ctx, config.LocalIdentityProvider, "Readers")
	if err != nil {
		t.Fatal(err)
	}
	readers, err = s.UpdateIdentity(ctx, readers.ID, readers.Revision, true, config.IdentityPermissions{Scopes: []string{"db:read"}, Access: []config.IdentityAccess{{EndpointID: "db", Tools: []string{"read"}}}})
	if err != nil {
		t.Fatal(err)
	}
	reviewers, err := s.CreateGroup(ctx, config.LocalIdentityProvider, "Reviewers")
	if err != nil {
		t.Fatal(err)
	}
	reviewers, err = s.UpdateIdentity(ctx, reviewers.ID, reviewers.Revision, true, config.IdentityPermissions{Roles: []string{"approver"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateIdentity(ctx, user.ID, user.Revision, true, readers.Permissions); !errors.Is(err, ErrUserPermissions) {
		t.Fatal("direct user permissions accepted", err)
	}
	foreign, err := s.CreateGroup(ctx, "other-provider", "Foreign")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateUser(ctx, user.ID, user.Revision, true, []string{foreign.ID}); !errors.Is(err, ErrGroupMembership) {
		t.Fatal("cross-provider group granted access", err)
	}
	user, err = s.UpdateUser(ctx, user.ID, user.Revision, true, []string{readers.ID, reviewers.ID})
	if err != nil {
		t.Fatal(err)
	}
	effective, err := s.EffectiveIdentity(ctx, user.ID)
	if err != nil || !effective.Permissions.AllowsTool("db", "read", "read") || !reflect.DeepEqual(effective.Permissions.Roles, []string{"approver"}) {
		t.Fatal("multiple group permissions were not inherited", err)
	}
	call, release, err := s.AdmitIdentity(ctx, effective)
	if err != nil {
		t.Fatal(err)
	}
	user, err = s.UpdateUser(ctx, user.ID, user.Revision, true, []string{reviewers.ID})
	if err != nil {
		t.Fatal(err)
	}
	if call.Err() == nil {
		t.Fatal("membership revocation retained a running request")
	}
	release()
	effective, err = s.EffectiveIdentity(ctx, user.ID)
	if err != nil || effective.Permissions.AllowsTool("db", "read", "read") {
		t.Fatal("removed group retained its permissions", err)
	}
	if _, err = s.UpdateUser(ctx, user.ID, user.Revision-1, true, nil); !errors.Is(err, ErrConflict) {
		t.Fatal("stale membership edit accepted", err)
	}
	if _, mfa, err := s.AuthenticateLocal(ctx, "EMPLOYEE", "initial-long-password", "", false); err != nil || mfa {
		t.Fatal("password login", err)
	}
	if _, _, err := s.AuthenticateLocal(ctx, "employee", "initial-long-password", "", true); !errors.Is(err, ErrCredentials) {
		t.Fatal("password was treated as MFA", err)
	}
	session, refresh, err := s.CreateSSOSession(ctx, SSOSession{UserID: user.ID, CredentialVersion: user.CredentialVersion, ClientID: "bridge", Resource: "mcp", ExpiresAt: time.Now().Add(time.Hour)}, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetLocalPassword(ctx, user.ID, "replacement-long-password", user.CredentialVersion, false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SSOSession(ctx, session.ID); err == nil {
		t.Fatal("password reset retained access session")
	}
	if _, err := s.SSORefreshSession(ctx, refresh); err == nil {
		t.Fatal("password reset retained refresh")
	}
	if _, _, err := s.CreateSSOSession(ctx, session, true); !errors.Is(err, ErrIdentityDenied) {
		t.Fatal("old password proof created a new session", err)
	}
	user, _, err = s.AuthenticateLocal(ctx, "employee", "replacement-long-password", "", false)
	if err != nil {
		t.Fatal(err)
	}
	secret, err := s.BeginLocalMFA(ctx, user.ID, user.CredentialVersion)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(secret)
	if err != nil {
		t.Fatal(err)
	}
	var stored []byte
	if err := s.db.QueryRowContext(ctx, "SELECT data FROM local_accounts WHERE identity_id=?", user.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(stored, []byte(secret)) || bytes.Contains(stored, raw) || bytes.Contains(stored, []byte("replacement-long-password")) {
		t.Fatal("plaintext credential stored")
	}
	if err := s.ConfirmLocalMFA(ctx, user.ID, totpCode(raw, time.Now().Unix()/30-1), user.CredentialVersion); err != nil {
		t.Fatal(err)
	}
	code := totpCode(raw, time.Now().Unix()/30)
	var attempts sync.WaitGroup
	results := make(chan error, 4)
	for range 4 {
		attempts.Go(func() {
			_, mfa, err := s.AuthenticateLocal(ctx, "employee", "replacement-long-password", code, true)
			if err == nil && !mfa {
				err = errors.New("MFA login lacked a second factor")
			}
			results <- err
		})
	}
	attempts.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		} else if !errors.Is(err, ErrCredentials) {
			t.Fatal(err)
		}
	}
	if successes != 1 {
		t.Fatalf("concurrent TOTP authentications succeeded %d times, want 1", successes)
	}
	user, err = s.Identity(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.AuthenticateLocal(ctx, "employee", "replacement-long-password", code, true); !errors.Is(err, ErrCredentials) {
		t.Fatal("TOTP replay admitted", err)
	}
	session, _, err = s.CreateSSOSession(ctx, SSOSession{UserID: user.ID, CredentialVersion: user.CredentialVersion, ClientID: "bridge", Resource: "mcp", ExpiresAt: time.Now().Add(time.Hour)}, false)
	if err != nil {
		t.Fatal(err)
	}
	user, err = s.UpdateIdentity(ctx, user.ID, user.Revision, false, user.Permissions)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SSOSession(ctx, session.ID); err == nil {
		t.Fatal("disable retained session")
	}
	if _, err := s.UpdateIdentity(ctx, user.ID, user.Revision, true, user.Permissions); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.CreateSSOSession(ctx, session, false); !errors.Is(err, ErrIdentityDenied) {
		t.Fatal("re-enable restored old password proof", err)
	}
	for range 5 {
		_, _, _ = s.AuthenticateLocal(ctx, "local.admin", "wrong-password", "", false)
	}
	if _, _, err := s.AuthenticateLocal(ctx, "local.admin", "initial-long-password", "", false); !errors.Is(err, ErrLoginLimited) {
		t.Fatal("login lockout bypassed", err)
	}
}

func TestTOTPWindowAndReplay(t *testing.T) {
	secret := []byte("12345678901234567890")
	now := time.Unix(1111111109, 0)
	counter, ok := verifyTOTP(secret, "081804", now, -1)
	if !ok {
		t.Fatal("RFC 6238 SHA1 vector failed")
	}
	if _, ok := verifyTOTP(secret, "081804", now, counter); ok {
		t.Fatal("code reused")
	}
	if _, ok := verifyTOTP(secret, "081804", now.Add(2*time.Minute), -1); ok {
		t.Fatal("expired code accepted")
	}
}

func TestIdentityLifecycle(t *testing.T) { s, _, _ := newTestStore(t); testIdentityLifecycle(t, s) }

func TestLastAdministratorProtection(t *testing.T) {
	s, _, _ := newTestStore(t)
	testLastAdministratorProtection(t, s)
}

func testLastAdministratorProtection(t *testing.T, s *Store) {
	t.Helper()
	ctx := t.Context()
	auth := config.AuthConfig{SSO: &config.SSOConfig{Upstream: config.IdentityProvider{Issuer: "https://admin-protection.example", Protocol: "oidc", ClientID: "admin"}}}
	admin := config.AdminConfig{Mode: "remote", RequiredScopes: []string{"manage"}}
	s.ConfigureIdentityProtection(auth, admin)
	defer s.ConfigureIdentityProtection(config.AuthConfig{}, config.AdminConfig{})
	user, err := s.SyncIdentity(ctx, auth.SSO.Upstream.Namespace(), "first", "First admin", []string{"ops"}, nil, false, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdateIdentity(ctx, user.ID, user.Revision, false, config.IdentityPermissions{}); !errors.Is(err, ErrLastAdministrator) {
		t.Fatal("last administrator disabled", err)
	}
	var group Identity
	var sourceGroups []string
	for _, id := range user.Groups {
		g, err := s.Identity(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if g.ManagedLocally {
			group = g
		} else {
			sourceGroups = append(sourceGroups, id)
		}
	}
	if _, err := s.UpdateUser(ctx, user.ID, user.Revision, true, sourceGroups); !errors.Is(err, ErrLastAdministrator) {
		t.Fatal("last administrator left its policy group", err)
	}
	if _, err := s.UpdateIdentity(ctx, group.ID, group.Revision, false, group.Permissions); !errors.Is(err, ErrLastAdministrator) {
		t.Fatal("last administrator group disabled", err)
	}
	if _, err := s.UpdateIdentity(ctx, group.ID, group.Revision, true, config.IdentityPermissions{}); !errors.Is(err, ErrLastAdministrator) {
		t.Fatal("last administrator group demoted", err)
	}
	second, err := s.SyncIdentity(ctx, auth.SSO.Upstream.Namespace(), "second", "Second admin", nil, nil, false, false)
	if err != nil {
		t.Fatal(err)
	}
	second, err = s.UpdateUser(ctx, second.ID, second.Revision, true, []string{group.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SyncIdentity(ctx, group.Provider, "claim-attacker", "Attacker", []string{group.ExternalID}, nil, false, false); !errors.Is(err, ErrGroupMembership) {
		t.Fatal("upstream claimed a local policy group", err)
	}
	user, err = s.SyncIdentity(ctx, group.Provider, "first", "First admin", nil, nil, false, false)
	if err != nil || !slices.Contains(user.Groups, group.ID) {
		t.Fatal("login removed locally managed membership", err)
	}
	results := make(chan error, 2)
	for _, p := range []Identity{user, second} {
		go func() { _, err := s.UpdateIdentity(ctx, p.ID, p.Revision, false, p.Permissions); results <- err }()
	}
	succeeded, blocked := 0, 0
	for range 2 {
		err := <-results
		if err == nil {
			succeeded++
		} else if errors.Is(err, ErrLastAdministrator) {
			blocked++
		} else {
			t.Fatal(err)
		}
	}
	if succeeded != 1 || blocked != 1 {
		t.Fatalf("concurrent demotions: success=%d blocked=%d", succeeded, blocked)
	}
}

func testIdentityLifecycle(t *testing.T, s *Store) {
	t.Helper()
	ctx := t.Context()
	provider := "https://directory.example"
	user, err := s.SyncIdentity(ctx, provider, "subject-1", "Alice", []string{"engineering"}, []string{"dept-1"}, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.EffectiveIdentity(ctx, user.ID); !errors.Is(err, ErrIdentityDenied) {
		t.Fatal("new user acquired access")
	}
	other, err := s.SyncIdentity(ctx, "https://other.example", "subject-1", "Alice", nil, nil, false, false)
	if err != nil || other.ID == user.ID {
		t.Fatal("provider isolation failed", err)
	}
	group, err := s.Identity(ctx, user.Groups[0])
	if err != nil {
		t.Fatal(err)
	}
	access := config.IdentityAccess{EndpointID: "db", Tools: []string{"read"}, ResourceRules: []config.ResourceRule{{Argument: "/project", AllowedValues: []string{"work"}}}}
	group, err = s.UpdateIdentity(ctx, group.ID, group.Revision, true, config.IdentityPermissions{Scopes: []string{"db:read"}, Access: []config.IdentityAccess{access}})
	if err != nil {
		t.Fatal(err)
	}
	user, err = s.UpdateIdentity(ctx, user.ID, user.Revision, true, config.IdentityPermissions{})
	if err != nil {
		t.Fatal(err)
	}
	effective, err := s.EffectiveIdentity(ctx, user.ID)
	if err != nil || !reflect.DeepEqual(effective.Permissions.Scopes, []string{"db:read"}) {
		t.Fatal("group grant not inherited", err)
	}
	if _, err = effective.Permissions.ToolArguments("db", "read", "read", []byte(`{"project":"work"}`)); err != nil {
		t.Fatal(err)
	}
	for _, args := range []string{`{"project":"secret"}`, `{}`, `{"project":[]}`} {
		if _, err = effective.Permissions.ToolArguments("db", "read", "read", []byte(args)); err == nil {
			t.Fatal("resource escaped group grant")
		}
	}
	call, release, err := s.AdmitIdentity(ctx, effective)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, err = s.SyncIdentity(ctx, provider, "subject-1", "Alice", []string{"engineering"}, []string{"dept-1"}, false, false); err != nil {
		t.Fatal(err)
	}
	if call.Err() != nil {
		t.Fatal("unchanged login cancelled a running request")
	}
	if _, err = s.UpdateIdentity(ctx, group.ID, group.Revision, false, group.Permissions); err != nil {
		t.Fatal(err)
	}
	select {
	case <-call.Done():
	case <-time.After(time.Second):
		t.Fatal("running call not cancelled")
	}
	if _, _, err = s.AdmitIdentity(ctx, effective); !errors.Is(err, ErrIdentityDenied) {
		t.Fatal("stale cached identity admitted")
	}
	if _, err = s.UpdateIdentity(ctx, user.ID, user.Revision-1, false, user.Permissions); !errors.Is(err, ErrConflict) {
		t.Fatal("stale update accepted")
	}
	user, err = s.UpdateIdentity(ctx, user.ID, user.Revision, false, user.Permissions)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.SyncIdentity(ctx, provider, user.ExternalID, user.Name, nil, nil, false, true); err != nil {
		t.Fatal(err)
	}
	if _, err = s.EffectiveIdentity(ctx, user.ID); !errors.Is(err, ErrIdentityDenied) {
		t.Fatal("login/bootstrap overwrote administrator disable")
	}

	snapshot := DirectorySnapshot{Version: 1, Groups: []DirectoryGroup{{Kind: "group", ID: "staff", Name: "Staff"}}, Users: []DirectoryUser{{Subject: "directory-user", Name: "Bob", Active: true, Groups: []string{"staff"}}}}
	if err = s.SyncDirectory(ctx, provider, snapshot); err != nil {
		t.Fatal(err)
	}
	if err = s.SyncDirectory(ctx, provider, snapshot); !errors.Is(err, ErrConflict) {
		t.Fatal("stale snapshot accepted")
	}
	dirUser, err := s.SyncIdentity(ctx, provider, "directory-user", "Bob", []string{"injected-admin-group"}, nil, true, false)
	if err != nil || dirUser.Enabled || len(dirUser.Groups) != 1 {
		t.Fatal("directory state overwritten by login", err)
	}
	dirGroup, err := s.Identity(ctx, dirUser.Groups[0])
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.UpdateIdentity(ctx, dirGroup.ID, dirGroup.Revision, true, config.IdentityPermissions{Roles: []string{"approver"}}); err != nil {
		t.Fatal(err)
	}
	policyGroup, err := s.CreateGroup(ctx, provider, "Review policy")
	if err != nil {
		t.Fatal(err)
	}
	dirUser, err = s.UpdateUser(ctx, dirUser.ID, dirUser.Revision, true, append(slices.Clone(dirUser.Groups), policyGroup.ID))
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Version = 2
	snapshot.Users[0].Active = false
	if err = s.SyncDirectory(ctx, provider, snapshot); err != nil {
		t.Fatal(err)
	}
	if _, err = s.EffectiveIdentity(ctx, dirUser.ID); !errors.Is(err, ErrIdentityDenied) {
		t.Fatal("deactivated directory user retained access")
	}
	snapshot.Version = 3
	snapshot.Users[0].Active = true
	if err = s.SyncDirectory(ctx, provider, snapshot); err != nil {
		t.Fatal(err)
	}
	restored, err := s.EffectiveIdentity(ctx, dirUser.ID)
	currentDirUser, membershipErr := s.Identity(ctx, dirUser.ID)
	if err != nil || membershipErr != nil || !slices.Contains(currentDirUser.Groups, policyGroup.ID) || !reflect.DeepEqual(restored.Permissions.Roles, []string{"approver"}) {
		t.Fatal("directory overwrote local grants", err)
	}
	snapshot.Version = 4
	snapshot.Users = nil
	snapshot.Groups = nil
	if err = s.SyncDirectory(ctx, provider, snapshot); err != nil {
		t.Fatal(err)
	}
	if _, err = s.EffectiveIdentity(ctx, dirUser.ID); !errors.Is(err, ErrIdentityDenied) {
		t.Fatal("omitted user retained access")
	}

	key, err := s.SSOSigningKey(ctx)
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.SSOSigningKey(ctx)
	if err != nil || !reflect.DeepEqual(key, again) {
		t.Fatal("signing key changed", err)
	}
	user, err = s.SyncIdentity(ctx, "https://session.example", "refresh-user", "Refresh user", nil, nil, false, false)
	if err != nil {
		t.Fatal(err)
	}
	user, err = s.UpdateUser(ctx, user.ID, user.Revision, true, user.Groups)
	if err != nil {
		t.Fatal(err)
	}
	session, refresh, err := s.CreateSSOSession(ctx, SSOSession{UserID: user.ID, CredentialVersion: user.CredentialVersion, ClientID: "cli", Resource: "https://hub/mcp", ExpiresAt: time.Now().Add(time.Hour)}, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = s.RotateSSORefresh(ctx, refresh, "attacker", "https://hub/mcp"); err == nil {
		t.Fatal("refresh crossed client boundary")
	}
	_, rotated, err := s.RotateSSORefresh(ctx, refresh, "cli", "https://hub/mcp")
	if err != nil || rotated == refresh {
		t.Fatal("rotation failed", err)
	}
	if _, _, err = s.RotateSSORefresh(ctx, refresh, "cli", "https://hub/mcp"); err == nil {
		t.Fatal("refresh replay accepted")
	}
	if _, err = s.SSOSession(context.Background(), session.ID); err == nil {
		t.Fatal("replayed family not revoked")
	}
}
