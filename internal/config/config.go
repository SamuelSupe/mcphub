package config

import (
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	pathpkg "path"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	defaultListen              = ":8080"
	defaultPageSize            = 1000
	defaultRequestTimeout      = 60 * time.Second
	defaultDrainTimeout        = 15 * time.Second
	defaultRefreshInterval     = 5 * time.Minute
	defaultCatalogTTL          = 30 * time.Second
	defaultMaxRequestBodyBytes = int64(4 << 20)
)

var (
	backendIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)
	envPattern       = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)
	headerName       = regexp.MustCompile("^[!#$%&'*+\\-.^_`|~0-9A-Za-z]+$")
)

type Duration struct {
	time.Duration
}

func (d *Duration) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.ScalarNode {
		return fmt.Errorf("duration must be a string")
	}
	v, err := time.ParseDuration(node.Value)
	if err != nil {
		return err
	}
	d.Duration = v
	return nil
}

type Config struct {
	Server   ServerConfig    `yaml:"server"`
	Auth     AuthConfig      `yaml:"auth"`
	Backends []BackendConfig `yaml:"backends"`
}

type ServerConfig struct {
	Listen              string   `yaml:"listen"`
	PublicURL           string   `yaml:"public_url"`
	PageSize            int      `yaml:"page_size"`
	RequestTimeout      Duration `yaml:"request_timeout"`
	DrainTimeout        Duration `yaml:"drain_timeout"`
	RefreshInterval     Duration `yaml:"refresh_interval"`
	CatalogTTL          Duration `yaml:"catalog_ttl"`
	MaxRequestBodyBytes int64    `yaml:"max_request_body_bytes"`
	AllowedOrigins      []string `yaml:"allowed_origins"`
}

type AuthConfig struct {
	Issuer string `yaml:"issuer"`
}

type BackendConfig struct {
	ID                string            `yaml:"id"`
	URL               string            `yaml:"url"`
	Required          bool              `yaml:"required"`
	RequiredScopes    []string          `yaml:"required_scopes"`
	ToolRules         []ToolRule        `yaml:"tool_rules"`
	RequestTimeout    Duration          `yaml:"request_timeout"`
	AllowInsecureHTTP bool              `yaml:"allow_insecure_http"`
	Headers           map[string]string `yaml:"headers"`
	OAuth             *OAuthConfig      `yaml:"oauth"`
}

type ToolRule struct {
	Match          string   `yaml:"match"`
	RequiredScopes []string `yaml:"required_scopes"`
}

type OAuthConfig struct {
	Type         string   `yaml:"type"`
	Issuer       string   `yaml:"issuer"`
	ClientID     string   `yaml:"client_id"`
	ClientSecret string   `yaml:"client_secret"`
	Scopes       []string `yaml:"scopes"`
}

func Load(path string) (*Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open config: %w", err)
	}
	defer f.Close()

	cfg := defaults()
	dec := yaml.NewDecoder(f)
	dec.KnownFields(true)
	if err := dec.Decode(cfg); err != nil {
		return nil, fmt.Errorf("decode config: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, fmt.Errorf("decode config: multiple YAML documents are not supported")
		}
		return nil, fmt.Errorf("decode config: %w", err)
	}
	if err := expandEnvironment(cfg); err != nil {
		return nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func defaults() *Config {
	return &Config{Server: ServerConfig{
		Listen:              defaultListen,
		PageSize:            defaultPageSize,
		RequestTimeout:      Duration{defaultRequestTimeout},
		DrainTimeout:        Duration{defaultDrainTimeout},
		RefreshInterval:     Duration{defaultRefreshInterval},
		CatalogTTL:          Duration{defaultCatalogTTL},
		MaxRequestBodyBytes: defaultMaxRequestBodyBytes,
	}}
}

func expandEnvironment(cfg *Config) error {
	fields := []*string{&cfg.Server.Listen, &cfg.Server.PublicURL, &cfg.Auth.Issuer}
	for i := range cfg.Server.AllowedOrigins {
		fields = append(fields, &cfg.Server.AllowedOrigins[i])
	}
	for i := range cfg.Backends {
		backend := &cfg.Backends[i]
		fields = append(fields, &backend.ID, &backend.URL)
		for name, value := range backend.Headers {
			expanded, err := expandString(value)
			if err != nil {
				return fmt.Errorf("backend %q header %q: %w", backend.ID, name, err)
			}
			backend.Headers[name] = expanded
		}
		if backend.OAuth != nil {
			fields = append(fields,
				&backend.OAuth.Type,
				&backend.OAuth.Issuer,
				&backend.OAuth.ClientID,
				&backend.OAuth.ClientSecret,
			)
			for j := range backend.OAuth.Scopes {
				fields = append(fields, &backend.OAuth.Scopes[j])
			}
		}
		for j := range backend.RequiredScopes {
			fields = append(fields, &backend.RequiredScopes[j])
		}
		for j := range backend.ToolRules {
			rule := &backend.ToolRules[j]
			fields = append(fields, &rule.Match)
			for k := range rule.RequiredScopes {
				fields = append(fields, &rule.RequiredScopes[k])
			}
		}
	}
	for _, field := range fields {
		expanded, err := expandString(*field)
		if err != nil {
			return err
		}
		*field = expanded
	}
	return nil
}

func expandString(value string) (string, error) {
	var missing string
	expanded := envPattern.ReplaceAllStringFunc(value, func(match string) string {
		name := envPattern.FindStringSubmatch(match)[1]
		v, ok := os.LookupEnv(name)
		if !ok && missing == "" {
			missing = name
		}
		return v
	})
	if missing != "" {
		return "", fmt.Errorf("environment variable %s is not set", missing)
	}
	return expanded, nil
}

func (cfg *Config) Validate() error {
	if err := validateListen(cfg.Server.Listen); err != nil {
		return err
	}
	publicURL, err := validateAbsoluteURL("server.public_url", cfg.Server.PublicURL, false)
	if err != nil {
		return err
	}
	if publicURL.RawQuery != "" || publicURL.Fragment != "" {
		return fmt.Errorf("server.public_url must not contain a query or fragment")
	}
	if publicURL.RawPath != "" {
		return fmt.Errorf("server.public_url path must not use percent-encoded characters")
	}
	if publicURL.Path == "" || publicURL.Path == "/" {
		return fmt.Errorf("server.public_url must include the MCP endpoint path")
	}
	switch publicURL.Path {
	case "/healthz", "/readyz", "/.well-known/oauth-protected-resource":
		return fmt.Errorf("server.public_url path %q is reserved", publicURL.Path)
	}
	if cfg.Server.PageSize <= 0 {
		return fmt.Errorf("server.page_size must be positive")
	}
	if cfg.Server.RequestTimeout.Duration <= 0 {
		return fmt.Errorf("server.request_timeout must be positive")
	}
	if cfg.Server.DrainTimeout.Duration <= 0 {
		return fmt.Errorf("server.drain_timeout must be positive")
	}
	if cfg.Server.RefreshInterval.Duration <= 0 {
		return fmt.Errorf("server.refresh_interval must be positive")
	}
	if cfg.Server.CatalogTTL.Duration < 0 {
		return fmt.Errorf("server.catalog_ttl must not be negative")
	}
	if cfg.Server.MaxRequestBodyBytes <= 0 {
		return fmt.Errorf("server.max_request_body_bytes must be positive")
	}
	authIssuer, err := validateAbsoluteURL("auth.issuer", cfg.Auth.Issuer, false)
	if err != nil {
		return err
	}
	if authIssuer.RawQuery != "" || authIssuer.Fragment != "" {
		return fmt.Errorf("auth.issuer must not contain a query or fragment")
	}
	if err := validateOrigins(cfg.Server.AllowedOrigins); err != nil {
		return err
	}
	if len(cfg.Backends) == 0 {
		return fmt.Errorf("at least one backend is required")
	}

	ids := make(map[string]string, len(cfg.Backends))
	for i := range cfg.Backends {
		backend := &cfg.Backends[i]
		if !backendIDPattern.MatchString(backend.ID) {
			return fmt.Errorf("backend %q: id must match %s", backend.ID, backendIDPattern)
		}
		identity := strings.ToLower(backend.ID)
		if previous, exists := ids[identity]; exists {
			if previous != backend.ID {
				return fmt.Errorf("backend %q: id conflicts case-insensitively with backend %q", backend.ID, previous)
			}
			return fmt.Errorf("backend %q: duplicate id", backend.ID)
		}
		ids[identity] = backend.ID
		backendURL, err := validateAbsoluteURL("backend "+backend.ID+" url", backend.URL, backend.AllowInsecureHTTP)
		if err != nil {
			return err
		}
		if backendURL.Fragment != "" {
			return fmt.Errorf("backend %q: url must not contain a fragment", backend.ID)
		}
		if backend.RequestTimeout.Duration == 0 {
			backend.RequestTimeout = Duration{cfg.Server.RequestTimeout.Duration}
		}
		if backend.RequestTimeout.Duration <= 0 {
			return fmt.Errorf("backend %q: request_timeout must be positive", backend.ID)
		}
		if err := validateScopes("backend "+backend.ID+" required_scopes", backend.RequiredScopes); err != nil {
			return err
		}
		if err := validateToolRules(backend); err != nil {
			return err
		}
		canonicalHeaders := make(map[string]string, len(backend.Headers))
		for name, value := range backend.Headers {
			if !headerName.MatchString(name) {
				return fmt.Errorf("backend %q: invalid header name %q", backend.ID, name)
			}
			canonical := http.CanonicalHeaderKey(name)
			if previous, exists := canonicalHeaders[canonical]; exists {
				return fmt.Errorf("backend %q: headers %q and %q are duplicates", backend.ID, previous, name)
			}
			canonicalHeaders[canonical] = name
			if forbiddenBackendHeader(canonical) {
				return fmt.Errorf("backend %q: header %q is managed by the HTTP or MCP transport", backend.ID, name)
			}
			if strings.ContainsAny(value, "\r\n") {
				return fmt.Errorf("backend %q: header %q contains a line break", backend.ID, name)
			}
		}
		if backend.OAuth != nil {
			if err := validateOAuth(backend); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateToolRules(backend *BackendConfig) error {
	seen := make(map[string]struct{}, len(backend.ToolRules))
	for i := range backend.ToolRules {
		rule := &backend.ToolRules[i]
		field := fmt.Sprintf("backend %s tool_rules[%d]", backend.ID, i)
		if rule.Match == "" {
			return fmt.Errorf("%s match is required", field)
		}
		if _, err := pathpkg.Match(rule.Match, ""); err != nil {
			return fmt.Errorf("%s match %q is invalid: %w", field, rule.Match, err)
		}
		if _, exists := seen[rule.Match]; exists {
			return fmt.Errorf("backend %q: tool_rules contains duplicate match %q", backend.ID, rule.Match)
		}
		seen[rule.Match] = struct{}{}
		if len(rule.RequiredScopes) == 0 {
			return fmt.Errorf("%s required_scopes must not be empty", field)
		}
		if err := validateScopes(field+" required_scopes", rule.RequiredScopes); err != nil {
			return err
		}
	}
	return nil
}

func validateListen(value string) error {
	if value == "" {
		return fmt.Errorf("server.listen is required")
	}
	_, port, err := net.SplitHostPort(value)
	if err != nil {
		return fmt.Errorf("server.listen must be a host:port address: %w", err)
	}
	number, err := strconv.Atoi(port)
	if err != nil || number < 1 || number > 65535 {
		return fmt.Errorf("server.listen must use a numeric port between 1 and 65535")
	}
	return nil
}

func forbiddenBackendHeader(name string) bool {
	if strings.HasPrefix(name, "Mcp-") {
		return true
	}
	switch name {
	case "Accept", "Connection", "Content-Length", "Content-Type", "Host", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization", "Proxy-Connection", "Te", "Trailer", "Transfer-Encoding", "Upgrade":
		return true
	default:
		return false
	}
}

func validateOAuth(backend *BackendConfig) error {
	oauth := backend.OAuth
	if oauth.Type != "client_credentials" {
		return fmt.Errorf("backend %q: oauth.type must be client_credentials", backend.ID)
	}
	if oauth.ClientID == "" || oauth.ClientSecret == "" {
		return fmt.Errorf("backend %q: oauth client_id and client_secret are required", backend.ID)
	}
	issuer, err := validateAbsoluteURL("backend "+backend.ID+" oauth.issuer", oauth.Issuer, false)
	if err != nil {
		return err
	}
	if issuer.RawQuery != "" || issuer.Fragment != "" {
		return fmt.Errorf("backend %q: oauth.issuer must not contain a query or fragment", backend.ID)
	}
	if err := validateScopes("backend "+backend.ID+" oauth.scopes", oauth.Scopes); err != nil {
		return err
	}
	for name := range backend.Headers {
		if strings.EqualFold(name, "Authorization") {
			return fmt.Errorf("backend %q: oauth cannot be combined with an Authorization header", backend.ID)
		}
	}
	return nil
}

func validateScopes(field string, scopes []string) error {
	seen := make(map[string]struct{}, len(scopes))
	for _, scope := range scopes {
		if scope == "" || strings.ContainsAny(scope, " \t\r\n") {
			return fmt.Errorf("%s contains invalid scope %q", field, scope)
		}
		if _, ok := seen[scope]; ok {
			return fmt.Errorf("%s contains duplicate scope %q", field, scope)
		}
		seen[scope] = struct{}{}
	}
	return nil
}

func validateOrigins(origins []string) error {
	seen := make(map[string]struct{}, len(origins))
	for _, origin := range origins {
		u, err := validateAbsoluteURL("server.allowed_origins", origin, false)
		if err != nil {
			return err
		}
		if u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
			return fmt.Errorf("server.allowed_origins entry %q must contain only scheme and authority", origin)
		}
		if _, ok := seen[origin]; ok {
			return fmt.Errorf("server.allowed_origins contains duplicate %q", origin)
		}
		seen[origin] = struct{}{}
	}
	return nil
}

func validateAbsoluteURL(field, raw string, allowInsecure bool) (*url.URL, error) {
	if raw == "" {
		return nil, fmt.Errorf("%s is required", field)
	}
	u, err := url.Parse(raw)
	if err != nil || !u.IsAbs() || u.Host == "" {
		return nil, fmt.Errorf("%s must be an absolute URL", field)
	}
	if u.User != nil {
		return nil, fmt.Errorf("%s must not contain user information", field)
	}
	if u.Scheme == "https" {
		return u, nil
	}
	if u.Scheme == "http" && allowInsecure && isLoopbackHost(u.Hostname()) {
		return u, nil
	}
	return nil, fmt.Errorf("%s must use HTTPS", field)
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func (cfg *Config) MCPPath() string {
	u, _ := url.Parse(cfg.Server.PublicURL)
	return u.Path
}

func (cfg *Config) ResourceMetadataPath() string {
	return "/.well-known/oauth-protected-resource" + cfg.MCPPath()
}

func (cfg *Config) ResourceMetadataURL() string {
	u, _ := url.Parse(cfg.Server.PublicURL)
	u.Path = cfg.ResourceMetadataPath()
	u.RawPath = ""
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}

func (cfg *Config) AllScopes() []string {
	set := make(map[string]struct{})
	for _, backend := range cfg.Backends {
		for _, scope := range backend.RequiredScopes {
			set[scope] = struct{}{}
		}
		for _, rule := range backend.ToolRules {
			for _, scope := range rule.RequiredScopes {
				set[scope] = struct{}{}
			}
		}
	}
	scopes := make([]string, 0, len(set))
	for scope := range set {
		scopes = append(scopes, scope)
	}
	slices.Sort(scopes)
	return scopes
}

func (rule ToolRule) Matches(toolName string) bool {
	matched, err := pathpkg.Match(rule.Match, toolName)
	return err == nil && matched
}

func (backend BackendConfig) RequiredToolScopes(toolName string) []string {
	set := make(map[string]struct{})
	for _, rule := range backend.ToolRules {
		if !rule.Matches(toolName) {
			continue
		}
		for _, scope := range rule.RequiredScopes {
			set[scope] = struct{}{}
		}
	}
	scopes := make([]string, 0, len(set))
	for scope := range set {
		scopes = append(scopes, scope)
	}
	slices.Sort(scopes)
	return scopes
}

func (cfg *Config) Backend(id string) (BackendConfig, bool) {
	for _, backend := range cfg.Backends {
		if backend.ID == id {
			return backend, true
		}
	}
	return BackendConfig{}, false
}

func (cfg *Config) ImmutableEqual(other *Config) error {
	if cfg.Server.Listen != other.Server.Listen {
		return fmt.Errorf("server.listen requires a restart")
	}
	if cfg.Server.PublicURL != other.Server.PublicURL {
		return fmt.Errorf("server.public_url requires a restart")
	}
	if cfg.Auth.Issuer != other.Auth.Issuer {
		return fmt.Errorf("auth.issuer requires a restart")
	}
	return nil
}

func HeaderMap(headers map[string]string) http.Header {
	result := make(http.Header, len(headers))
	for name, value := range headers {
		result.Set(name, value)
	}
	return result
}
