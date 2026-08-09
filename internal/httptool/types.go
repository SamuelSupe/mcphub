package httptool

import (
	"fmt"
	"net/http"
	"net/url"
	pathpkg "path"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/SamuelSupe/mcphub/internal/config"
)

const (
	DefaultMaxResponseBytes = int64(1 << 20)
	MinMaxResponseBytes     = int64(64 << 10)
	MaxMaxResponseBytes     = int64(16 << 20)
)

var (
	idPattern       = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)
	toolNamePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,95}$`)
	headerPattern   = regexp.MustCompile("^[!#$%&'*+\\-.^_`|~0-9A-Za-z]+$")
	pathVariable    = regexp.MustCompile(`\{([A-Za-z0-9_.-]+)\}`)
)

type GroupConfig struct {
	ID                   string              `json:"id"`
	BaseURL              string              `json:"base_url"`
	Enabled              bool                `json:"enabled"`
	RequiredScopes       []string            `json:"required_scopes"`
	ToolRules            []config.ToolRule   `json:"tool_rules"`
	RequestTimeout       time.Duration       `json:"request_timeout_ns"`
	MaxResponseBodyBytes int64               `json:"max_response_body_bytes"`
	Headers              map[string]string   `json:"headers"`
	OAuth                *config.OAuthConfig `json:"oauth,omitempty"`
	Tools                []ToolConfig        `json:"tools"`
}

type ToolConfig struct {
	Name         string         `json:"name"`
	Description  string         `json:"description"`
	Enabled      bool           `json:"enabled"`
	Method       string         `json:"method"`
	Path         string         `json:"path"`
	Parameters   []Parameter    `json:"parameters"`
	BodySchema   map[string]any `json:"body_schema,omitempty"`
	BodyRequired bool           `json:"body_required"`
	OutputSchema map[string]any `json:"output_schema,omitempty"`
	Origin       string         `json:"origin"`
	ImportID     string         `json:"import_id,omitempty"`
}

type Parameter struct {
	Name        string         `json:"name"`
	Argument    string         `json:"argument"`
	In          string         `json:"in"`
	Required    bool           `json:"required"`
	Description string         `json:"description,omitempty"`
	Schema      map[string]any `json:"schema"`
}

func ValidateGroups(groups []GroupConfig, backendIDs []string) error {
	identities := make(map[string]string, len(groups)+len(backendIDs))
	for _, id := range backendIDs {
		identities[strings.ToLower(id)] = id
	}
	for index := range groups {
		group := &groups[index]
		if err := ValidateGroup(group); err != nil {
			return err
		}
		identity := strings.ToLower(group.ID)
		if previous, exists := identities[identity]; exists {
			return fmt.Errorf("tool group %q conflicts case-insensitively with source %q", group.ID, previous)
		}
		identities[identity] = group.ID
	}
	return nil
}

func ValidateGroup(group *GroupConfig) error {
	if !idPattern.MatchString(group.ID) {
		return fmt.Errorf("tool group %q: id must match %s", group.ID, idPattern)
	}
	base, err := url.Parse(group.BaseURL)
	if err != nil || !base.IsAbs() || base.Scheme != "https" || base.Host == "" {
		return fmt.Errorf("tool group %q: base_url must be an absolute HTTPS URL", group.ID)
	}
	if base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return fmt.Errorf("tool group %q: base_url must not contain user information, query, or fragment", group.ID)
	}
	for _, segment := range strings.Split(base.Path, "/") {
		if segment == "." || segment == ".." {
			return fmt.Errorf("tool group %q: base_url path must not contain dot segments", group.ID)
		}
	}
	if strings.Contains(base.Path, `\`) {
		return fmt.Errorf("tool group %q: base_url path must not contain backslashes", group.ID)
	}
	if group.RequestTimeout <= 0 {
		return fmt.Errorf("tool group %q: request_timeout must be positive", group.ID)
	}
	if group.MaxResponseBodyBytes == 0 {
		group.MaxResponseBodyBytes = DefaultMaxResponseBytes
	}
	if group.MaxResponseBodyBytes < MinMaxResponseBytes || group.MaxResponseBodyBytes > MaxMaxResponseBytes {
		return fmt.Errorf("tool group %q: max_response_body_bytes must be between %d and %d", group.ID, MinMaxResponseBytes, MaxMaxResponseBytes)
	}
	if err := validateScopes("tool group "+group.ID+" required_scopes", group.RequiredScopes); err != nil {
		return err
	}
	seenRules := make(map[string]struct{}, len(group.ToolRules))
	for _, rule := range group.ToolRules {
		if rule.Match == "" {
			return fmt.Errorf("tool group %q: tool rule match is required", group.ID)
		}
		if _, err := pathpkg.Match(rule.Match, ""); err != nil {
			return fmt.Errorf("tool group %q: invalid tool rule match %q", group.ID, rule.Match)
		}
		if _, exists := seenRules[rule.Match]; exists {
			return fmt.Errorf("tool group %q: duplicate tool rule match %q", group.ID, rule.Match)
		}
		seenRules[rule.Match] = struct{}{}
		if len(rule.RequiredScopes) == 0 {
			return fmt.Errorf("tool group %q: tool rule scopes are required", group.ID)
		}
		if err := validateScopes("tool group "+group.ID+" tool rule", rule.RequiredScopes); err != nil {
			return err
		}
	}
	canonicalHeaders := make(map[string]string, len(group.Headers))
	for name, value := range group.Headers {
		if !headerPattern.MatchString(name) {
			return fmt.Errorf("tool group %q: invalid header name %q", group.ID, name)
		}
		canonical := http.CanonicalHeaderKey(name)
		if previous, exists := canonicalHeaders[canonical]; exists {
			return fmt.Errorf("tool group %q: headers %q and %q are duplicates", group.ID, previous, name)
		}
		canonicalHeaders[canonical] = name
		if forbiddenGroupHeader(canonical) || strings.ContainsAny(value, "\r\n") {
			return fmt.Errorf("tool group %q: header %q is not allowed", group.ID, name)
		}
	}
	if group.OAuth != nil {
		if group.OAuth.Type != "client_credentials" || group.OAuth.ClientID == "" || group.OAuth.ClientSecret == "" {
			return fmt.Errorf("tool group %q: oauth client_credentials values are required", group.ID)
		}
		issuer, err := url.Parse(group.OAuth.Issuer)
		if err != nil || !issuer.IsAbs() || issuer.Scheme != "https" || issuer.Host == "" || issuer.User != nil || issuer.RawQuery != "" || issuer.Fragment != "" {
			return fmt.Errorf("tool group %q: oauth.issuer must be an absolute HTTPS URL without credentials, query, or fragment", group.ID)
		}
		if err := validateScopes("tool group "+group.ID+" oauth.scopes", group.OAuth.Scopes); err != nil {
			return err
		}
		for name := range group.Headers {
			if strings.EqualFold(name, "Authorization") {
				return fmt.Errorf("tool group %q: oauth cannot be combined with an Authorization header", group.ID)
			}
		}
	}
	seenTools := make(map[string]string, len(group.Tools))
	for index := range group.Tools {
		tool := &group.Tools[index]
		if err := ValidateTool(group.ID, tool); err != nil {
			return err
		}
		for _, parameter := range tool.Parameters {
			if parameter.In != "header" {
				continue
			}
			for name := range group.Headers {
				if strings.EqualFold(name, parameter.Name) {
					return fmt.Errorf("tool group %q tool %q: header parameter %q conflicts with a shared header", group.ID, tool.Name, parameter.Name)
				}
			}
		}
		definition, err := buildDefinition(*group, *tool)
		if err != nil {
			return err
		}
		if err := validateDefinition(definition); err != nil {
			return fmt.Errorf("tool group %s tool %s: %w", group.ID, tool.Name, err)
		}
		identity := strings.ToLower(tool.Name)
		if previous, exists := seenTools[identity]; exists {
			return fmt.Errorf("tool group %q: tool %q conflicts with %q", group.ID, tool.Name, previous)
		}
		seenTools[identity] = tool.Name
	}
	return nil
}

func ValidateTool(groupID string, tool *ToolConfig) error {
	if !toolNamePattern.MatchString(tool.Name) {
		return fmt.Errorf("tool group %q: tool name %q must match %s", groupID, tool.Name, toolNamePattern)
	}
	tool.Method = strings.ToUpper(tool.Method)
	switch tool.Method {
	case http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodHead:
	default:
		return fmt.Errorf("tool group %q tool %q: unsupported HTTP method", groupID, tool.Name)
	}
	if !strings.HasPrefix(tool.Path, "/") || strings.ContainsAny(tool.Path, "?#%\r\n\t ") {
		return fmt.Errorf("tool group %q tool %q: path must be a relative absolute-path without query or fragment", groupID, tool.Name)
	}
	for _, segment := range strings.Split(tool.Path, "/") {
		if segment == "." || segment == ".." {
			return fmt.Errorf("tool group %q tool %q: path must not contain dot segments", groupID, tool.Name)
		}
	}
	if strings.Contains(tool.Path, `\`) {
		return fmt.Errorf("tool group %q tool %q: path must not contain backslashes", groupID, tool.Name)
	}
	arguments := make(map[string]struct{}, len(tool.Parameters)+1)
	parameters := make(map[string]struct{}, len(tool.Parameters))
	pathParameters := make(map[string]struct{})
	for _, parameter := range tool.Parameters {
		if parameter.Name == "" || parameter.Argument == "" {
			return fmt.Errorf("tool group %q tool %q: parameter name and argument are required", groupID, tool.Name)
		}
		switch parameter.In {
		case "path", "query", "header":
		default:
			return fmt.Errorf("tool group %q tool %q: parameter location %q is unsupported", groupID, tool.Name, parameter.In)
		}
		if _, exists := arguments[parameter.Argument]; exists || parameter.Argument == "body" {
			return fmt.Errorf("tool group %q tool %q: duplicate or reserved argument %q", groupID, tool.Name, parameter.Argument)
		}
		arguments[parameter.Argument] = struct{}{}
		parameterIdentity := parameter.In + "\x00" + parameter.Name
		if parameter.In == "header" {
			parameterIdentity = parameter.In + "\x00" + strings.ToLower(parameter.Name)
		}
		if _, exists := parameters[parameterIdentity]; exists {
			return fmt.Errorf("tool group %q tool %q: duplicate %s parameter %q", groupID, tool.Name, parameter.In, parameter.Name)
		}
		parameters[parameterIdentity] = struct{}{}
		if parameter.In == "path" {
			if !parameter.Required {
				return fmt.Errorf("tool group %q tool %q: path parameter %q must be required", groupID, tool.Name, parameter.Name)
			}
			pathParameters[parameter.Name] = struct{}{}
		}
		if parameter.In == "header" {
			canonical := http.CanonicalHeaderKey(parameter.Name)
			if !headerPattern.MatchString(parameter.Name) || forbiddenHeader(canonical) {
				return fmt.Errorf("tool group %q tool %q: header parameter %q is not allowed", groupID, tool.Name, parameter.Name)
			}
		}
		if len(parameter.Schema) == 0 {
			return fmt.Errorf("tool group %q tool %q: parameter %q schema is required", groupID, tool.Name, parameter.Name)
		}
	}
	for _, match := range pathVariable.FindAllStringSubmatch(tool.Path, -1) {
		if _, ok := pathParameters[match[1]]; !ok {
			return fmt.Errorf("tool group %q tool %q: path variable %q has no path parameter", groupID, tool.Name, match[1])
		}
	}
	withoutVariables := pathVariable.ReplaceAllString(tool.Path, "")
	if strings.ContainsAny(withoutVariables, "{}") {
		return fmt.Errorf("tool group %q tool %q: path contains an invalid variable", groupID, tool.Name)
	}
	for name := range pathParameters {
		if !strings.Contains(tool.Path, "{"+name+"}") {
			return fmt.Errorf("tool group %q tool %q: path parameter %q is unused", groupID, tool.Name, name)
		}
	}
	if tool.BodyRequired && len(tool.BodySchema) == 0 {
		return fmt.Errorf("tool group %q tool %q: required body schema is missing", groupID, tool.Name)
	}
	return nil
}

func (group GroupConfig) RequiredToolScopes(name string) []string {
	set := make(map[string]struct{})
	for _, rule := range group.ToolRules {
		if !rule.Matches(name) {
			continue
		}
		for _, scope := range rule.RequiredScopes {
			set[scope] = struct{}{}
		}
	}
	result := make([]string, 0, len(set))
	for scope := range set {
		result = append(result, scope)
	}
	slices.Sort(result)
	return result
}

func validateScopes(field string, values []string) error {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value == "" || strings.ContainsAny(value, " \t\r\n") {
			return fmt.Errorf("%s contains invalid scope %q", field, value)
		}
		if _, exists := seen[value]; exists {
			return fmt.Errorf("%s contains duplicate scope %q", field, value)
		}
		seen[value] = struct{}{}
	}
	return nil
}

func forbiddenHeader(name string) bool {
	switch name {
	case "Authorization", "Cookie", "Host", "Connection", "Content-Length", "Content-Type", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization", "Proxy-Connection", "Te", "Trailer", "Transfer-Encoding", "Upgrade":
		return true
	default:
		return false
	}
}

func forbiddenGroupHeader(name string) bool {
	switch name {
	case "Host", "Connection", "Content-Length", "Content-Type", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization", "Proxy-Connection", "Te", "Trailer", "Transfer-Encoding", "Upgrade":
		return true
	default:
		return false
	}
}
