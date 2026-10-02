package configstore

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"slices"
	"strings"

	"github.com/SamuelSupe/mcphub/v2/internal/config"
)

var (
	ErrUserPermissions = errors.New("assign permissions to groups, not users")
	ErrGroupMembership = errors.New("invalid group membership; groups must belong to the same identity provider and enterprise memberships are managed by the identity source")
)

func createGroup(ctx context.Context, tx *transaction, provider, name string, permissions config.IdentityPermissions) (Identity, error) {
	group := Identity{
		ID: "usr_" + rand.Text(), ExternalID: "grp_" + rand.Text(),
		Provider: provider, Kind: "group", Name: name,
		Enabled: true, DirectoryActive: true, ManagedLocally: true, Permissions: permissions,
	}
	return saveIdentity(ctx, tx, group, "group_created")
}

// A private group, never an upstream claim, supplies bootstrap administration.
func administratorGroup(ctx context.Context, tx *transaction, provider string) (Identity, error) {
	key := "administrator_group:" + SecretHash(provider)
	var id []byte
	err := tx.QueryRowContext(ctx, "SELECT value FROM metadata WHERE key=?", key).Scan(&id)
	if err == nil {
		return readIdentity(ctx, tx, "id=?", string(id))
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return Identity{}, err
	}
	group, err := createGroup(ctx, tx, provider, "Administrators", config.IdentityPermissions{Roles: []string{"admin"}})
	if err == nil {
		_, err = tx.ExecContext(ctx, "INSERT INTO metadata(key,value) VALUES(?,?)", key, []byte(group.ID))
	}
	return group, err
}

func (s *Store) CreateGroup(ctx context.Context, provider, name string) (Identity, error) {
	name = strings.TrimSpace(name)
	if provider == "" || name == "" || len(name) > 128 {
		return Identity{}, errors.New("a group name of 1–128 bytes and an identity provider are required")
	}
	s.identityMu.Lock()
	defer s.identityMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Identity{}, err
	}
	defer tx.Rollback()
	group, err := createGroup(ctx, tx, provider, name, config.IdentityPermissions{})
	if err == nil {
		err = tx.Commit()
	}
	return group, err
}

// UpdateUser changes enablement and group membership atomically. Upstream-owned
// memberships must remain unchanged; only locally managed groups are editable.
func (s *Store) UpdateUser(ctx context.Context, id string, revision int64, enabled bool, groups []string) (Identity, error) {
	return s.updateIdentity(ctx, id, revision, enabled, config.IdentityPermissions{}, &groups)
}

func replaceUserGroups(ctx context.Context, tx *transaction, user *Identity, groups []string) error {
	if user.Kind != "user" || len(groups) > 256 {
		return ErrGroupMembership
	}
	groups = slices.Clone(groups)
	slices.Sort(groups)
	groups = slices.Compact(groups)
	var external []string
	for _, id := range groups {
		group, err := readIdentity(ctx, tx, "id=?", id)
		if err != nil || group.Kind == "user" || group.Provider != user.Provider {
			return ErrGroupMembership
		}
		if !group.ManagedLocally {
			external = append(external, id)
		}
	}
	var previousExternal []string
	for _, id := range user.Groups {
		group, err := readIdentity(ctx, tx, "id=?", id)
		if err != nil {
			return err
		}
		if !group.ManagedLocally {
			previousExternal = append(previousExternal, id)
		}
	}
	slices.Sort(previousExternal)
	if !slices.Equal(external, slices.Compact(previousExternal)) {
		return ErrGroupMembership
	}
	user.Groups = groups
	return nil
}

func locallyManagedGroups(ctx context.Context, tx *transaction, groups []string) ([]string, error) {
	var result []string
	for _, id := range groups {
		group, err := readIdentity(ctx, tx, "id=?", id)
		if err != nil {
			return nil, err
		}
		if group.ManagedLocally {
			result = append(result, id)
		}
	}
	return result, nil
}
