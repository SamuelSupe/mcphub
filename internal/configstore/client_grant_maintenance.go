package configstore

import (
	"context"
	"slices"
	"time"
)

func (s *Store) ValidateClientGrant(ctx context.Context, g ClientGrant) error {
	current, err := s.GetClientGrant(ctx, g.GrantID, g.Issuer, g.Subject)
	if err != nil {
		return err
	}
	if current.GrantBinding != g.GrantBinding {
		return ErrGrantReconfirmation
	}
	return s.checkGrant(ctx, current)
}

func (s *Store) invalidateClientGrants(ctx context.Context, tx *transaction, endpoint string, tools ...string) ([]string, error) {
	rows, err := tx.QueryContext(ctx, "SELECT id,status,revision,data FROM client_grants WHERE endpoint_id=? AND status IN ('pending','confirmed','active')", endpoint)
	if err != nil {
		return nil, err
	}
	var grants []ClientGrant
	for rows.Next() {
		g, err := s.scanGrant(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		grants = append(grants, g)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, g := range grants {
		if len(tools) > 0 && !slices.ContainsFunc(tools, func(name string) bool { return slices.Contains(g.AllowedTools, name) }) {
			continue
		}
		if _, err := tx.ExecContext(ctx, "UPDATE client_grants SET status='reconfirmation_required',revision=revision+1,exchange_hash=NULL WHERE id=?", g.GrantID); err != nil {
			return nil, err
		}
		g.Status, g.Revision = "reconfirmation_required", g.Revision+1
		if err := s.grantEvent(ctx, tx, g, "client_authorization_reconfirmation_required"); err != nil {
			return nil, err
		}
		ids = append(ids, g.GrantID)
	}
	return ids, nil
}

func (s *Store) MaintainClientGrants(ctx context.Context, retention time.Duration) error {
	s.grantMu.Lock()
	defer s.grantMu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now()
	rows, err := tx.QueryContext(ctx, "SELECT id,status,revision,data FROM client_grants WHERE status IN ('pending','confirmed','active') AND (expires_at<=? OR (status IN ('pending','confirmed') AND request_expires_at<=?)) LIMIT 256", now.UnixMilli(), now.UnixMilli())
	if err != nil {
		return err
	}
	var grants []ClientGrant
	for rows.Next() {
		g, err := s.scanGrant(rows)
		if err != nil {
			rows.Close()
			return err
		}
		grants = append(grants, g)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, g := range grants {
		if _, err := tx.ExecContext(ctx, "UPDATE client_grants SET status='expired',revision=revision+1,exchange_hash=NULL WHERE id=?", g.GrantID); err != nil {
			return err
		}
		g.Status, g.Revision = "expired", g.Revision+1
		if err := s.grantEvent(ctx, tx, g, "client_authorization_expired"); err != nil {
			return err
		}
	}
	if retention < 24*time.Hour {
		retention = 24 * time.Hour
	}
	cutoff := now.Add(-retention).UnixMilli()
	if _, err := tx.ExecContext(ctx, "DELETE FROM client_grants WHERE expires_at<? AND status NOT IN ('pending','confirmed','active')", cutoff); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM broker_sessions WHERE expires_at<? AND id NOT IN (SELECT session_id FROM client_grants)", cutoff); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM client_authorization_events WHERE created_at<? AND id NOT IN (SELECT event_id FROM approval_deliveries WHERE archive_due<>0)", cutoff); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	for _, g := range grants {
		s.cancelGrantLocked(g.GrantID)
	}
	return nil
}

func (s *Store) RecordClientGrantDenial(ctx context.Context, g ClientGrant, action string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := s.grantEvent(ctx, tx, g, action); err != nil {
		return err
	}
	return tx.Commit()
}
