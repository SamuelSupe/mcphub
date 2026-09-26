package configstore

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/SamuelSupe/mcphub/v2/internal/config"
)

func TestIdentityLifecycle(t *testing.T) { s, _, _ := newTestStore(t); testIdentityLifecycle(t, s) }

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
	dirUser, err = s.UpdateIdentity(ctx, dirUser.ID, dirUser.Revision, true, config.IdentityPermissions{Roles: []string{"approver"}})
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
	if err != nil || !reflect.DeepEqual(restored.Permissions.Roles, []string{"approver"}) {
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
	session, refresh, err := s.CreateSSOSession(ctx, SSOSession{UserID: user.ID, ClientID: "cli", Resource: "https://hub/mcp", ExpiresAt: time.Now().Add(time.Hour)}, true)
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
