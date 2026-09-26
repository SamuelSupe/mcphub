package configstore

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

var ErrGrantCursor = errors.New("invalid client grant cursor")

type ClientGrantQuery struct {
	Issuer, Subject, ClientID, Endpoint, Status, Cursor string
	Limit                                               int
}

type grantCursor struct {
	Created int64  `json:"created"`
	ID      string `json:"id"`
}

// QueryClientGrants is for configuration administrators. User-facing callers
// must continue to use ListClientGrants with an authenticated issuer and subject.
func (s *Store) QueryClientGrants(ctx context.Context, q ClientGrantQuery) ([]ClientGrant, string, error) {
	if q.Limit < 1 || q.Limit > 100 {
		q.Limit = 25
	}
	now := time.Now().UnixMilli()
	status := `CASE
WHEN g.status IN ('active','confirmed') AND (g.expires_at<=? OR b.expires_at<=?) THEN 'expired'
WHEN g.status IN ('pending','confirmed') AND g.request_expires_at<=? THEN 'expired'
WHEN g.status IN ('active','confirmed') AND b.status<>'active' THEN 'revoked'
ELSE g.status END`
	query := `SELECT id,status,revision,data FROM (SELECT g.id,` + status + ` AS status,g.revision,g.data,g.created_at,g.issuer,g.subject,g.client_id,g.endpoint_id FROM client_grants g JOIN broker_sessions b ON b.id=g.session_id) grants WHERE issuer=?`
	args := []any{now, now, now, q.Issuer}
	for _, filter := range []struct{ column, value string }{{"subject", q.Subject}, {"client_id", q.ClientID}, {"endpoint_id", q.Endpoint}, {"status", q.Status}} {
		if filter.value != "" {
			if filter.column == "endpoint_id" {
				query += " AND LOWER(endpoint_id)=LOWER(?)"
			} else {
				query += " AND " + filter.column + "=?"
			}
			args = append(args, filter.value)
		}
	}
	if q.Cursor != "" {
		var cursor grantCursor
		data, err := base64.RawURLEncoding.DecodeString(q.Cursor)
		if err != nil || json.Unmarshal(data, &cursor) != nil || cursor.Created <= 0 || len(cursor.ID) < 4 || len(cursor.ID) > 128 {
			return nil, "", ErrGrantCursor
		}
		query += " AND (created_at<? OR (created_at=? AND id<?))"
		args = append(args, cursor.Created, cursor.Created, cursor.ID)
	}
	query += " ORDER BY created_at DESC,id DESC LIMIT ?"
	args = append(args, q.Limit+1)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, "", fmt.Errorf("query client grants: %w", err)
	}
	defer rows.Close()
	values := []ClientGrant{}
	for rows.Next() {
		g, err := s.scanGrant(rows)
		if err != nil {
			return nil, "", err
		}
		values = append(values, g)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(values) > q.Limit {
		values = values[:q.Limit]
		last := values[len(values)-1]
		data, _ := json.Marshal(grantCursor{Created: last.CreatedAt.UnixMilli(), ID: last.GrantID})
		next = base64.RawURLEncoding.EncodeToString(data)
	}
	return values, next, nil
}
