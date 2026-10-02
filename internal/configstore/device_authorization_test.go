package configstore

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/SamuelSupe/mcphub/v2/internal/config"
)

func deviceFixture(t *testing.T, s *Store, provider string) (DeviceAuthorization, string, ClientGrant, EffectiveIdentity, Identity) {
	t.Helper()
	ctx := t.Context()
	user, err := s.SyncIdentity(ctx, provider, "device-test-user", "Device user", nil, nil, false, false)
	if err != nil {
		t.Fatal(err)
	}
	group, err := s.CreateGroup(ctx, provider, "Readers")
	if err != nil {
		t.Fatal(err)
	}
	group, err = s.UpdateIdentity(ctx, group.ID, group.Revision, true, config.IdentityPermissions{Scopes: []string{"mcp:device"}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.UpdateUser(ctx, user.ID, user.Revision, true, []string{group.ID})
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.EffectiveIdentity(ctx, user.ID)
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.Create(ctx, Record{Enabled: true, Config: config.BackendConfig{ID: "device", URL: "https://device.example/mcp", PublishedTools: []string{"read"}}})
	if err != nil {
		t.Fatal(err)
	}
	uid, policy, err := s.ClientEndpointPolicy(ctx, b.Config.ID)
	if err != nil {
		t.Fatal(err)
	}
	g := ClientGrant{GrantBinding: GrantBinding{ClientID: "ci_device_instance", EndpointUID: uid}, Issuer: "https://hub.example/sso", Subject: p.ID, Resource: "https://hub.example/mcp", EndpointID: b.Config.ID, EndpointPolicy: policy, ClientName: "Test Agent", AllowedScopes: []string{"mcp:device"}, AllowedTools: []string{"read"}, Capabilities: GrantCapabilities{Tools: true}}
	d, code, err := s.CreateDeviceAuthorization(ctx, DeviceAuthorization{ClientID: "mcpbridge", Resource: g.Resource, Scopes: []string{"mcp:device", "offline_access"}, TTLSeconds: 3600, Request: ClientGrant{GrantBinding: GrantBinding{ClientID: g.ClientID}, ClientName: g.ClientName}}, "192.0.2.1")
	if err != nil {
		t.Fatal(err)
	}
	return d, code, g, p, group
}
func TestDeviceAuthorizationSingleDelivery(t *testing.T) {
	s, key, path := newTestStore(t)
	peer, err := Open(t.Context(), path, key)
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	testDeviceAuthorizationSingleDelivery(t, s, peer)
}
func testDeviceAuthorizationSingleDelivery(t *testing.T, s *Store, peer *Store) {
	ctx := t.Context()
	var beforeSessions int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM sso_sessions").Scan(&beforeSessions); err != nil {
		t.Fatal(err)
	}
	d, code, g, p, _ := deviceFixture(t, s, config.LocalIdentityProvider)
	if _, err := s.DeviceByUserCode(ctx, "WRONG-CODE", "192.0.2.1"); !errors.Is(err, DeviceInvalid) {
		t.Fatal(err)
	}
	found, err := s.DeviceByUserCode(ctx, d.UserCode, "192.0.2.1")
	if err != nil || found.ID != d.ID {
		t.Fatal(err)
	}
	// The guessing limit is shared and atomic across processes, including simultaneous valid lookups.
	var lookups sync.WaitGroup
	lookupResults := make(chan error, 32)
	for i := range 32 {
		target := s
		if i%2 == 1 {
			target = peer
		}
		lookups.Go(func() { _, e := target.DeviceByUserCode(ctx, d.UserCode, "192.0.2.9"); lookupResults <- e })
	}
	lookups.Wait()
	close(lookupResults)
	accepted := 0
	for e := range lookupResults {
		if e == nil {
			accepted++
		} else if !errors.Is(e, DeviceLimited) {
			t.Fatal("concurrent code lookup", e)
		}
	}
	if accepted != 20 {
		t.Fatalf("concurrent code lookups accepted %d, want 20", accepted)
	}
	if _, err = s.PollDeviceAuthorization(ctx, code, "another-client", d.Resource); !errors.Is(err, DeviceInvalid) {
		t.Fatal("client binding", err)
	}
	if _, err = s.PollDeviceAuthorization(ctx, code, d.ClientID, d.Resource); !errors.Is(err, DeviceSlowDown) {
		t.Fatal("poll interval", err)
	}
	_, err = s.db.ExecContext(ctx, "UPDATE device_authorizations SET next_poll=0 WHERE id=?", d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.PollDeviceAuthorization(ctx, code, d.ClientID, d.Resource); !errors.Is(err, DevicePending) {
		t.Fatal("pending returned credentials", err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 8)
	for i := range 8 {
		target := s
		if i%2 == 1 {
			target = peer
		}
		wg.Go(func() {
			_, e := target.ConfirmDeviceAuthorization(ctx, d.ID, g, p, 0, time.Hour, 8*time.Hour)
			results <- e
		})
	}
	wg.Wait()
	close(results)
	success := 0
	for e := range results {
		if e == nil {
			success++
		}
	}
	if success != 1 {
		t.Fatalf("confirm successes %d", success)
	}
	var grants int
	if err = s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM client_grants WHERE client_id='ci_device_instance'").Scan(&grants); err != nil || grants != 1 {
		t.Fatal("orphan grant", grants, err)
	}
	type delivery struct {
		d DeviceAuthorization
		s SSOSession
		r string
		e error
	}
	deliveries := make(chan delivery, 8)
	for i := range 8 {
		target := s
		if i%2 == 1 {
			target = peer
		}
		wg.Go(func() {
			value, session, refresh, e := target.RedeemDeviceAuthorization(ctx, d.ID, d.ClientID)
			deliveries <- delivery{value, session, refresh, e}
		})
	}
	wg.Wait()
	close(deliveries)
	success = 0
	var got delivery
	for v := range deliveries {
		if v.e == nil {
			success++
			got = v
		}
	}
	if success != 1 {
		t.Fatalf("redeem successes %d", success)
	}
	if got.d.Exchange == "" || got.d.Proof == "" || got.r == "" {
		t.Fatal("incomplete private delivery")
	}
	var count int
	if err = s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM sso_sessions").Scan(&count); err != nil || count != beforeSessions+1 {
		t.Fatal("duplicate SSO family", count, err)
	}
	stored, err := s.readDevice(ctx, s.db, "id=?", d.ID)
	if err != nil || stored.Exchange != "" || stored.Proof != "" {
		t.Fatal("redeemed secrets retained", err)
	}
	active, secret, err := s.ExchangeClientGrant(ctx, got.d.Request.GrantID, g.Issuer, g.Subject, got.d.Exchange)
	if err != nil || secret == "" || active.Status != "active" {
		t.Fatal("exchange", err)
	}
	if _, _, err = s.ExchangeClientGrant(ctx, active.GrantID, g.Issuer, g.Subject, got.d.Exchange); err == nil {
		t.Fatal("repeated exchange")
	}
	if err = s.CancelDeviceAuthorization(ctx, code, d.ClientID); !errors.Is(err, DeviceInvalid) {
		t.Fatal("cancel revoked delivered connection", err)
	}
	if err = s.RevokeClientGrant(ctx, active.GrantID, g.Issuer, g.Subject); err != nil {
		t.Fatal(err)
	}
	if err = s.CancelDeviceAuthorization(ctx, code, d.ClientID); !errors.Is(err, DeviceInvalid) {
		t.Fatal("completed pairing retained cancellation authority", err)
	}
	if _, err = s.SSOSession(ctx, got.s.ID); err != nil {
		t.Fatal("old pairing deleted delivered token family", err)
	}
}
func TestDevicePermissionChangeAndLostDelivery(t *testing.T) {
	for _, phase := range []string{"confirm", "redeem", "exchange", "lost", "disabled", "policy", "enterprise", "grant-expired", "broker-expired"} {
		t.Run(phase, func(t *testing.T) {
			s, _, _ := newTestStore(t)
			ctx := t.Context()
			provider := config.LocalIdentityProvider
			var connections config.EnterpriseConnections
			if phase == "enterprise" {
				var err error
				connections, err = s.SaveEnterpriseConnections(ctx, config.EnterpriseConnections{}, config.EnterpriseConnections{LDAP: config.LDAPConnection{Enabled: true, URL: "ldaps://device.example"}})
				if err != nil {
					t.Fatal(err)
				}
				provider = connections.LDAP.Namespace()
			}
			d, code, g, p, group := deviceFixture(t, s, provider)
			if phase != "confirm" {
				var err error
				g, err = s.ConfirmDeviceAuthorization(ctx, d.ID, g, p, connections.Revision, time.Hour, 8*time.Hour)
				if err != nil {
					t.Fatal(err)
				}
			}
			var delivered DeviceAuthorization
			if phase == "exchange" || phase == "lost" {
				var err error
				delivered, _, _, err = s.RedeemDeviceAuthorization(ctx, d.ID, d.ClientID)
				if err != nil {
					t.Fatal(err)
				}
			}
			if phase == "lost" {
				if err := s.CancelDeviceAuthorization(ctx, code, d.ClientID); err != nil {
					t.Fatal(err)
				}
				var n int
				_ = s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM sso_sessions").Scan(&n)
				if n != 0 {
					t.Fatal("lost session family retained")
				}
				return
			}
			switch phase {
			case "grant-expired":
				if _, err := s.db.ExecContext(ctx, "UPDATE client_grants SET expires_at=? WHERE id=?", time.Now().Add(-time.Second).UnixMilli(), g.GrantID); err != nil {
					t.Fatal(err)
				}
			case "broker-expired":
				if _, err := s.db.ExecContext(ctx, "UPDATE broker_sessions SET expires_at=? WHERE id=?", time.Now().Add(-time.Second).UnixMilli(), g.SessionID); err != nil {
					t.Fatal(err)
				}
			case "disabled":
				user, err := s.Identity(ctx, p.ID)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = s.UpdateUser(ctx, user.ID, user.Revision, false, user.Groups); err != nil {
					t.Fatal(err)
				}
			case "policy":
				backend, err := s.Get(ctx, g.EndpointID)
				if err != nil {
					t.Fatal(err)
				}
				backend.Config.PublishedTools = []string{"new-tool"}
				if _, err = s.Update(ctx, backend, backend.Revision); err != nil {
					t.Fatal(err)
				}
			case "enterprise":
				next := connections
				next.LDAP.Enabled = false
				if _, err := s.SaveEnterpriseConnections(ctx, connections, next); err != nil {
					t.Fatal(err)
				}
			default:
				if _, err := s.UpdateIdentity(ctx, group.ID, group.Revision, true, config.IdentityPermissions{}); err != nil {
					t.Fatal(err)
				}
			}
			switch phase {
			case "confirm":
				if _, err := s.ConfirmDeviceAuthorization(ctx, d.ID, g, p, 0, time.Hour, 8*time.Hour); err == nil {
					t.Fatal("stale group consent")
				}
			case "redeem", "disabled", "policy", "enterprise", "grant-expired", "broker-expired":
				if _, _, _, err := s.RedeemDeviceAuthorization(ctx, d.ID, d.ClientID); err == nil {
					t.Fatal("invalid consent returned credentials")
				}
			case "exchange":
				if _, _, err := s.ExchangeClientGrant(ctx, g.GrantID, g.Issuer, g.Subject, delivered.Exchange); err == nil {
					t.Fatal("stale group exchange")
				}
			}
		})
	}
}

func TestDeviceExpiryRatesAndRestart(t *testing.T) {
	s, key, path := newTestStore(t)
	ctx := t.Context()
	d, code, g, p, _ := deviceFixture(t, s, config.LocalIdentityProvider)
	// Pending requests and their hashed capability survive opening another process.
	if _, err := s.ConfirmDeviceAuthorization(ctx, d.ID, g, p, 0, time.Hour, 8*time.Hour); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err := Open(ctx, path, key)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	restored, err := s.DeviceByUserCode(ctx, d.UserCode, "192.0.2.2")
	if err != nil || restored.Status != "approved" {
		t.Fatal("approved request lost on restart", err)
	}
	if _, _, _, err = s.RedeemDeviceAuthorization(ctx, d.ID, d.ClientID); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(ctx, path, key)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, _, _, err = s.RedeemDeviceAuthorization(ctx, d.ID, d.ClientID); !errors.Is(err, DeviceInvalid) {
		t.Fatal("restart allowed a second token family", err)
	}
	if _, err = s.PollDeviceAuthorization(ctx, code, d.ClientID, d.Resource); !errors.Is(err, DeviceInvalid) {
		t.Fatal("restart allowed capability replay", err)
	}
	for range 20 {
		_, _ = s.DeviceByUserCode(ctx, "WRONG-CODE", "192.0.2.3")
	}
	if _, err = s.DeviceByUserCode(ctx, d.UserCode, "192.0.2.3"); !errors.Is(err, DeviceLimited) {
		t.Fatal("code brute force was not limited", err)
	}
	for range 10 {
		if _, _, err = s.CreateDeviceAuthorization(ctx, DeviceAuthorization{ClientID: "another-client"}, "192.0.2.4"); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err = s.CreateDeviceAuthorization(ctx, DeviceAuthorization{ClientID: "another-client"}, "192.0.2.4"); !errors.Is(err, DeviceLimited) {
		t.Fatal("source request rate", err)
	}
	expired, expiredCode, err := s.CreateDeviceAuthorization(ctx, DeviceAuthorization{ClientID: "mcpbridge", Resource: g.Resource, Request: g}, "192.0.2.5")
	if err != nil {
		t.Fatal(err)
	}
	expired.ExpiresAt = time.Now().Add(-time.Second)
	data, err := s.deviceData(expired)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.ExecContext(ctx, "UPDATE device_authorizations SET data=?,expires_at=? WHERE id=?", data, expired.ExpiresAt.UnixMilli(), expired.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.PollDeviceAuthorization(ctx, expiredCode, expired.ClientID, expired.Resource); !errors.Is(err, DeviceExpired) {
		t.Fatal("expired request returned credentials", err)
	}
	if _, err = s.ConfirmDeviceAuthorization(ctx, expired.ID, g, p, 0, time.Hour, 8*time.Hour); !errors.Is(err, DeviceExpired) {
		t.Fatal("expired consent", err)
	}
	if err = s.MaintainClientGrants(ctx, time.Hour); err != nil {
		t.Fatal(err)
	}
}
