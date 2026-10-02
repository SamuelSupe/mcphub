package configstore

import "context"

// Directory names are display metadata; they cannot change group grants or
// overwrite administrator-managed policy groups.
func (s *Store) SyncGroupNames(ctx context.Context, provider string, names map[string]string) error {
	s.identityMu.Lock()
	defer s.identityMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for id, name := range names {
		if name == "" || len(name) > 256 {
			continue
		}
		group, err := readIdentity(ctx, tx, "provider=? AND kind='group' AND external_id=?", provider, id)
		if err != nil {
			return err
		}
		if group.ManagedLocally || group.Name == name {
			continue
		}
		group.Name = name
		if _, err = saveIdentity(ctx, tx, group, "group_name_synced"); err != nil {
			return err
		}
	}
	return tx.Commit()
}
