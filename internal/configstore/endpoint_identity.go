package configstore

import (
	"context"
	"crypto/rand"
	"encoding/json"
)

func policyHash(value any) string {
	data, _ := json.Marshal(value)
	return SecretHash(string(data))
}

// Backfill identity without changing configuration revisions or secret AAD.
// Subsequent creates allocate a new UID; updates retain the existing one.
func (s *Store) ensureEndpointUIDs(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, table := range []string{"backends", "tool_groups"} {
		rows, err := tx.QueryContext(ctx, "SELECT id,config_json FROM "+table)
		if err != nil {
			return err
		}
		updates := map[string][]byte{}
		for rows.Next() {
			var id string
			var data []byte
			if err := rows.Scan(&id, &data); err != nil {
				rows.Close()
				return err
			}
			var value map[string]json.RawMessage
			if err := json.Unmarshal(data, &value); err != nil {
				rows.Close()
				return err
			}
			var uid string
			_ = json.Unmarshal(value["endpoint_uid"], &uid)
			if uid != "" {
				continue
			}
			value["endpoint_uid"], _ = json.Marshal("ep_" + rand.Text())
			updates[id], err = json.Marshal(value)
			if err != nil {
				rows.Close()
				return err
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		for id, data := range updates {
			if _, err := tx.ExecContext(ctx, "UPDATE "+table+" SET config_json=? WHERE id=?", data, id); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}
