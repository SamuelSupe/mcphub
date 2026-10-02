package sso

import (
	"context"
	"errors"
	"os"
	"strings"

	"github.com/SamuelSupe/mcphub/v2/internal/authn"
	"github.com/SamuelSupe/mcphub/v2/internal/config"
	"github.com/SamuelSupe/mcphub/v2/internal/configstore"
)

func (s *Server) Connections() config.EnterpriseConnections {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.connections
}

func (s *Server) loadConnections(ctx context.Context) error {
	connections, err := s.store.EnterpriseConnections(ctx)
	if errors.Is(err, configstore.ErrNotFound) {
		p := s.cfg.Auth.SSO.Upstream
		connections.OIDC = config.OIDCConnection{Enabled: p.Protocol != "", Provider: p, ClientSecret: os.Getenv(p.ClientSecretEnv)}
		err = nil
	}
	if err == nil {
		err = s.validateConnections(&connections, false)
	}
	if err == nil {
		s.connections = connections
	}
	return err
}

func (s *Server) validateConnections(c *config.EnterpriseConnections, managed bool) error {
	c.LDAP.RootCAPEM = strings.TrimSpace(c.LDAP.RootCAPEM)
	if c.OIDC.Enabled {
		if managed && c.OIDC.Provider.Protocol != "oidc" {
			return errors.New("UI identity connection must use OIDC")
		}
		// The secret is held in encrypted storage rather than an environment
		// variable, while retaining the existing provider validation contract.
		c.OIDC.Provider.ClientSecretEnv = "managed_identity_secret"
		if err := c.OIDC.Provider.Validate(); err != nil {
			return err
		}
		if c.OIDC.Provider.Issuer == s.cfg.Auth.Issuer || c.OIDC.ClientSecret == "" || len(c.OIDC.ClientSecret) > 4096 {
			return errors.New("OIDC requires an external issuer and a client secret")
		}
	}
	return c.LDAP.Validate()
}

func (s *Server) UpdateConnections(ctx context.Context, expected int64, next config.EnterpriseConnections) (config.EnterpriseConnections, error) {
	if !s.Builtin() {
		return next, errors.New("UI identity configuration requires built-in authentication and a local recovery administrator")
	}
	if err := s.validateConnections(&next, true); err != nil {
		return next, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if expected != s.connections.Revision {
		return next, configstore.ErrConflict
	}
	next, err := s.store.SaveEnterpriseConnections(ctx, s.connections, next)
	if err == nil {
		s.connections = next
		// Pending authorizations cannot finish against a replaced source.
		clear(s.pending)
		clear(s.codes)
	}
	return next, err
}

func (s *Server) ProbeConnections(ctx context.Context, next config.EnterpriseConnections, source string) error {
	if source == "oidc" {
		next.LDAP.Enabled = false
	}
	if source == "ldap" {
		next.OIDC.Enabled = false
	}
	if err := s.validateConnections(&next, true); err != nil {
		return err
	}
	switch source {
	case "oidc":
		if !next.OIDC.Enabled {
			return errors.New("enable OIDC before testing")
		}
		_, err := authn.LoginMetadata(ctx, next.OIDC.Provider.Issuer, s.client, false)
		if err == nil {
			_, err = authn.LoginIDVerifier(ctx, next.OIDC.Provider.Issuer, next.OIDC.Provider.ClientID, s.client)
		}
		return err
	case "ldap":
		if !next.LDAP.Enabled {
			return errors.New("enable LDAP before testing")
		}
		conn, err := connectLDAP(ctx, next.LDAP)
		if err == nil {
			defer conn.Close()
			err = probeLDAP(conn, next.LDAP)
		}
		return err
	default:
		return errors.New("select LDAP or OIDC")
	}
}

func (s *Server) ProviderAllowed(provider string, directoryManaged bool) bool {
	if s.Builtin() && provider == config.LocalIdentityProvider {
		return true
	}
	c := s.Connections()
	return c.LDAP.Enabled && provider == c.LDAP.Namespace() || c.OIDC.Enabled && provider == c.OIDC.Provider.Namespace() && (s.cfg.Auth.SSO.DirectoryTokenEnv == "" || directoryManaged)
}

func (s *Server) Providers() []map[string]string {
	result := []map[string]string{}
	if s.Builtin() {
		result = append(result, map[string]string{"id": config.LocalIdentityProvider, "name": "MCPHub"})
	}
	c := s.Connections()
	if c.OIDC.Enabled {
		result = append(result, map[string]string{"id": c.OIDC.Provider.Namespace(), "name": "OIDC · " + c.OIDC.Provider.Issuer})
	}
	if c.LDAP.Enabled {
		result = append(result, map[string]string{"id": c.LDAP.Namespace(), "name": "LDAP · " + c.LDAP.URL})
	}
	return result
}
