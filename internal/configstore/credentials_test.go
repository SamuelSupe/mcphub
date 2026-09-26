package configstore

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SamuelSupe/mcphub/v2/internal/config"
)

func TestCredentialBindingLifecycle(t *testing.T) {
	s, _, _ := newTestStore(t)
	testCredentialBindingLifecycle(t, s)
}

func testCredentialBindingLifecycle(t *testing.T, s *Store) {
	t.Helper()
	ctx := t.Context()
	// A schema-7 client must keep its authorization when Vault is not enabled.
	legacy, err := s.Create(ctx, Record{Enabled: true, Config: config.BackendConfig{ID: "vault-legacy", URL: "https://legacy.example/mcp", PublishedTools: []string{"read"}}})
	if err != nil {
		t.Fatal(err)
	}
	_, legacyPolicy, err := s.ClientEndpointPolicy(ctx, legacy.Config.ID)
	if err != nil || legacyPolicy != "afb8d5e7d81b91e9e1a569f49576e4ef27ad863b12fe588cdc9eada466220b5b" {
		t.Fatal("upgrade changed a legacy client grant policy", err)
	}
	r, err := s.Create(ctx, Record{Enabled: true, Config: config.BackendConfig{ID: "vault-personal", URL: "https://backend.example/mcp", PublishedTools: []string{"read"}, Credentials: &config.CredentialConfig{Mode: "personal", Header: "Authorization", Scheme: "Bearer"}}})
	if err != nil {
		t.Fatal(err)
	}
	uid, policy, err := s.ClientEndpointPolicy(ctx, r.Config.ID)
	if err != nil {
		t.Fatal(err)
	}
	grant := func(subject string) ClientGrant {
		t.Helper()
		g, exchange, _, err := s.CreateClientGrant(ctx, ClientGrant{GrantBinding: GrantBinding{ClientID: "client_instance_" + subject, EndpointUID: uid}, Issuer: "https://id.example", Subject: subject, Resource: "https://hub.example/mcp", EndpointID: r.Config.ID, EndpointPolicy: policy, ClientName: "Editor", AllowedTools: []string{"read"}, Capabilities: GrantCapabilities{Tools: true}}, "", time.Hour, 8*time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		if err := s.DecideClientGrant(ctx, g.GrantID, g.Issuer, g.Subject, true); err != nil {
			t.Fatal(err)
		}
		g, _, err = s.ExchangeClientGrant(ctx, g.GrantID, g.Issuer, g.Subject, exchange)
		if err != nil {
			t.Fatal(err)
		}
		return g
	}
	alice, bob := grant("alice"), grant("bob")
	b := CredentialBinding{ID: "cr_initial", Issuer: alice.Issuer, Subject: alice.Subject, EndpointID: r.Config.ID, EndpointUID: uid, Policy: config.CredentialPolicy(r.Config.URL, r.Config.Credentials), Connected: true, Account: "Alice"}
	if err := s.ScheduleCredentialCleanup(ctx, b.ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	b, err = s.SaveCredentialBinding(ctx, b, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ValidateClientGrant(ctx, alice); err != nil {
		t.Fatal("initial account connection invalidated consent", err)
	}
	if ids, err := s.CredentialCleanup(ctx); err != nil || len(ids) != 0 {
		t.Fatal("connected secret still scheduled for deletion", err)
	}
	for _, owner := range [][2]string{{alice.Issuer, "bob"}, {"https://other.example", "alice"}} {
		if list, err := s.ListCredentialBindings(ctx, owner[0], owner[1]); err != nil || len(list) != 0 {
			t.Fatal("credential owner isolation failed", err)
		}
	}
	call, release, err := s.AdmitClientGrant(ctx, alice.GrantBinding, alice.Issuer, alice.Subject)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	b.ID = "cr_replaced"
	var won atomic.Int32
	var wg sync.WaitGroup
	for range 6 {
		wg.Go(func() {
			if _, err := s.SaveCredentialBinding(ctx, b, b.Revision); err == nil {
				won.Add(1)
			} else if !errors.Is(err, ErrConflict) {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if won.Load() != 1 {
		t.Fatal("competing account replacements were not serialized")
	}
	select {
	case <-call.Done():
	case <-time.After(time.Second):
		t.Fatal("account replacement did not cancel existing call")
	}
	if err := s.ValidateClientGrant(ctx, alice); err == nil {
		t.Fatal("old client grant silently transferred to new account")
	}
	if err := s.ValidateClientGrant(ctx, bob); err != nil {
		t.Fatal("replacement affected another user", err)
	}
	ids, err := s.CredentialCleanup(ctx)
	if err != nil || len(ids) != 1 || ids[0] != "cr_initial" {
		t.Fatal("old secret was not queued for durable cleanup", err)
	}
	b, err = s.CredentialBinding(ctx, alice.Issuer, alice.Subject, uid)
	if err != nil {
		t.Fatal(err)
	}
	b.Connected = false
	if _, err := s.SaveCredentialBinding(ctx, b, b.Revision); err != nil {
		t.Fatal(err)
	}
	b.Connected = true
	if _, err := s.SaveCredentialBinding(ctx, b, b.Revision); !errors.Is(err, ErrConflict) {
		t.Fatal("stale login undid disconnection", err)
	}
	if other, err := s.CredentialBinding(ctx, alice.Issuer, alice.Subject, "recreated-endpoint"); err != nil || other.Connected {
		t.Fatal("recreated endpoint inherited credentials", err)
	}
}
