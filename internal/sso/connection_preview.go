package sso

import (
	"context"
	"crypto/rand"
	"errors"
	"net/http"
	"net/url"
	"time"

	"github.com/SamuelSupe/mcphub/v2/internal/config"
	"github.com/SamuelSupe/mcphub/v2/internal/configstore"
	"golang.org/x/oauth2"
)

// Connection tests verify candidate settings without provisioning identities,
// granting permissions, issuing MCPHub tokens or replacing the active source.
type ConnectionIdentity struct {
	Provider      string            `json:"provider"`
	Subject       string            `json:"subject"`
	Name          string            `json:"name"`
	Groups        []string          `json:"groups"`
	Departments   []string          `json:"departments"`
	GroupNames    map[string]string `json:"group_names,omitempty"`
	MissingClaims []string          `json:"missing_claims,omitempty"`
}

type ConnectionTestResult struct {
	ID        string              `json:"id"`
	Source    string              `json:"source"`
	Status    string              `json:"status"`
	StartURL  string              `json:"start_url,omitempty"`
	ExpiresAt time.Time           `json:"expires_at"`
	Identity  *ConnectionIdentity `json:"identity,omitempty"`
	Error     string              `json:"error,omitempty"`
}

type connectionTest struct {
	owner    string
	revision int64
	login    login
	result   ConnectionTestResult
}

func (s *Server) TestConnection(ctx context.Context, next config.EnterpriseConnections, source, owner, username, password, address string) (ConnectionTestResult, error) {
	if !s.Builtin() || owner == "" {
		return ConnectionTestResult{}, errors.New("connection tests require a built-in recovery administrator")
	}
	switch source {
	case "oidc":
		next.LDAP.Enabled = false
		if !next.OIDC.Enabled {
			return ConnectionTestResult{}, errors.New("enable OIDC before testing")
		}
	case "ldap":
		next.OIDC.Enabled = false
		if !next.LDAP.Enabled {
			return ConnectionTestResult{}, errors.New("enable LDAP before testing")
		}
	default:
		return ConnectionTestResult{}, errors.New("select LDAP or OIDC")
	}
	if err := s.validateConnections(&next, true); err != nil {
		return ConnectionTestResult{}, err
	}
	result := ConnectionTestResult{ID: rand.Text() + rand.Text(), Source: source, Status: "pending", ExpiresAt: time.Now().Add(5 * time.Minute)}
	p := login{expires: result.ExpiresAt, browser: rand.Text(), verifier: oauth2.GenerateVerifier(), nonce: rand.Text(), connectionsRevision: next.Revision, connectionTestID: result.ID}
	if source == "oidc" {
		if err := s.configureUpstreamLogin(ctx, &p, next.OIDC); err != nil {
			return ConnectionTestResult{}, errors.New("OIDC discovery or identity verifier unavailable")
		}
		result.StartURL = s.cfg.Auth.Issuer + "/connection-test?" + url.Values{"request": {result.ID}}.Encode()
	} else {
		identity, err := s.verifyLDAPIdentity(ctx, next.LDAP, username, password, address)
		if err != nil {
			return ConnectionTestResult{}, err
		}
		result.Status, result.Identity = "passed", &identity
		p = login{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupLocked()
	if next.Revision != s.connections.Revision {
		return ConnectionTestResult{}, configstore.ErrConflict
	}
	if len(s.connectionTests) >= 64 {
		return ConnectionTestResult{}, errors.New("too many pending connection tests; retry after five minutes")
	}
	s.connectionTests[result.ID] = &connectionTest{owner: owner, revision: next.Revision, login: p, result: result}
	return result, nil
}

func (s *Server) ConnectionTest(owner, id string) (ConnectionTestResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanupLocked()
	test := s.connectionTests[id]
	if test == nil || test.owner != owner {
		return ConnectionTestResult{}, configstore.ErrNotFound
	}
	if test.revision != s.connections.Revision {
		return ConnectionTestResult{}, configstore.ErrConflict
	}
	return test.result, nil
}

func (s *Server) startConnectionTest(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("request")
	s.mu.Lock()
	s.cleanupLocked()
	test := s.connectionTests[id]
	if test == nil || test.result.Source != "oidc" || test.result.Status != "pending" || test.revision != s.connections.Revision {
		s.mu.Unlock()
		http.Error(w, "connection test expired or already started; restart from the administration console", 400)
		return
	}
	p := test.login
	test.login, test.result.Status, test.result.StartURL = login{}, "running", ""
	s.mu.Unlock()
	// The gateway origin sets the same browser binding used by ordinary login;
	// the administration console may be served from a different origin.
	s.redirectUpstreamLogin(w, r, rand.Text(), p, url.Values{"prompt": {"login"}})
}

func (s *Server) finishConnectionTest(id string, identity *ConnectionIdentity, message string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	test := s.connectionTests[id]
	if test == nil || !time.Now().Before(test.result.ExpiresAt) || test.revision != s.connections.Revision || test.result.Status != "running" {
		return false
	}
	test.result.Status, test.result.Identity, test.result.Error = "passed", identity, message
	if identity == nil {
		test.result.Status = "failed"
	}
	return true
}
