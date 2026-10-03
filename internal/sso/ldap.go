package sso

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/SamuelSupe/mcphub/v2/internal/config"
	"github.com/SamuelSupe/mcphub/v2/internal/configstore"
	"github.com/go-ldap/ldap/v3"
)

func connectLDAP(ctx context.Context, p config.LDAPConnection) (*ldap.Conn, error) {
	u, _ := url.Parse(p.URL)
	roots, err := x509.SystemCertPool()
	if err != nil {
		roots = x509.NewCertPool()
	}
	if p.RootCAPEM != "" && !roots.AppendCertsFromPEM([]byte(p.RootCAPEM)) {
		return nil, errors.New("invalid LDAP CA certificates")
	}
	options := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: u.Hostname(), RootCAs: roots}
	conn, err := ldap.DialURL(p.URL, ldap.DialWithDialer(&net.Dialer{Timeout: 10 * time.Second}), ldap.DialWithTLSConfig(options))
	if err != nil {
		return nil, errors.New("LDAP connection or TLS verification failed")
	}
	conn.SetTimeout(10 * time.Second)
	context.AfterFunc(ctx, func() { conn.Close() })
	if u.Scheme == "ldap" {
		if err = conn.StartTLS(options); err != nil {
			conn.Close()
			return nil, errors.New("LDAP StartTLS failed")
		}
	}
	// A simple bind is performed only after TLS, with both DN and password.
	if p.BindDN == "" || p.BindPassword == "" || conn.Bind(p.BindDN, p.BindPassword) != nil {
		conn.Close()
		return nil, errors.New("LDAP service bind failed")
	}
	return conn, nil
}

func probeLDAP(conn *ldap.Conn, p config.LDAPConnection) error {
	for _, base := range []string{p.UserBaseDN, p.GroupBaseDN} {
		if base == "" {
			continue
		}
		result, err := conn.Search(ldap.NewSearchRequest(base, ldap.ScopeBaseObject, ldap.NeverDerefAliases, 1, 5, false, "(objectClass=*)", []string{"1.1"}, nil))
		if err != nil || len(result.Entries) != 1 {
			return errors.New("LDAP search base is missing or unreadable")
		}
	}
	return nil
}

// Immutable UUID/GUID attributes are encoded so AD binary IDs and OpenLDAP
// textual UUIDs obey the same stable, bounded identity contract.
func ldapSubject(entry *ldap.Entry, attribute string) (string, error) {
	values := entry.GetRawAttributeValues(attribute)
	if len(values) != 1 || len(values[0]) == 0 || len(values[0]) > 192 {
		return "", errors.New("LDAP stable ID attribute must contain one nonempty value")
	}
	return base64.RawURLEncoding.EncodeToString(values[0]), nil
}

func (s *Server) authenticateLDAP(ctx context.Context, p config.LDAPConnection, username, password, address string) (configstore.Identity, error) {
	identity, err := s.verifyLDAPIdentity(ctx, p, username, password, address)
	if err != nil {
		return configstore.Identity{}, err
	}
	ctx = configstore.WithActor(ctx, "ldap-login")
	user, err := s.store.SyncIdentity(ctx, identity.Provider, identity.Subject, identity.Name, identity.Groups, nil, false, false)
	if err == nil {
		err = s.store.SyncGroupNames(ctx, identity.Provider, identity.GroupNames)
	}
	return user, err
}

func (s *Server) verifyLDAPIdentity(ctx context.Context, p config.LDAPConnection, username, password, address string) (ConnectionIdentity, error) {
	if !p.Enabled || username == "" || len(username) > 256 || password == "" || len(password) > 1024 || strings.ContainsRune(username, '\x00') {
		return ConnectionIdentity{}, configstore.ErrCredentials
	}
	release, err := s.reservePasswordAttempt(address, "ldap:"+strings.ToLower(username))
	if err != nil {
		return ConnectionIdentity{}, err
	}
	defer release()
	conn, err := connectLDAP(ctx, p)
	if err != nil {
		return ConnectionIdentity{}, err
	}
	defer conn.Close()
	filter := strings.ReplaceAll(p.UserFilter, "{{username}}", ldap.EscapeFilter(username))
	result, err := conn.Search(ldap.NewSearchRequest(p.UserBaseDN, ldap.ScopeWholeSubtree, ldap.NeverDerefAliases, 2, 5, false, filter, []string{p.UserIDAttribute, p.NameAttribute}, nil))
	if err != nil || len(result.Entries) != 1 || result.Entries[0].DN == "" {
		return ConnectionIdentity{}, configstore.ErrCredentials
	}
	entry := result.Entries[0]
	subject, err := ldapSubject(entry, p.UserIDAttribute)
	if err != nil {
		return ConnectionIdentity{}, err
	}
	groups := []string{}
	groupNames := map[string]string{}
	if p.GroupFilter != "" {
		filter := strings.NewReplacer("{{dn}}", ldap.EscapeFilter(entry.DN), "{{username}}", ldap.EscapeFilter(username)).Replace(p.GroupFilter)
		result, err := conn.Search(ldap.NewSearchRequest(p.GroupBaseDN, ldap.ScopeWholeSubtree, ldap.NeverDerefAliases, 256, 5, false, filter, []string{p.GroupIDAttribute, p.GroupNameAttribute}, nil))
		if err != nil || len(result.Entries) > 256 {
			return ConnectionIdentity{}, errors.New("LDAP group search failed or exceeded its limit")
		}
		for _, group := range result.Entries {
			id, err := ldapSubject(group, p.GroupIDAttribute)
			if err != nil {
				return ConnectionIdentity{}, err
			}
			groups = append(groups, id)
			groupNames[id] = group.GetAttributeValue(p.GroupNameAttribute)
		}
	}
	if conn.Bind(entry.DN, password) != nil {
		return ConnectionIdentity{}, configstore.ErrCredentials
	}
	name := entry.GetAttributeValue(p.NameAttribute)
	if name == "" {
		name = username
	}
	if len(name) > 512 {
		return ConnectionIdentity{}, errors.New("invalid upstream identity")
	}
	return ConnectionIdentity{Provider: p.Namespace(), Subject: subject, Name: name, Groups: groups, GroupNames: groupNames}, nil
}
