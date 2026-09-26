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
	"time"

	"github.com/SamuelSupe/mcphub/v2/internal/config"
)

var ErrIdentityDenied = errors.New("user is pending, disabled, or permissions changed; contact the MCPHub administrator")

type Identity struct {
	ID               string                     `json:"id"`
	Provider         string                     `json:"provider"`
	Kind             string                     `json:"kind"`
	ExternalID       string                     `json:"external_id"`
	Name             string                     `json:"name"`
	Enabled          bool                       `json:"enabled"`
	DirectoryActive  bool                       `json:"directory_active"`
	DirectoryManaged bool                       `json:"directory_managed"`
	Groups           []string                   `json:"groups"`
	Permissions      config.IdentityPermissions `json:"permissions"`
	Revision         int64                      `json:"revision"`
	UpdatedAt        time.Time                  `json:"updated_at"`
}

type EffectiveIdentity struct {
	ID               string                     `json:"id"`
	Provider         string                     `json:"provider"`
	DirectoryManaged bool                       `json:"directory_managed"`
	Version          string                     `json:"version"`
	Permissions      config.IdentityPermissions `json:"permissions"`
}

type identityCall struct {
	identity EffectiveIdentity
	cancel   context.CancelFunc
}

func saveSyncedIdentity(ctx context.Context, tx *transaction, p, previous Identity, action string) (Identity, error) {
	if p.Revision > 0 && reflect.DeepEqual(p, previous) {
		return p, nil
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
	rows, err := s.db.QueryContext(ctx, "SELECT data FROM identities ORDER BY kind,external_id")
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
	p.Enabled, p.Permissions = enabled, permissions
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
			p.Permissions.Roles = []string{"admin"}
		}
	}
	p.Name = name
	if !directory {
		p.Groups = nil
		for kind, ids := range map[string][]string{"group": groups, "department": departments} {
			for _, id := range ids {
				if id == "" || len(id) > 256 {
					return p, fmt.Errorf("invalid upstream membership")
				}
				g, err := ensureIdentity(ctx, tx, provider, kind, id)
				if err != nil {
					return p, err
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
	p, err := s.Identity(ctx, id)
	if err != nil {
		return EffectiveIdentity{}, err
	}
	if p.Kind != "user" || !p.Enabled || !p.DirectoryActive {
		return EffectiveIdentity{}, ErrIdentityDenied
	}
	permissions := p.Permissions
	for _, gid := range p.Groups {
		g, err := s.Identity(ctx, gid)
		if err != nil {
			return EffectiveIdentity{}, err
		}
		if g.Kind == "user" || g.Provider != p.Provider || !g.Enabled || !g.DirectoryActive {
			continue
		}
		permissions.Roles = append(permissions.Roles, g.Permissions.Roles...)
		permissions.Scopes = append(permissions.Scopes, g.Permissions.Scopes...)
		permissions.Access = append(permissions.Access, g.Permissions.Access...)
	}
	slices.Sort(permissions.Roles)
	permissions.Roles = slices.Compact(permissions.Roles)
	slices.Sort(permissions.Scopes)
	permissions.Scopes = slices.Compact(permissions.Scopes)
	data, _ := json.Marshal(permissions)
	return EffectiveIdentity{ID: id, Provider: p.Provider, DirectoryManaged: p.DirectoryManaged, Version: SecretHash(string(data)), Permissions: permissions}, nil
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
