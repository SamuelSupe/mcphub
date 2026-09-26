package configstore

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SamuelSupe/mcphub/v2/internal/config"
	"github.com/SamuelSupe/mcphub/v2/internal/httptool"
)

func TestClientGrantLifecycle(t *testing.T) {
	s, _, _ := newTestStore(t)
	testClientGrantLifecycle(t, s)
}

func TestClientGrantAdministratorQuery(t *testing.T) {
	s, _, _ := newTestStore(t)
	testClientGrantAdministratorQuery(t, s)
}

func testClientGrantAdministratorQuery(t *testing.T, s *Store) {
	t.Helper()
	ctx := t.Context()
	b, err := s.Create(ctx, Record{Enabled: true, Config: config.BackendConfig{ID: "query-grants", URL: "https://db.example/mcp", PublishedTools: []string{"query"}}})
	if err != nil {
		t.Fatal(err)
	}
	uid, policy, err := s.ClientEndpointPolicy(ctx, b.Config.ID)
	if err != nil {
		t.Fatal(err)
	}
	var created []ClientGrant
	for i, subject := range []string{"alice", "bob", "alice", "outsider"} {
		issuer := "https://query-id.example"
		if subject == "outsider" {
			issuer = "https://other-id.example"
		}
		input := ClientGrant{GrantBinding: GrantBinding{ClientID: fmt.Sprintf("ci_query_instance_%d", i), EndpointUID: uid}, Issuer: issuer, Subject: subject, Resource: "https://hub.example/mcp", ClientName: "Editor", EndpointID: b.Config.ID, EndpointPolicy: policy, AllowedTools: []string{"query"}, Capabilities: GrantCapabilities{Tools: true}}
		g, exchange, _, err := s.CreateClientGrant(ctx, input, "", time.Hour, 8*time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			if err := s.DecideClientGrant(ctx, g.GrantID, issuer, subject, true); err != nil {
				t.Fatal(err)
			}
			g, _, err = s.ExchangeClientGrant(ctx, g.GrantID, issuer, subject, exchange)
			if err != nil {
				t.Fatal(err)
			}
		}
		if i == 1 {
			if err := s.RevokeClientGrant(ctx, g.GrantID, issuer, subject); err != nil {
				t.Fatal(err)
			}
		}
		if i == 2 {
			if _, err := s.db.ExecContext(ctx, "UPDATE client_grants SET request_expires_at=? WHERE id=?", time.Now().Add(-time.Second).UnixMilli(), g.GrantID); err != nil {
				t.Fatal(err)
			}
		}
		created = append(created, g)
	}
	q := ClientGrantQuery{Issuer: created[0].Issuer, Limit: 1}
	seen := map[string]bool{}
	for {
		page, next, err := s.QueryClientGrants(ctx, q)
		if err != nil || len(page) != 1 {
			t.Fatalf("grant page: %+v %v", page, err)
		}
		if seen[page[0].GrantID] || page[0].Subject == "outsider" {
			t.Fatal("duplicate or cross-issuer grant")
		}
		seen[page[0].GrantID] = true
		if next == "" {
			break
		}
		q.Cursor = next
		if len(seen) > 3 {
			t.Fatal("pagination did not end")
		}
	}
	if len(seen) != 3 {
		t.Fatalf("pagination lost grants: %v", seen)
	}
	for i, status := range []string{"active", "revoked", "expired"} {
		page, next, err := s.QueryClientGrants(ctx, ClientGrantQuery{Issuer: q.Issuer, Endpoint: "QUERY-GRANTS", Status: status, Subject: created[i].Subject, ClientID: created[i].ClientID})
		if err != nil || len(page) != 1 || next != "" || page[0].GrantID != created[i].GrantID || page[0].Status != status {
			t.Fatalf("filter %s: %+v %v", status, page, err)
		}
	}
	if _, _, err := s.QueryClientGrants(ctx, ClientGrantQuery{Issuer: q.Issuer, Cursor: "broken"}); !errors.Is(err, ErrGrantCursor) {
		t.Fatalf("invalid cursor: %v", err)
	}
	personal, err := s.ListClientGrants(ctx, q.Issuer, "bob")
	if err != nil || len(personal) != 1 || personal[0].Subject != "bob" {
		t.Fatal("personal listing lost owner isolation", err)
	}
}

// Shared with the real PostgreSQL suite: races here must consume consent once,
// keep grant ownership, and never resurrect access when an ID is reused.
func testClientGrantLifecycle(t *testing.T, s *Store) {
	t.Helper()
	ctx := t.Context()
	public, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	s.ConfigureApprovalDelivery(config.ApprovalSettings{AuditArchive: config.ApprovalArchive{URL: "https://archive.example", KeyID: "grant-test", SigningKey: base64.StdEncoding.EncodeToString(key)}}, "")
	defer s.ConfigureApprovalDelivery(config.ApprovalSettings{}, "")
	var head deliveryHead
	var initial []byte
	if err := s.db.QueryRowContext(ctx, "SELECT value FROM metadata WHERE key='approval_delivery_head'").Scan(&initial); err == nil {
		plain, err := s.open("approval-delivery-head", initial)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(plain, &head); err != nil {
			t.Fatal(err)
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		t.Fatal(err)
	}
	b, err := s.Create(ctx, Record{Enabled: true, Config: config.BackendConfig{ID: "grant-db", URL: "https://db.example/mcp", PublishedTools: []string{"query"}}})
	if err != nil {
		t.Fatal(err)
	}
	uid, policy, err := s.ClientEndpointPolicy(ctx, b.Config.ID)
	if err != nil {
		t.Fatal(err)
	}
	input := ClientGrant{GrantBinding: GrantBinding{ClientID: "ci_test_instance_1", EndpointUID: uid}, Issuer: "https://id.example", Subject: "alice", Resource: "https://hub.example/mcp", ClientName: "Editor", EndpointID: b.Config.ID, EndpointPolicy: policy, AllowedScopes: []string{"db:read"}, AllowedTools: []string{"query"}, ResourceRules: []config.ResourceRule{{Argument: "/project", AllowedValues: []string{"private-project"}}}, Capabilities: GrantCapabilities{Tools: true}}
	g, exchange, proof, err := s.CreateClientGrant(ctx, input, "", time.Hour, 8*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.ExchangeClientGrant(ctx, g.GrantID, g.Issuer, g.Subject, exchange); err == nil {
		t.Fatal("unconfirmed grant exchanged")
	}
	if err := s.DecideClientGrant(ctx, g.GrantID, g.Issuer, "bob", true); err == nil {
		t.Fatal("another user confirmed")
	}
	var confirms atomic.Int32
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if s.DecideClientGrant(ctx, g.GrantID, g.Issuer, g.Subject, true) == nil {
				confirms.Add(1)
			}
		})
	}
	wg.Wait()
	if confirms.Load() != 1 {
		t.Fatalf("confirmations=%d", confirms.Load())
	}
	var exchanges atomic.Int32
	var secret string
	grantID, issuer, subject := g.GrantID, g.Issuer, g.Subject
	var mu sync.Mutex
	for range 8 {
		wg.Go(func() {
			value, credential, err := s.ExchangeClientGrant(ctx, grantID, issuer, subject, exchange)
			if err == nil {
				exchanges.Add(1)
				mu.Lock()
				g, secret = value, credential
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	if exchanges.Load() != 1 {
		t.Fatalf("exchanges=%d", exchanges.Load())
	}
	var sealed []byte
	if err := s.db.QueryRowContext(ctx, "SELECT data FROM client_grants WHERE id=?", g.GrantID).Scan(&sealed); err != nil || bytes.Contains(sealed, []byte("private-project")) || bytes.Contains(sealed, []byte(secret)) {
		t.Fatalf("encrypted grant: %v", err)
	}
	if _, err := s.AuthenticateClientGrant(ctx, g.GrantID, g.Issuer, g.Subject, g.Resource); err == nil {
		t.Fatal("grant ID authenticated as credential")
	}
	for _, owner := range []struct{ issuer, subject, resource string }{{g.Issuer, "bob", g.Resource}, {g.Issuer, g.Subject, "https://other.example/mcp"}, {"https://other.example", g.Subject, g.Resource}} {
		if _, err := s.AuthenticateClientGrant(ctx, secret, owner.issuer, owner.subject, owner.resource); err == nil {
			t.Fatal("cross-identity grant accepted")
		}
	}
	call, release, err := s.AdmitClientGrant(ctx, g.GrantBinding, g.Issuer, g.Subject)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if err := s.RevokeClientGrant(ctx, g.GrantID, g.Issuer, g.Subject); err != nil {
		t.Fatal(err)
	}
	select {
	case <-call.Done():
	case <-time.After(time.Second):
		t.Fatal("revocation did not cancel admitted work")
	}
	if _, err := s.AuthenticateClientGrant(ctx, secret, g.Issuer, g.Subject, g.Resource); !errors.Is(err, ErrGrantRevoked) {
		t.Fatalf("revoked grant: %v", err)
	}
	input.SessionID, input.ClientID = g.SessionID, "ci_test_instance_2"
	other, ex, _, err := s.CreateClientGrant(ctx, input, proof, time.Hour, 8*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DecideClientGrant(ctx, other.GrantID, other.Issuer, other.Subject, true); err != nil {
		t.Fatal(err)
	}
	other, credential, err := s.ExchangeClientGrant(ctx, other.GrantID, other.Issuer, other.Subject, ex)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AuthenticateClientGrant(ctx, credential, other.Issuer, other.Subject, other.Resource); err != nil {
		t.Fatal("revoking one entry affected another", err)
	}
	if err := s.Delete(ctx, b.Config.ID, b.Revision); err != nil {
		t.Fatal(err)
	}
	recreated, err := s.Create(ctx, b)
	if err != nil {
		t.Fatal(err)
	}
	if recreated.Config.EndpointUID == uid {
		t.Fatal("endpoint identity reused")
	}
	if _, err := s.AuthenticateClientGrant(ctx, credential, other.Issuer, other.Subject, other.Resource); !errors.Is(err, ErrGrantReconfirmation) {
		t.Fatalf("recreated endpoint revived grant: %v", err)
	}
	if err := s.RevokeBrokerSession(ctx, other.SessionID, other.Issuer, other.Subject, "wrong", false); err == nil {
		t.Fatal("session ID revoked without holder proof")
	}
	if err := s.RevokeBrokerSession(ctx, other.SessionID, other.Issuer, other.Subject, proof, false); err != nil {
		t.Fatal(err)
	}
	input.EndpointUID, input.EndpointPolicy, _ = s.ClientEndpointPolicy(ctx, b.Config.ID)
	if _, _, _, err := s.CreateClientGrant(ctx, input, proof, time.Hour, 8*time.Hour); err == nil {
		t.Fatal("revoked session revived")
	}
	input.SessionID = ""
	group, err := s.CreateToolGroup(ctx, ToolGroupRecord{Config: httptool.GroupConfig{ID: "grant-api", BaseURL: "https://api.example", Enabled: true}})
	if err != nil {
		t.Fatal(err)
	}
	tool, err := s.CreateHTTPTool(ctx, HTTPToolRecord{GroupID: group.Config.ID, Config: httptool.ToolConfig{Name: "read", Method: "GET", Path: "/v1/projects", Enabled: true}})
	if err != nil {
		t.Fatal(err)
	}
	httpInput := input
	httpInput.EndpointID = group.Config.ID
	httpInput.EndpointUID, httpInput.EndpointPolicy, _ = s.ClientEndpointPolicy(ctx, group.Config.ID)
	httpInput.AllowedTools = []string{"read"}
	httpGrant, httpExchange, _, err := s.CreateClientGrant(ctx, httpInput, "", time.Hour, 8*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DecideClientGrant(ctx, httpGrant.GrantID, httpGrant.Issuer, httpGrant.Subject, true); err != nil {
		t.Fatal(err)
	}
	httpGrant, httpSecret, err := s.ExchangeClientGrant(ctx, httpGrant.GrantID, httpGrant.Issuer, httpGrant.Subject, httpExchange)
	if err != nil {
		t.Fatal(err)
	}
	tool.Config.Description = "Clearer display text"
	tool, err = s.UpdateHTTPTool(ctx, tool, tool.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AuthenticateClientGrant(ctx, httpSecret, httpGrant.Issuer, httpGrant.Subject, httpGrant.Resource); err != nil {
		t.Fatal("display change invalidated consent", err)
	}
	tool.Config.Path = "/v2/different-target"
	if _, err := s.UpdateHTTPTool(ctx, tool, tool.Revision); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AuthenticateClientGrant(ctx, httpSecret, httpGrant.Issuer, httpGrant.Subject, httpGrant.Resource); !errors.Is(err, ErrGrantReconfirmation) {
		t.Fatal("changed HTTP target inherited consent", err)
	}
	expiring, _, _, err := s.CreateClientGrant(ctx, input, "", time.Hour, 8*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	expiring.RequestExpiresAt = time.Now().Add(-time.Minute)
	data, err := s.grantData(expiring)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, "UPDATE client_grants SET data=?,request_expires_at=? WHERE id=?", data, expiring.RequestExpiresAt.UnixMilli(), expiring.GrantID); err != nil {
		t.Fatal(err)
	}
	if err := s.DecideClientGrant(ctx, expiring.GrantID, expiring.Issuer, expiring.Subject, true); !errors.Is(err, ErrGrantExpired) {
		t.Fatalf("expired consent: %v", err)
	}
	if err := s.MaintainClientGrants(ctx, 24*time.Hour); err != nil {
		t.Fatal(err)
	}
	delivery, err := s.NextApprovalDelivery(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CompleteApprovalDelivery(ctx, delivery, true, false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.NextApprovalDelivery(ctx, true); !errors.Is(err, ErrNotFound) {
		t.Fatal("failed grant archive was skipped")
	}
	if _, err := s.db.ExecContext(ctx, "UPDATE approval_deliveries SET archive_due=1 WHERE archive_due>0"); err != nil {
		t.Fatal(err)
	}
	actions := map[string]bool{}
	for {
		value, err := s.NextApprovalDelivery(ctx, true)
		if errors.Is(err, ErrNotFound) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		var envelope AuditEnvelope
		if err := json.Unmarshal(value.Payload, &envelope); err != nil {
			t.Fatal(err)
		}
		entry, err := VerifyAuditEnvelope(envelope, map[string]ed25519.PublicKey{"grant-test": public}, head.Sequence, head.Hash)
		if err != nil {
			t.Fatal(err)
		}
		if entry.ObjectKind != "client_grant" || entry.Grant == nil || bytes.Contains(value.Payload, []byte(secret)) || bytes.Contains(value.Payload, []byte(proof)) {
			t.Fatal("grant archive lost binding or exposed credentials")
		}
		actions[entry.Action] = true
		head.Sequence, head.Hash = entry.Sequence, envelope.Hash
		if err := s.CompleteApprovalDelivery(ctx, value, true, true); err != nil {
			t.Fatal(err)
		}
	}
	for _, action := range []string{"client_authorization_requested", "client_authorization_confirmed", "client_authorization_activated", "client_authorization_revoked", "client_authorization_reconfirmation_required", "client_authorization_expired"} {
		if !actions[action] {
			t.Fatal("missing grant archive event", action)
		}
	}
}
