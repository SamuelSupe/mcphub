package config

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/go-ldap/ldap/v3"
)

// EnterpriseConnections is persisted as encrypted managed configuration. The
// two sources have separate identity namespaces and never merge local accounts.
type EnterpriseConnections struct {
	Revision int64          `json:"revision"`
	OIDC     OIDCConnection `json:"oidc"`
	LDAP     LDAPConnection `json:"ldap"`
}

type OIDCConnection struct {
	Enabled      bool             `json:"enabled"`
	Provider     IdentityProvider `json:"provider"`
	ClientSecret string           `json:"client_secret"`
}

type LDAPConnection struct {
	Enabled            bool   `json:"enabled"`
	URL                string `json:"url"`
	BindDN             string `json:"bind_dn"`
	BindPassword       string `json:"bind_password"`
	UserBaseDN         string `json:"user_base_dn"`
	UserFilter         string `json:"user_filter"`
	UserIDAttribute    string `json:"user_id_attribute"`
	NameAttribute      string `json:"name_attribute"`
	GroupBaseDN        string `json:"group_base_dn"`
	GroupFilter        string `json:"group_filter"`
	GroupIDAttribute   string `json:"group_id_attribute"`
	GroupNameAttribute string `json:"group_name_attribute"`
	RootCAPEM          string `json:"root_ca_pem"`
}

func (p LDAPConnection) Namespace() string {
	data, _ := json.Marshal([]string{p.URL, p.UserBaseDN, p.UserFilter, p.UserIDAttribute, p.GroupBaseDN, p.GroupFilter, p.GroupIDAttribute})
	digest := sha256.Sum256(data)
	return "ldap:" + p.URL + "#" + hex.EncodeToString(digest[:])
}

var ldapAttribute = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9-]*(?:;binary)?$`)

func (p LDAPConnection) Validate() error {
	if !p.Enabled {
		return nil
	}
	u, err := url.Parse(p.URL)
	if err != nil || (u.Scheme != "ldaps" && u.Scheme != "ldap") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return fmt.Errorf("LDAP URL must use ldaps:// or ldap:// with StartTLS, without credentials or a search path")
	}
	for _, dn := range []string{p.BindDN, p.UserBaseDN} {
		if dn == "" {
			return fmt.Errorf("LDAP bind DN and user base DN are required")
		}
		if _, err := ldap.ParseDN(dn); err != nil {
			return fmt.Errorf("invalid LDAP distinguished name")
		}
	}
	if p.BindPassword == "" || len(p.BindPassword) > 4096 || !ldapAttribute.MatchString(p.UserIDAttribute) || !ldapAttribute.MatchString(p.NameAttribute) {
		return fmt.Errorf("LDAP service password and valid user ID/name attributes are required")
	}
	if strings.Count(p.UserFilter, "{{username}}") != 1 || len(p.UserFilter) > 4096 {
		return fmt.Errorf("LDAP user filter must contain exactly one {{username}} placeholder")
	}
	if _, err := ldap.CompileFilter(strings.ReplaceAll(p.UserFilter, "{{username}}", "test")); err != nil {
		return fmt.Errorf("invalid LDAP user filter")
	}
	if p.GroupBaseDN != "" || p.GroupFilter != "" || p.GroupIDAttribute != "" {
		if _, err := ldap.ParseDN(p.GroupBaseDN); err != nil || p.GroupBaseDN == "" || !ldapAttribute.MatchString(p.GroupIDAttribute) || !ldapAttribute.MatchString(p.GroupNameAttribute) || len(p.GroupFilter) > 4096 || (!strings.Contains(p.GroupFilter, "{{dn}}") && !strings.Contains(p.GroupFilter, "{{username}}")) {
			return fmt.Errorf("LDAP group search requires a base DN, stable ID attribute and a {{dn}} or {{username}} filter")
		}
		filter := strings.NewReplacer("{{dn}}", "cn=test", "{{username}}", "test").Replace(p.GroupFilter)
		if _, err := ldap.CompileFilter(filter); err != nil {
			return fmt.Errorf("invalid LDAP group filter")
		}
	}
	if len(p.RootCAPEM) > 128<<10 || (p.RootCAPEM != "" && !x509.NewCertPool().AppendCertsFromPEM([]byte(p.RootCAPEM))) {
		return fmt.Errorf("LDAP CA must contain PEM certificates")
	}
	return nil
}
