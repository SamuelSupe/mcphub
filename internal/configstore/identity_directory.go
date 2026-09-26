package configstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strconv"
)

type DirectorySnapshot struct {
	Version int64            `json:"version"`
	Users   []DirectoryUser  `json:"users"`
	Groups  []DirectoryGroup `json:"groups"`
}
type DirectoryUser struct {
	Subject     string   `json:"subject"`
	Name        string   `json:"name"`
	Active      bool     `json:"active"`
	Groups      []string `json:"groups"`
	Departments []string `json:"departments"`
}
type DirectoryGroup struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
	Name string `json:"name"`
}

// SyncDirectory atomically replaces one provider's authoritative membership.
// A monotonic version rejects stale/out-of-order snapshots, including retries.
// Omitted users become inactive; local enablement and grants are preserved.
func (s *Store) SyncDirectory(ctx context.Context, provider string, snapshot DirectorySnapshot) error {
	if snapshot.Version < 1 || len(snapshot.Users) > 10000 || len(snapshot.Groups) > 2000 {
		return fmt.Errorf("invalid directory size or version")
	}
	groups := map[string]DirectoryGroup{}
	users := map[string]bool{}
	for _, g := range snapshot.Groups {
		key := g.Kind + ":" + g.ID
		if (g.Kind != "group" && g.Kind != "department") || g.ID == "" || len(g.ID) > 256 || len(g.Name) > 512 {
			return fmt.Errorf("invalid directory group")
		}
		if _, ok := groups[key]; ok {
			return fmt.Errorf("duplicate directory group")
		}
		groups[key] = g
	}
	for _, u := range snapshot.Users {
		if u.Subject == "" || len(u.Subject) > 512 || len(u.Name) > 512 || users[u.Subject] || len(u.Groups)+len(u.Departments) > 256 {
			return fmt.Errorf("invalid directory user")
		}
		users[u.Subject] = true
		for kind, ids := range map[string][]string{"group": u.Groups, "department": u.Departments} {
			for _, id := range ids {
				if _, ok := groups[kind+":"+id]; !ok {
					return fmt.Errorf("unknown directory membership")
				}
			}
		}
	}
	s.identityMu.Lock()
	defer s.identityMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	key := "directory_version:" + SecretHash(provider)
	var raw []byte
	err = tx.QueryRowContext(ctx, "SELECT value FROM metadata WHERE key=?", key).Scan(&raw)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil {
		version, err := strconv.ParseInt(string(raw), 10, 64)
		if err != nil {
			return err
		}
		if snapshot.Version <= version {
			return ErrConflict
		}
	}
	ids := map[string]string{}
	present := map[string]bool{}
	for external, g := range groups {
		p, err := ensureIdentity(ctx, tx, provider, g.Kind, g.ID)
		if err != nil {
			return err
		}
		previous := p
		p.Name, p.DirectoryActive = g.Name, true
		p.DirectoryManaged = true
		if p, err = saveSyncedIdentity(ctx, tx, p, previous, "directory_synced"); err != nil {
			return err
		}
		ids[external] = p.ID
		present[p.ID] = true
	}
	for _, u := range snapshot.Users {
		p, err := ensureIdentity(ctx, tx, provider, "user", u.Subject)
		if err != nil {
			return err
		}
		previous := p
		p.Name, p.DirectoryActive, p.Groups = u.Name, u.Active, nil
		p.DirectoryManaged = true
		for _, id := range u.Groups {
			p.Groups = append(p.Groups, ids["group:"+id])
		}
		for _, id := range u.Departments {
			p.Groups = append(p.Groups, ids["department:"+id])
		}
		slices.Sort(p.Groups)
		p.Groups = slices.Compact(p.Groups)
		if p, err = saveSyncedIdentity(ctx, tx, p, previous, "directory_synced"); err != nil {
			return err
		}
		present[p.ID] = true
	}
	rows, err := tx.QueryContext(ctx, "SELECT id FROM identities WHERE provider=?", provider)
	if err != nil {
		return err
	}
	var missing []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			break
		}
		if !present[id] {
			missing = append(missing, id)
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range missing {
		p, err := readIdentity(ctx, tx, "id=?", id)
		if err != nil {
			return err
		}
		if p.DirectoryActive {
			p.DirectoryActive = false
			if _, err = saveIdentity(ctx, tx, p, "directory_deactivated"); err != nil {
				return err
			}
		}
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO metadata(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, []byte(strconv.FormatInt(snapshot.Version, 10))); err != nil {
		return err
	}
	if err = tx.Commit(); err == nil {
		s.cancelIdentityCalls(ctx)
	}
	return err
}
