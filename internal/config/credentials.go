package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"path"
	"slices"
	"strings"
)

type VaultConfig struct {
	Address           string `yaml:"address" json:"address"`
	Mount             string `yaml:"mount" json:"mount"`
	Prefix            string `yaml:"prefix" json:"prefix"`
	Namespace         string `yaml:"namespace" json:"namespace,omitempty"`
	TokenEnv          string `yaml:"token_env" json:"-"`
	RoleIDEnv         string `yaml:"role_id_env" json:"-"`
	SecretIDEnv       string `yaml:"secret_id_env" json:"-"`
	AuthMount         string `yaml:"auth_mount" json:"auth_mount,omitempty"`
	CAFile            string `yaml:"ca_file" json:"-"`
	AllowInsecureHTTP bool   `yaml:"allow_insecure_http" json:"-"`
}

// CredentialConfig selects an administrator-owned Vault location. Personal
// secret paths are allocated by the server, never supplied by an MCP caller.
type CredentialConfig struct {
	Mode          string         `yaml:"mode" json:"mode"`
	Path          string         `yaml:"path" json:"path,omitempty"`
	Field         string         `yaml:"field" json:"field,omitempty"`
	Header        string         `yaml:"header" json:"header,omitempty"`
	Scheme        string         `yaml:"scheme" json:"scheme,omitempty"`
	DiscoveryPath string         `yaml:"discovery_path" json:"discovery_path,omitempty"`
	OAuth         *PersonalOAuth `yaml:"oauth" json:"oauth,omitempty"`
}

type PersonalOAuth struct {
	Issuer           string   `yaml:"issuer" json:"issuer"`
	ClientID         string   `yaml:"client_id" json:"client_id"`
	ClientSecretPath string   `yaml:"client_secret_path" json:"client_secret_path,omitempty"`
	Scopes           []string `yaml:"scopes" json:"scopes"`
}

func (v *VaultConfig) Validate() error {
	if v == nil {
		return nil
	}
	u, err := url.Parse(v.Address)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return fmt.Errorf("vault.address must be an HTTPS origin")
	}
	loopback := net.ParseIP(u.Hostname())
	if u.Scheme != "https" && !(v.AllowInsecureHTTP && u.Scheme == "http" && loopback != nil && loopback.IsLoopback()) {
		return fmt.Errorf("vault.address must use HTTPS (HTTP is allowed only on an explicitly enabled loopback address)")
	}
	if v.Mount == "" {
		v.Mount = "secret"
	}
	if v.Prefix == "" {
		v.Prefix = "mcphub"
	}
	if v.AuthMount == "" {
		v.AuthMount = "approle"
	}
	for _, p := range []string{v.Mount, v.Prefix, v.AuthMount} {
		if !ValidVaultPath(p) {
			return fmt.Errorf("invalid Vault mount or prefix")
		}
	}
	if strings.ContainsAny(v.Namespace, "\r\n\x00") {
		return fmt.Errorf("invalid Vault namespace")
	}
	if (v.TokenEnv == "") == (v.RoleIDEnv == "" && v.SecretIDEnv == "") || (v.RoleIDEnv == "") != (v.SecretIDEnv == "") {
		return fmt.Errorf("configure vault.token_env or both vault.role_id_env and vault.secret_id_env")
	}
	for _, name := range []string{v.TokenEnv, v.RoleIDEnv, v.SecretIDEnv} {
		if name != "" && !envNamePattern.MatchString(name) {
			return fmt.Errorf("invalid Vault credential environment variable name")
		}
	}
	return nil
}

func ValidVaultPath(value string) bool {
	return value != "" && len(value) <= 512 && path.Clean(value) == value && !strings.HasPrefix(value, "/") && !strings.HasPrefix(value, "../") && value != ".." && value != "." && !strings.ContainsAny(value, "%?#\\\r\n\x00")
}

func (c *CredentialConfig) Validate(headers map[string]string, oauth *OAuthConfig) error {
	if c == nil {
		return nil
	}
	if c.Mode != "shared" && c.Mode != "personal" {
		return fmt.Errorf("credentials.mode must be shared or personal")
	}
	if c.Header == "" {
		c.Header = "Authorization"
	}
	if c.Field == "" {
		c.Field = "token"
	}
	if c.Scheme == "" && strings.EqualFold(c.Header, "Authorization") {
		c.Scheme = "Bearer"
	}
	c.Header = http.CanonicalHeaderKey(c.Header)
	if !headerName.MatchString(c.Header) || forbiddenBackendHeader(c.Header) || len(c.Scheme) > 64 || strings.ContainsAny(c.Scheme, " \t\r\n\x00") || len(c.Field) > 128 {
		return fmt.Errorf("invalid credential header, scheme, or field")
	}
	if oauth != nil {
		return fmt.Errorf("Vault credentials cannot be combined with service OAuth")
	}
	for name := range headers {
		if strings.EqualFold(name, c.Header) || strings.EqualFold(name, "Authorization") {
			return fmt.Errorf("authentication headers must come from the configured credential source")
		}
	}
	if c.Mode == "shared" {
		if !ValidVaultPath(c.Path) || c.OAuth != nil || c.DiscoveryPath != "" {
			return fmt.Errorf("shared credentials require a Vault path and cannot configure personal login")
		}
		return nil
	}
	if c.Path != "" || c.DiscoveryPath != "" && !ValidVaultPath(c.DiscoveryPath) {
		return fmt.Errorf("personal credentials use server-allocated paths; invalid discovery path")
	}
	if c.OAuth != nil {
		o := c.OAuth
		u, err := validateAbsoluteURL("personal OAuth issuer", o.Issuer, false)
		if err != nil || u.RawQuery != "" || u.Fragment != "" || o.ClientID == "" || len(o.ClientID) > 256 {
			return fmt.Errorf("personal OAuth requires an HTTPS issuer and client ID")
		}
		if o.ClientSecretPath != "" && !ValidVaultPath(o.ClientSecretPath) {
			return fmt.Errorf("invalid OAuth client secret path")
		}
		if c.Header != "Authorization" || c.Scheme != "Bearer" {
			return fmt.Errorf("personal OAuth uses Authorization: Bearer")
		}
		if err := validateScopes("personal OAuth scopes", o.Scopes); err != nil {
			return err
		}
	}
	return nil
}

func CloneCredentials(c *CredentialConfig) *CredentialConfig {
	if c == nil {
		return nil
	}
	copy := *c
	if c.OAuth != nil {
		oauth := *c.OAuth
		oauth.Scopes = slices.Clone(c.OAuth.Scopes)
		copy.OAuth = &oauth
	}
	return &copy
}

func CredentialPolicy(endpoint string, c *CredentialConfig) string {
	data, _ := json.Marshal([]any{endpoint, c})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
