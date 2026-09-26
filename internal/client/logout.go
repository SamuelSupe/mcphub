package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"slices"
	"time"
)

type pendingRevocation struct {
	SessionID string `json:"session_id"`
	ServerURL string `json:"server_url"`
}

func rememberRevocation(p, old *profile) {
	if old == nil {
		return
	}
	p.PendingRevocations = append(p.PendingRevocations, old.PendingRevocations...)
	if old.Broker != nil {
		p.PendingRevocations = append(p.PendingRevocations, pendingRevocation{old.Broker.SessionID, old.ServerURL})
	}
}

func revokeLoginSnapshot(ctx context.Context, p *profile, base *http.Client) error {
	if p == nil || p.Broker == nil {
		return nil
	}
	if p.Token == nil {
		return ErrLoginRequired
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	c := httpClient(base, 10*time.Second)
	if !time.Now().Before(p.Token.Expiry) {
		if p.Token.RefreshToken == "" {
			return ErrLoginRequired
		}
		token, err := refreshToken(ctx, p, c)
		if err != nil {
			return err
		}
		p.Token = token
	}
	target, err := url.Parse(p.ServerURL)
	if err != nil {
		return err
	}
	target.Path, target.RawPath, target.RawQuery = "/api/v1/broker-sessions/"+url.PathEscape(p.Broker.SessionID)+"/revoke", "", ""
	data, _ := json.Marshal(map[string]string{"broker_session_proof": p.Broker.Proof})
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, target.String(), bytes.NewReader(data))
	req.Header.Set("Authorization", "Bearer "+p.Token.AccessToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.Do(req)
	if err != nil {
		return errors.New("remote session revocation could not be confirmed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return errors.New("remote session revocation could not be confirmed")
	}
	return nil
}

func (s *Store) forgetRevocation(ctx context.Context, name string, old *profile) error {
	if old == nil || old.Broker == nil {
		return nil
	}
	return s.locked(ctx, name, func() error {
		p, err := s.load(name)
		if err != nil {
			return err
		}
		p.PendingRevocations = slices.DeleteFunc(p.PendingRevocations, func(v pendingRevocation) bool {
			return v.SessionID == old.Broker.SessionID && v.ServerURL == old.ServerURL
		})
		return s.save(name, p)
	})
}

// Logout clears local access before any network I/O. An offline revocation
// leaves only the non-secret session reference for review in the user portal.
func Logout(ctx context.Context, store *Store, name string, base *http.Client) (bool, error) {
	var previous *profile
	err := store.locked(ctx, name, func() error {
		p, err := store.load(name)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		previous = p
		cleared := *p
		cleared.Token, cleared.Broker, cleared.PendingRevocations = nil, nil, nil
		rememberRevocation(&cleared, p)
		return store.save(name, &cleared)
	})
	if err != nil {
		return false, err
	}
	if secret, err := store.brokerSecret(); err == nil {
		conn, _, _, err := ipcHandshake(ctx, store, brokerMessage{Operation: "disconnect", Profile: name, Secret: secret})
		if err == nil {
			conn.Close()
		}
	}
	if err := revokeLoginSnapshot(ctx, previous, base); err != nil {
		return false, nil
	}
	return true, store.forgetRevocation(ctx, name, previous)
}
