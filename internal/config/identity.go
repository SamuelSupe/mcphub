package config

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strings"
)

// SSOConfig makes MCPHub the issuer; upstream credentials never reach MCP clients.
type SSOConfig struct {
	Upstream          IdentityProvider `yaml:"upstream"`
	Clients           []SSOClient      `yaml:"clients"`
	BootstrapSubjects []string         `yaml:"bootstrap_subjects"`
	DirectoryTokenEnv string           `yaml:"directory_token_env"`
}

type IdentityProvider struct {
	Protocol         string   `yaml:"protocol"`
	Issuer           string   `yaml:"issuer"`
	AuthorizationURL string   `yaml:"authorization_url"`
	TokenURL         string   `yaml:"token_url"`
	UserInfoURL      string   `yaml:"userinfo_url"`
	ClientID         string   `yaml:"client_id"`
	ClientSecretEnv  string   `yaml:"client_secret_env"`
	TokenAuthMethod  string   `yaml:"token_auth_method"`
	Scopes           []string `yaml:"scopes"`
	SubjectClaim     string   `yaml:"subject_claim"`
	NameClaim        string   `yaml:"name_claim"`
	TenantClaim      string   `yaml:"tenant_claim"`
	TenantValue      string   `yaml:"tenant_value"`
	GroupsClaim      string   `yaml:"groups_claim"`
	DepartmentsClaim string   `yaml:"departments_claim"`
	SuccessClaim     string   `yaml:"success_claim"`
	SuccessValue     string   `yaml:"success_value"`
}

// App-scoped subjects and tenant-local IDs must not inherit another connection's
// grants when the operator changes the application or subject mapping.
func (p IdentityProvider) Namespace() string {
	data, _ := json.Marshal([]string{p.Protocol, p.Issuer, p.ClientID, p.SubjectClaim, p.TenantClaim, p.TenantValue})
	digest := sha256.Sum256(data)
	return p.Issuer + "#" + hex.EncodeToString(digest[:])
}

type SSOClient struct {
	ID           string   `yaml:"id"`
	RedirectURIs []string `yaml:"redirect_uris"`
	Resources    []string `yaml:"resources"`
}

// Each access entry is an independent grant. Resource predicates must match
// within one entry, never combined with the tool list of a different grant.
type IdentityPermissions struct {
	Roles  []string         `json:"roles"`
	Scopes []string         `json:"scopes"`
	Access []IdentityAccess `json:"access"`
}

type IdentityAccess struct {
	EndpointID         string         `json:"endpoint_id"`
	Tools              []string       `json:"tools"`
	AllowWriteRequests bool           `json:"allow_write_requests"`
	Prompts            bool           `json:"prompts"`
	Resources          bool           `json:"resources"`
	Subscriptions      bool           `json:"subscriptions"`
	ResourceRules      []ResourceRule `json:"resource_rules"`
}

func (p IdentityPermissions) Validate() error {
	if len(p.Access) > 256 || len(p.Scopes) > 256 {
		return fmt.Errorf("too many permissions")
	}
	for _, role := range p.Roles {
		if !slices.Contains([]string{"admin", "approver", "security_reviewer"}, role) {
			return fmt.Errorf("unknown role %q", role)
		}
	}
	for _, scope := range p.Scopes {
		if len(scope) == 0 || len(scope) > 256 || strings.ContainsAny(scope, " \t\r\n\"\\") {
			return fmt.Errorf("invalid scope")
		}
	}
	for _, a := range p.Access {
		if !backendIDPattern.MatchString(a.EndpointID) {
			return fmt.Errorf("invalid endpoint id")
		}
		if err := validatePublishedTools(a.Tools); err != nil {
			return err
		}
		if err := validateResourceRules(a.ResourceRules); err != nil {
			return err
		}
	}
	return nil
}

func (p IdentityPermissions) AllowsEndpoint(id string) bool {
	return slices.ContainsFunc(p.Access, func(a IdentityAccess) bool { return a.EndpointID == id })
}

func (p IdentityPermissions) EffectiveScopes(admin AdminConfig) []string {
	result := slices.Clone(p.Scopes)
	for _, role := range p.Roles {
		switch role {
		case "admin":
			result = append(result, admin.RequiredScopes...)
		case "approver":
			result = append(result, admin.Approvals.Scopes()...)
		case "security_reviewer":
			result = append(result, admin.Approvals.PolicyChanges.Scopes()...)
		}
	}
	slices.Sort(result)
	return slices.Compact(result)
}

func (p IdentityPermissions) AllowsCapability(id, capability string) bool {
	return slices.ContainsFunc(p.Access, func(a IdentityAccess) bool {
		if a.EndpointID != id {
			return false
		}
		switch capability {
		case "prompts":
			return a.Prompts
		case "resources":
			return a.Resources
		case "subscriptions":
			return a.Subscriptions && a.Resources
		}
		return false
	})
}

func (p IdentityPermissions) AllowsTool(id, name, effect string) bool {
	return slices.ContainsFunc(p.Access, func(a IdentityAccess) bool {
		return a.EndpointID == id && slices.Contains(a.Tools, name) && (effect == "read" || a.AllowWriteRequests)
	})
}

func (p IdentityPermissions) ToolArguments(id, name, effect string, args json.RawMessage) (json.RawMessage, error) {
	for _, a := range p.Access {
		if a.EndpointID != id || !slices.Contains(a.Tools, name) || (effect != "read" && !a.AllowWriteRequests) {
			continue
		}
		if normalized, err := CheckToolResources(a.ResourceRules, args); err == nil {
			return normalized, nil
		}
	}
	return nil, fmt.Errorf("user permission does not allow this tool or resource")
}

func (cfg *Config) validateSSO() error {
	s := cfg.Auth.SSO
	if s == nil {
		return nil
	}
	issuer, _ := url.Parse(cfg.Auth.Issuer)
	hub, _ := url.Parse(cfg.Server.PublicURL)
	if !cfg.Admin.Enabled || issuer.Scheme != "https" || issuer.Host != hub.Host || issuer.Path != "/sso" || issuer.RawPath != "" {
		return fmt.Errorf("auth.sso requires admin storage and auth.issuer=https://<MCP host>/sso")
	}
	if strings.HasPrefix(hub.Path, "/sso/") || strings.HasPrefix(hub.Path, "/.well-known/") {
		return fmt.Errorf("MCP path overlaps the SSO endpoints")
	}
	p := &s.Upstream
	if p.Protocol != "oidc" && p.Protocol != "oauth2" {
		return fmt.Errorf("SSO upstream protocol must be oidc or oauth2")
	}
	if _, err := validateAbsoluteURL("SSO upstream issuer", p.Issuer, false); err != nil {
		return err
	}
	if p.Issuer == cfg.Auth.Issuer || strings.ContainsAny(p.Issuer, "?#") {
		return fmt.Errorf("invalid upstream issuer")
	}
	if p.ClientID == "" || p.ClientSecretEnv == "" {
		return fmt.Errorf("SSO upstream confidential client id and client_secret_env are required")
	}
	if p.TokenAuthMethod == "" {
		p.TokenAuthMethod = "client_secret_post"
	}
	if !slices.Contains([]string{"client_secret_post", "client_secret_basic"}, p.TokenAuthMethod) {
		return fmt.Errorf("unsupported upstream token auth method")
	}
	if p.Protocol == "oauth2" {
		for _, raw := range []string{p.AuthorizationURL, p.TokenURL, p.UserInfoURL} {
			if _, err := validateAbsoluteURL("SSO endpoint", raw, false); err != nil {
				return err
			}
			if strings.Contains(raw, "#") {
				return fmt.Errorf("SSO endpoints must not contain fragments")
			}
		}
		if p.SubjectClaim == "" {
			return fmt.Errorf("OAuth2 SSO subject_claim is required")
		}
	} else if p.SubjectClaim != "" && p.SubjectClaim != "sub" {
		return fmt.Errorf("OIDC identity must use sub")
	}
	if (p.TenantClaim == "") != (p.TenantValue == "") {
		return fmt.Errorf("tenant_claim and tenant_value must be configured together")
	}
	if (p.SuccessClaim == "") != (p.SuccessValue == "") {
		return fmt.Errorf("success_claim and success_value must be configured together")
	}
	for _, path := range []string{p.SubjectClaim, p.NameClaim, p.TenantClaim, p.GroupsClaim, p.DepartmentsClaim, p.SuccessClaim} {
		if strings.Contains(path, "..") || strings.HasPrefix(path, ".") || strings.HasSuffix(path, ".") {
			return fmt.Errorf("invalid identity claim path")
		}
	}
	seen := map[string]bool{}
	for _, c := range s.Clients {
		if c.ID == "" || seen[c.ID] || len(c.ID) > 256 || len(c.RedirectURIs) == 0 || len(c.Resources) == 0 {
			return fmt.Errorf("SSO clients need unique ids, redirect_uris and resources")
		}
		seen[c.ID] = true
		for _, raw := range c.RedirectURIs {
			u, err := url.Parse(raw)
			if err != nil || u.User != nil || u.Fragment != "" || u.RawQuery != "" || u.Host == "" || u.Path == "" || (u.Scheme != "https" && !(u.Scheme == "http" && u.Hostname() == "127.0.0.1")) {
				return fmt.Errorf("invalid SSO client redirect URI")
			}
		}
		for _, r := range c.Resources {
			if r != cfg.Server.PublicURL && (!cfg.Admin.Remote() || r != cfg.Admin.PublicURL) {
				return fmt.Errorf("SSO resource must be the MCP or admin public URL")
			}
		}
	}
	if len(seen) == 0 {
		return fmt.Errorf("at least one SSO client is required")
	}
	if cfg.Admin.Remote() && (!seen[cfg.Admin.ClientID] || cfg.Admin.ClientSecretEnv != "") {
		return fmt.Errorf("remote admin must use a registered SSO public client")
	}
	if cfg.ClientAuthorization.Enabled && (!seen[cfg.ClientAuthorization.ClientID] || cfg.ClientAuthorization.ClientSecretEnv != "") {
		return fmt.Errorf("client authorization portal must use a registered SSO public client")
	}
	return nil
}
