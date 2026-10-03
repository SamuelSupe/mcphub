package configstore

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/SamuelSupe/mcphub/v2/internal/config"
)

type ChangeImpact struct {
	PermissionGroups int  `json:"permission_groups"`
	Users            int  `json:"users"`
	Sessions         int  `json:"sessions"`
	Grants           int  `json:"grants"`
	Restart          bool `json:"restart_required"`
}

type ConfigurationDraft struct {
	CredentialsChanged bool            `json:"credentials_changed"`
	ID                 string          `json:"id"`
	Revision           int64           `json:"revision"`
	Kind               string          `json:"kind"`
	Target             string          `json:"target"`
	State              string          `json:"state"`
	Actor              string          `json:"actor"`
	ApprovalID         string          `json:"approval_id,omitempty"`
	Error              string          `json:"error,omitempty"`
	Before             json.RawMessage `json:"before"`
	After              json.RawMessage `json:"after"`
	Impact             ChangeImpact    `json:"impact"`
	UpdatedAt          time.Time       `json:"updated_at"`
	Change             json.RawMessage `json:"-"`
	Previous           json.RawMessage `json:"-"`
}

type storedDraft struct {
	Draft    ConfigurationDraft `json:"draft"`
	Change   json.RawMessage    `json:"change"`
	Previous json.RawMessage    `json:"previous"`
}

func (s *Store) ConfigurationDraft(ctx context.Context, id string) (ConfigurationDraft, error) {
	if !strings.HasPrefix(id, "chg_") || len(id) > 128 {
		return ConfigurationDraft{}, ErrNotFound
	}
	var raw []byte
	err := s.db.QueryRowContext(ctx, "SELECT value FROM metadata WHERE key=?", "configuration_draft:"+id).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return ConfigurationDraft{}, ErrNotFound
	}
	if err != nil {
		return ConfigurationDraft{}, err
	}
	raw, err = s.open("configuration_draft:"+id, raw)
	if err != nil {
		return ConfigurationDraft{}, err
	}
	var stored storedDraft
	err = json.Unmarshal(raw, &stored)
	stored.Draft.Change, stored.Draft.Previous = stored.Change, stored.Previous
	return stored.Draft, err
}

func (s *Store) ConfigurationDrafts(ctx context.Context) ([]ConfigurationDraft, error) {
	rows, err := s.db.QueryContext(ctx, "SELECT key FROM metadata WHERE key LIKE 'configuration_draft:%'")
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for rows.Next() {
		var key string
		if err = rows.Scan(&key); err != nil {
			break
		}
		ids = append(ids, strings.TrimPrefix(key, "configuration_draft:"))
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return nil, err
	}
	result := []ConfigurationDraft{}
	for _, id := range ids {
		draft, err := s.ConfigurationDraft(ctx, id)
		if err != nil {
			return nil, err
		}
		result = append(result, draft)
	}
	slices.SortFunc(result, func(a, b ConfigurationDraft) int { return b.UpdatedAt.Compare(a.UpdatedAt) })
	return result, nil
}

func (s *Store) SaveConfigurationDraft(ctx context.Context, draft ConfigurationDraft, expected int64) (ConfigurationDraft, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return draft, err
	}
	defer tx.Rollback()
	if expected == 0 {
		var count int
		if err = tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM metadata WHERE key LIKE 'configuration_draft:%'").Scan(&count); err != nil {
			return draft, err
		}
		if count >= 200 {
			return draft, errors.New("configuration history limit reached; export and remove completed changes")
		}
		draft.ID = "chg_" + rand.Text()
		draft.Actor = Actor(ctx)
	} else {
		var raw []byte
		if err = tx.QueryRowContext(ctx, "SELECT value FROM metadata WHERE key=?", "configuration_draft:"+draft.ID).Scan(&raw); err != nil {
			return draft, err
		}
		raw, err = s.open("configuration_draft:"+draft.ID, raw)
		if err != nil {
			return draft, err
		}
		var old storedDraft
		if err = json.Unmarshal(raw, &old); err != nil {
			return draft, err
		}
		if old.Draft.Revision != expected {
			return draft, ErrConflict
		}
	}
	draft.Revision = expected + 1
	draft.UpdatedAt = time.Now().UTC()
	raw, err := json.Marshal(storedDraft{draft, draft.Change, draft.Previous})
	if err != nil {
		return draft, err
	}
	raw, err = s.seal("configuration_draft:"+draft.ID, raw)
	if err != nil {
		return draft, err
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO metadata(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", "configuration_draft:"+draft.ID, raw); err != nil {
		return draft, err
	}
	if err = insertSourceEvent(ctx, tx, draft.Kind, draft.Target, "configuration_"+draft.State, true, "", draft.Revision, draft.UpdatedAt); err != nil {
		return draft, err
	}
	return draft, tx.Commit()
}

func (s *Store) DeleteConfigurationDraft(ctx context.Context, id string, expected int64) error {
	current, err := s.ConfigurationDraft(ctx, id)
	if err != nil {
		return err
	}
	if current.Revision != expected {
		return ErrConflict
	}
	if !slices.Contains([]string{"applied", "failed", "discarded"}, current.State) {
		return errors.New("only completed changes can be removed")
	}
	_, err = s.db.ExecContext(ctx, "DELETE FROM metadata WHERE key=?", "configuration_draft:"+id)
	return err
}

func (s *Store) ConfigurationImpact(ctx context.Context, endpoint string) (ChangeImpact, error) {
	impact := ChangeImpact{}
	identities, err := s.Identities(ctx)
	if err != nil {
		return impact, err
	}
	groups := map[string]bool{}
	groupRecords := map[string]Identity{}
	users := map[string]bool{}
	for _, p := range identities {
		if p.Kind != "user" {
			groupRecords[p.ID] = p
		}
		if p.Kind != "user" && slices.ContainsFunc(p.Permissions.Access, func(a config.IdentityAccess) bool { return a.EndpointID == endpoint }) {
			groups[p.ID] = true
		}
	}
	impact.PermissionGroups = len(groups)
	for _, p := range identities {
		if p.Kind != "user" {
			continue
		}
		sources, e := permissionSources(ctx, s.db, p, groupRecords)
		if e != nil {
			return impact, e
		}
		for _, source := range sources {
			if groups[source.Identity.ID] {
				users[p.ID] = true
				break
			}
		}
	}
	impact.Users = len(users)
	rows, err := s.db.QueryContext(ctx, "SELECT data FROM sso_sessions WHERE expires_at>?", time.Now().Unix())
	if err != nil {
		return impact, err
	}
	for rows.Next() {
		var raw []byte
		var session SSOSession
		if err = rows.Scan(&raw); err != nil {
			break
		}
		if err = json.Unmarshal(raw, &session); err != nil {
			break
		}
		if users[session.UserID] {
			impact.Sessions++
		}
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return impact, err
	}
	err = s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM client_grants WHERE LOWER(endpoint_id)=LOWER(?) AND status IN ('active','pending','confirmed') AND expires_at>?", endpoint, time.Now().UnixMilli()).Scan(&impact.Grants)
	if err != nil {
		return impact, err
	}
	return impact, nil
}
