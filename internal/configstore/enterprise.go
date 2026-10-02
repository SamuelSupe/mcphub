package configstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	"github.com/SamuelSupe/mcphub/v2/internal/config"
)

const enterpriseKey = "identity_connections"

func (s *Store) EnterpriseConnections(ctx context.Context) (config.EnterpriseConnections, error) {
	var sealed []byte
	err := s.db.QueryRowContext(ctx, "SELECT value FROM metadata WHERE key=?", enterpriseKey).Scan(&sealed)
	if errors.Is(err, sql.ErrNoRows) {
		return config.EnterpriseConnections{}, ErrNotFound
	}
	var result config.EnterpriseConnections
	if err == nil {
		var plain []byte
		plain, err = s.open(enterpriseKey, sealed)
		if err == nil {
			err = json.Unmarshal(plain, &result)
		}
	}
	return result, err
}

// Configuration replacement and credential revocation commit together. Keeping
// the identity namespace preserves grants, but never resurrects old sessions.
func (s *Store) SaveEnterpriseConnections(ctx context.Context, previous, next config.EnterpriseConnections) (config.EnterpriseConnections, error) {
	s.identityMu.Lock()
	defer s.identityMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return next, err
	}
	defer tx.Rollback()
	var old []byte
	err = tx.QueryRowContext(ctx, "SELECT value FROM metadata WHERE key=?", enterpriseKey).Scan(&old)
	if errors.Is(err, sql.ErrNoRows) && previous.Revision == 0 {
		err = nil
	} else if err == nil {
		plain, openErr := s.open(enterpriseKey, old)
		if openErr != nil {
			return next, openErr
		}
		var current config.EnterpriseConnections
		if json.Unmarshal(plain, &current) != nil || current.Revision != previous.Revision {
			return next, ErrConflict
		}
	} else if errors.Is(err, sql.ErrNoRows) {
		return next, ErrConflict
	}
	if err != nil {
		return next, err
	}
	next.Revision = previous.Revision + 1
	plain, err := json.Marshal(next)
	if err != nil {
		return next, err
	}
	sealed, err := s.seal(enterpriseKey, plain)
	if err != nil {
		return next, err
	}
	var result sql.Result
	if len(old) == 0 {
		result, err = tx.ExecContext(ctx, "INSERT INTO metadata(key,value) VALUES(?,?) ON CONFLICT(key) DO NOTHING", enterpriseKey, sealed)
	} else {
		result, err = tx.ExecContext(ctx, "UPDATE metadata SET value=? WHERE key=? AND value=?", sealed, enterpriseKey, old)
	}
	if err != nil {
		return next, err
	}
	if count, err := result.RowsAffected(); err != nil || count != 1 {
		return next, ErrConflict
	}
	providers := map[string]bool{}
	if !reflect.DeepEqual(previous.OIDC, next.OIDC) {
		providers[previous.OIDC.Provider.Namespace()] = true
		providers[next.OIDC.Provider.Namespace()] = true
	}
	if previous.LDAP != next.LDAP {
		providers[previous.LDAP.Namespace()] = true
		providers[next.LDAP.Namespace()] = true
	}
	rows, err := tx.QueryContext(ctx, "SELECT data FROM identities WHERE kind='user'")
	if err != nil {
		return next, err
	}
	users := []Identity{}
	for rows.Next() {
		var data []byte
		var user Identity
		if err = rows.Scan(&data); err == nil {
			err = json.Unmarshal(data, &user)
		}
		if err != nil {
			break
		}
		if providers[user.Provider] {
			users = append(users, user)
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return next, err
	}
	ids := make([]string, 0, len(users))
	for _, user := range users {
		user.CredentialVersion++
		if _, err = saveIdentity(ctx, tx, user, "identity_provider_changed"); err != nil {
			return next, err
		}
		ids = append(ids, user.ID)
	}
	if err = revokeIdentitySessions(ctx, tx, ids...); err != nil {
		return next, err
	}
	if err = insertSourceEvent(ctx, tx, "identity_provider", "enterprise", "identity_connections_updated", true, "", next.Revision, time.Now().UTC()); err == nil {
		err = tx.Commit()
	}
	if err == nil {
		s.cancelIdentityCalls(ctx)
	}
	return next, err
}
