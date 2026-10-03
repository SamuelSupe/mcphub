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

type NativeClients struct {
	Revision int64              `json:"revision"`
	Clients  []config.SSOClient `json:"clients"`
}

func (s *Store) NativeClients(ctx context.Context) (NativeClients, error) {
	var data []byte
	err := s.db.QueryRowContext(ctx, "SELECT value FROM metadata WHERE key='native_clients'").Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return NativeClients{Clients: []config.SSOClient{}}, nil
	}
	var result NativeClients
	if err == nil {
		err = json.Unmarshal(data, &result)
	}
	return result, err
}

func (s *Store) SaveNativeClients(ctx context.Context, expected int64, clients []config.SSOClient) (NativeClients, error) {
	s.identityMu.Lock()
	defer s.identityMu.Unlock()
	current, err := s.NativeClients(ctx)
	if err != nil {
		return current, err
	}
	if current.Revision != expected {
		return current, ErrConflict
	}
	next := NativeClients{Revision: expected + 1, Clients: clients}
	data, err := json.Marshal(next)
	if err != nil {
		return current, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return current, err
	}
	defer tx.Rollback()
	affected := map[string]bool{}
	for _, old := range current.Clients {
		unchanged := false
		for _, client := range clients {
			if reflect.DeepEqual(old, client) {
				unchanged = true
				break
			}
		}
		if !unchanged {
			affected[old.ID] = true
		}
	}
	rows, err := tx.QueryContext(ctx, "SELECT id,data FROM sso_sessions")
	if err != nil {
		return current, err
	}
	var sessions []SSOSession
	for rows.Next() {
		var id string
		var raw []byte
		if err = rows.Scan(&id, &raw); err != nil {
			break
		}
		var session SSOSession
		if err = json.Unmarshal(raw, &session); err != nil {
			break
		}
		if affected[session.ClientID] {
			sessions = append(sessions, session)
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return current, err
	}
	for _, session := range sessions {
		for _, binding := range session.Grants {
			var g ClientGrant
			g, err = s.scanGrant(tx.QueryRowContext(ctx, "SELECT id,status,revision,data FROM client_grants WHERE id=?", binding.GrantID))
			if err != nil {
				return current, err
			}
			if err = s.revokeGrantTx(ctx, tx, g, "oauth_client_changed"); err != nil {
				return current, err
			}
		}
		if _, err = tx.ExecContext(ctx, "DELETE FROM sso_refresh WHERE session_id=?", session.ID); err != nil {
			return current, err
		}
		if _, err = tx.ExecContext(ctx, "DELETE FROM sso_sessions WHERE id=?", session.ID); err != nil {
			return current, err
		}
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO metadata(key,value) VALUES('native_clients',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", data)
	if err == nil {
		err = insertSourceEvent(ctx, tx, "oauth_clients", "native_clients", "oauth_clients_updated", true, "", next.Revision, time.Now().UTC())
	}
	if err == nil {
		err = tx.Commit()
	}
	if err == nil {
		s.grantMu.Lock()
		for _, session := range sessions {
			for _, binding := range session.Grants {
				s.cancelGrantLocked(binding.GrantID)
			}
		}
		s.grantMu.Unlock()
	}
	return next, err
}

// The caller has authenticated the browser owner and frozen the catalog policy.
// Identity admission and all service grants commit under one identity lock.
func (s *Store) CreateNativeConnection(ctx context.Context, identity EffectiveIdentity, grants []ClientGrant, ttl, maximum time.Duration) ([]GrantBinding, error) {
	if len(grants) == 0 || len(grants) > 16 || ttl <= 0 || ttl > maximum {
		return nil, ErrGrantInsufficient
	}
	s.identityMu.Lock()
	defer s.identityMu.Unlock()
	s.grantMu.Lock()
	defer s.grantMu.Unlock()
	current, err := s.effectiveIdentity(ctx, identity.ID)
	if err != nil || current.Version != identity.Version {
		return nil, ErrIdentityDenied
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	bindings := []GrantBinding{}
	var sessionID, proof string
	for _, grant := range grants {
		if grant.Subject != identity.ID || validateGrantOwner(grant) != nil {
			return nil, ErrGrantInvalid
		}
		uid, policy, err := s.clientEndpointPolicyFrom(ctx, tx, grant.EndpointID)
		if err != nil || uid != grant.EndpointUID || policy != grant.EndpointPolicy {
			return nil, ErrGrantReconfirmation
		}
		grant.SessionID = sessionID
		grant, _, proof, err = s.createClientGrantTx(ctx, tx, grant, proof, ttl, maximum)
		if err != nil {
			return nil, err
		}
		sessionID = grant.SessionID
		grant.Status, grant.ConfirmedBy, grant.ConfirmedAt = "active", identity.ID, time.Now().UTC()
		data, err := s.grantData(grant)
		if err != nil {
			return nil, err
		}
		_, err = tx.ExecContext(ctx, "UPDATE client_grants SET status='active',exchange_hash=NULL,data=? WHERE id=?", data, grant.GrantID)
		if err != nil {
			return nil, err
		}
		if err = s.grantEvent(ctx, tx, grant, "native_client_authorized"); err != nil {
			return nil, err
		}
		bindings = append(bindings, grant.GrantBinding)
	}
	return bindings, tx.Commit()
}

func (s *Store) NativeGrant(ctx context.Context, binding GrantBinding, issuer, subject, resource string) (ClientGrant, error) {
	grant, err := s.GetClientGrant(ctx, binding.GrantID, issuer, subject)
	if err != nil {
		return grant, err
	}
	if grant.GrantBinding != binding || grant.Resource != resource {
		return grant, ErrGrantInvalid
	}
	return grant, s.checkGrant(ctx, grant)
}
