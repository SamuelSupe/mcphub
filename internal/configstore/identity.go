package configstore

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/SamuelSupe/mcphub/v2/internal/config"
)

var ErrIdentityDenied = errors.New("user is pending, disabled, or permissions changed; contact the MCPHub administrator")
var ErrIdentityStale = fmt.Errorf("%w: enterprise membership is stale; sign in again or refresh the directory snapshot", ErrIdentityDenied)

type Identity struct {
	SourceGroups      []string                   `json:"source_groups,omitempty"`
	VerifiedAt        time.Time                  `json:"verified_at,omitempty"`
	CredentialVersion int64                      `json:"credential_version,omitempty"`
	ID                string                     `json:"id"`
	Provider          string                     `json:"provider"`
	Kind              string                     `json:"kind"`
	ExternalID        string                     `json:"external_id"`
	Name              string                     `json:"name"`
	Enabled           bool                       `json:"enabled"`
	DirectoryActive   bool                       `json:"directory_active"`
	DirectoryManaged  bool                       `json:"directory_managed"`
	ManagedLocally    bool                       `json:"managed_locally,omitempty"`
	Groups            []string                   `json:"groups"`
	Permissions       config.IdentityPermissions `json:"permissions"`
	Revision          int64                      `json:"revision"`
	UpdatedAt         time.Time                  `json:"updated_at"`
}

type EffectiveIdentity struct {
	Groups            []PermissionSource         `json:"groups,omitempty"`
	Name              string                     `json:"name,omitempty"`
	CredentialVersion int64                      `json:"credential_version,omitempty"`
	ID                string                     `json:"id"`
	Provider          string                     `json:"provider"`
	DirectoryManaged  bool                       `json:"directory_managed"`
	ManagedLocally    bool                       `json:"managed_locally,omitempty"`
	Version           string                     `json:"version"`
	Permissions       config.IdentityPermissions `json:"permissions"`
}

type identityCall struct {
	identity EffectiveIdentity
	cancel   context.CancelFunc
}

func saveSyncedIdentity(ctx context.Context, tx *transaction, p, previous Identity, action string) (Identity, error) {
	if p.Revision > 0 {
		comparison := p
		comparison.VerifiedAt = previous.VerifiedAt
		if reflect.DeepEqual(comparison, previous) {
			if p.VerifiedAt.Equal(previous.VerifiedAt) {
				return p, nil
			}
			data, err := json.Marshal(p)
			if err == nil {
				_, err = tx.ExecContext(ctx, "UPDATE identities SET data=? WHERE id=?", data, p.ID)
			}
			return p, err
		}
	}
	return saveIdentity(ctx, tx, p, action)
}

type identityReader interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func readIdentity(ctx context.Context, db identityReader, clause string, args ...any) (Identity, error) {
	var data []byte
	err := db.QueryRowContext(ctx, "SELECT data FROM identities WHERE "+clause, args...).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return Identity{}, ErrNotFound
	}
	var p Identity
	if err == nil {
		err = json.Unmarshal(data, &p)
	}
	return p, err
}

func (s *Store) Identity(ctx context.Context, id string) (Identity, error) {
	return readIdentity(ctx, s.db, "id=?", id)
}

func (s *Store) Identities(ctx context.Context) ([]Identity, error) {
	return readIdentities(ctx, s.db, "ORDER BY kind,external_id")
}

func readIdentities(ctx context.Context, db identityReader, clause string, args ...any) ([]Identity, error) {
	rows, err := db.QueryContext(ctx, "SELECT data FROM identities "+clause, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []Identity{}
	for rows.Next() {
		var data []byte
		var p Identity
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(data, &p); err != nil {
			return nil, err
		}
		items = append(items, p)
	}
	return items, rows.Err()
}

func saveIdentity(ctx context.Context, tx *transaction, p Identity, action string) (Identity, error) {
	p.Revision++
	p.UpdatedAt = time.Now().UTC()
	data, err := json.Marshal(p)
	if err != nil {
		return p, err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO identities(id,provider,kind,external_id,data) VALUES(?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET data=excluded.data`, p.ID, p.Provider, p.Kind, p.ExternalID, data)
	if err == nil {
		err = insertSourceEvent(ctx, tx, "identity", p.ID, action, true, "", p.Revision, p.UpdatedAt)
	}
	return p, err
}

func ensureIdentity(ctx context.Context, tx *transaction, provider, kind, externalID string) (Identity, error) {
	p, err := readIdentity(ctx, tx, "provider=? AND kind=? AND external_id=?", provider, kind, externalID)
	if errors.Is(err, ErrNotFound) {
		return Identity{ID: "usr_" + rand.Text(), Provider: provider, Kind: kind, ExternalID: externalID, Enabled: kind != "user", DirectoryActive: true}, nil
	}
	return p, err
}

func (s *Store) UpdateIdentity(ctx context.Context, id string, revision int64, enabled bool, permissions config.IdentityPermissions) (Identity, error) {
	return s.updateIdentity(ctx, id, revision, enabled, permissions, nil)
}

func (s *Store) updateIdentity(ctx context.Context, id string, revision int64, enabled bool, permissions config.IdentityPermissions, groups *[]string) (Identity, error) {
	return s.updateIdentityWithMapping(ctx, id, revision, enabled, permissions, groups, nil)
}

func (s *Store) updateIdentityMapping(ctx context.Context, id string, revision int64, enabled bool, permissions config.IdentityPermissions, sourceGroups *[]string) (Identity, error) {
	return s.updateIdentityWithMapping(ctx, id, revision, enabled, permissions, nil, sourceGroups)
}

func (s *Store) updateIdentityWithMapping(ctx context.Context, id string, revision int64, enabled bool, permissions config.IdentityPermissions, groups, sourceGroups *[]string) (Identity, error) {
	if err := permissions.Validate(); err != nil {
		return Identity{}, err
	}
	s.identityMu.Lock()
	defer s.identityMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Identity{}, err
	}
	defer tx.Rollback()
	p, err := readIdentity(ctx, tx, "id=?", id)
	if err != nil {
		return p, err
	}
	if p.Revision != revision {
		return p, ErrConflict
	}
	if p.Kind == "user" && (len(permissions.Roles)+len(permissions.Scopes)+len(permissions.Access) > 0) {
		return p, ErrUserPermissions
	}
	if sourceGroups != nil {
		if p.Kind == "user" || p.Provider != config.PermissionGroupProvider || len(*sourceGroups) > 256 {
			return p, ErrGroupMembership
		}
		ids := slices.Clone(*sourceGroups)
		slices.Sort(ids)
		ids = slices.Compact(ids)
		for _, gid := range ids {
			group, err := readIdentity(ctx, tx, "id=?", gid)
			if err != nil || group.Kind == "user" || group.ManagedLocally || group.Provider == config.PermissionGroupProvider {
				return p, ErrGroupMembership
			}
		}
		p.SourceGroups = ids
	}
	if groups != nil {
		if err := replaceUserGroups(ctx, tx, &p, *groups); err != nil {
			return p, err
		}
	}
	p.Enabled, p.Permissions = enabled, permissions
	if !enabled && p.Kind == "user" && p.Provider == config.LocalIdentityProvider {
		p.CredentialVersion++
		if err := revokeIdentitySessions(ctx, tx, p.ID); err != nil {
			return p, err
		}
	}
	if err := s.checkLastAdministrator(ctx, tx, p); err != nil {
		return Identity{}, err
	}
	p, err = saveIdentity(ctx, tx, p, "identity_permissions_updated")
	if err == nil {
		err = tx.Commit()
	}
	if err == nil {
		s.cancelIdentityCalls(ctx)
	}
	return p, err
}

// SyncIdentity changes identity attributes, never administrator-owned permissions.
// Provisioned membership wins over login claims when directory mode is enabled.
func (s *Store) SyncIdentity(ctx context.Context, provider, subject, name string, groups, departments []string, directory, bootstrap bool) (Identity, error) {
	if subject == "" || len(subject) > 512 || len(name) > 512 || len(groups)+len(departments) > 256 {
		return Identity{}, fmt.Errorf("invalid upstream identity")
	}
	s.identityMu.Lock()
	defer s.identityMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Identity{}, err
	}
	defer tx.Rollback()
	p, err := ensureIdentity(ctx, tx, provider, "user", subject)
	if err != nil {
		return p, err
	}
	previous := p
	if p.Revision == 0 {
		p.Enabled = bootstrap
		p.DirectoryActive = !directory || bootstrap
		p.DirectoryManaged = directory && bootstrap
		if bootstrap {
			group, err := administratorGroup(ctx, tx, provider)
			if err != nil {
				return p, err
			}
			p.Groups = []string{group.ID}
		}
	}
	p.Name = name
	p.VerifiedAt = time.Now().UTC()
	if !directory {
		p.Groups, err = locallyManagedGroups(ctx, tx, p.Groups)
		if err != nil {
			return p, err
		}
		for kind, ids := range map[string][]string{"group": groups, "department": departments} {
			for _, id := range ids {
				if id == "" || len(id) > 256 {
					return p, fmt.Errorf("invalid upstream membership")
				}
				g, err := ensureIdentity(ctx, tx, provider, kind, id)
				if err != nil {
					return p, err
				}
				// Upstream claims cannot join administrator-managed policy groups.
				if g.ManagedLocally {
					return p, ErrGroupMembership
				}
				if g.Revision == 0 {
					g.Name = id
					if g, err = saveIdentity(ctx, tx, g, "identity_discovered"); err != nil {
						return p, err
					}
				}
				p.Groups = append(p.Groups, g.ID)
			}
		}
		slices.Sort(p.Groups)
		p.Groups = slices.Compact(p.Groups)
	}
	p, err = saveSyncedIdentity(ctx, tx, p, previous, "identity_login")
	if err == nil {
		err = tx.Commit()
	}
	if err == nil {
		s.cancelIdentityCalls(ctx)
	}
	return p, err
}

func (s *Store) EffectiveIdentity(ctx context.Context, id string) (EffectiveIdentity, error) {
	s.identityMu.Lock()
	defer s.identityMu.Unlock()
	return s.effectiveIdentity(ctx, id)
}

func (s *Store) effectiveIdentity(ctx context.Context, id string) (EffectiveIdentity, error) {
	return s.effectiveIdentityFrom(ctx, s.db, id)
}

func (s *Store) effectiveIdentityFrom(ctx context.Context, db identityReader, id string) (EffectiveIdentity, error) {
	p, err := readIdentity(ctx, db, "id=?", id)
	if err != nil {
		return EffectiveIdentity{}, err
	}
	if p.Kind != "user" || !p.Enabled || !p.DirectoryActive {
		return EffectiveIdentity{}, ErrIdentityDenied
	}
	if p.Provider != config.LocalIdentityProvider && s.identityMaxAge > 0 && (p.VerifiedAt.IsZero() || time.Since(p.VerifiedAt) > s.identityMaxAge) {
		return EffectiveIdentity{}, ErrIdentityStale
	}
	permissions := config.IdentityPermissions{}
	groups := map[string]Identity{}
	if len(p.Groups) > 0 {
		args := make([]any, len(p.Groups))
		for i, gid := range p.Groups {
			args[i] = gid
		}
		items, err := readIdentities(ctx, db, "WHERE id IN ("+strings.TrimSuffix(strings.Repeat("?,", len(args)), ",")+")", args...)
		if err != nil {
			return EffectiveIdentity{}, err
		}
		for _, g := range items {
			groups[g.ID] = g
		}
	}
	sources, err := permissionSources(ctx, db, p, groups)
	if err != nil {
		return EffectiveIdentity{}, err
	}
	for _, source := range sources {
		g := source.Identity
		permissions.Roles = append(permissions.Roles, g.Permissions.Roles...)
		permissions.Scopes = append(permissions.Scopes, g.Permissions.Scopes...)
		permissions.Access = append(permissions.Access, g.Permissions.Access...)
	}
	slices.Sort(permissions.Roles)
	permissions.Roles = slices.Compact(permissions.Roles)
	slices.Sort(permissions.Scopes)
	permissions.Scopes = slices.Compact(permissions.Scopes)
	data, _ := json.Marshal(struct {
		Permissions config.IdentityPermissions
		Credentials int64
	}{permissions, p.CredentialVersion})
	return EffectiveIdentity{ID: id, Name: p.Name, Provider: p.Provider, CredentialVersion: p.CredentialVersion, DirectoryManaged: p.DirectoryManaged, Version: SecretHash(string(data)), Permissions: permissions, Groups: sources}, nil
}

// Admission and changes share a lock: a cached MCP view cannot start another
// call after permissions are revoked. Cancellation cannot undo completed writes.
func (s *Store) AdmitIdentity(ctx context.Context, p EffectiveIdentity) (context.Context, func(), error) {
	s.identityMu.Lock()
	defer s.identityMu.Unlock()
	current, err := s.effectiveIdentity(ctx, p.ID)
	if err != nil {
		return nil, nil, err
	}
	if current.Version != p.Version {
		return nil, nil, ErrIdentityDenied
	}
	call, cancel := context.WithCancel(ctx)
	identity, identityErr := s.Identity(ctx, p.ID)
	if identityErr != nil {
		cancel()
		return nil, nil, identityErr
	}
	if identity.Provider != config.LocalIdentityProvider && s.identityMaxAge > 0 {
		cancel()
		call, cancel = context.WithDeadline(ctx, identity.VerifiedAt.Add(s.identityMaxAge))
	}
	key := rand.Text()
	if s.identityCalls == nil {
		s.identityCalls = map[string]identityCall{}
	}
	s.identityCalls[key] = identityCall{identity: p, cancel: cancel}
	return call, func() { s.identityMu.Lock(); delete(s.identityCalls, key); s.identityMu.Unlock(); cancel() }, nil
}

func (s *Store) cancelIdentityCalls(ctx context.Context) {
	versions := map[string]string{}
	for key, call := range s.identityCalls {
		version, ok := versions[call.identity.ID]
		if !ok {
			current, err := s.effectiveIdentity(ctx, call.identity.ID)
			if err == nil {
				version = current.Version
			}
			versions[call.identity.ID] = version
		}
		if version != call.identity.Version {
			call.cancel()
			delete(s.identityCalls, key)
		}
	}
}
