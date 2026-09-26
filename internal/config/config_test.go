package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadExpandsEnvironmentAndAppliesDefaults(t *testing.T) {
	t.Setenv("MCPHUB_TEST_KEY", "secret-value")
	t.Setenv("MCPHUB_TEST_TOOL_MATCH", "delete_*")
	t.Setenv("MCPHUB_TEST_TOOL_SCOPE", "mcp:alpha:dangerous")
	path := writeConfig(t, `
server:
  public_url: https://hub.example.com/mcp
auth:
  issuer: https://idp.example.com
backends:
  - id: alpha
    url: https://alpha.example.com/mcp
    required_scopes: [mcp:alpha]
    tool_rules:
      - match: "${MCPHUB_TEST_TOOL_MATCH}"
        required_scopes: ["${MCPHUB_TEST_TOOL_SCOPE}"]
    headers:
      X-API-Key: ${MCPHUB_TEST_KEY}
`)

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if got, want := cfg.Server.Listen, ":8080"; got != want {
		t.Fatalf("listen = %q, want %q", got, want)
	}
	if got, want := cfg.Server.PageSize, 1000; got != want {
		t.Fatalf("page size = %d, want %d", got, want)
	}
	if got, want := cfg.Server.MaxRequestBodyBytes, int64(4<<20); got != want {
		t.Fatalf("max request body = %d, want %d", got, want)
	}
	if got, want := cfg.Backends[0].Headers["X-API-Key"], "secret-value"; got != want {
		t.Fatalf("expanded header = %q, want %q", got, want)
	}
	if got, want := cfg.Backends[0].RequestTimeout.Duration, 60*time.Second; got != want {
		t.Fatalf("backend timeout = %s, want %s", got, want)
	}
	if got, want := cfg.Backends[0].ToolRules[0].Match, "delete_*"; got != want {
		t.Fatalf("expanded tool match = %q, want %q", got, want)
	}
	if got, want := cfg.Backends[0].ToolRules[0].RequiredScopes[0], "mcp:alpha:dangerous"; got != want {
		t.Fatalf("expanded tool scope = %q, want %q", got, want)
	}
	if got, want := strings.Join(cfg.AllScopes(), ","), "mcp:alpha,mcp:alpha:dangerous"; got != want {
		t.Fatalf("all scopes = %q, want %q", got, want)
	}
	if got, want := cfg.ResourceMetadataPath(), "/.well-known/oauth-protected-resource/mcp"; got != want {
		t.Fatalf("metadata path = %q, want %q", got, want)
	}
}

func TestToolEffectDefaultsToApprovalAndWriteOverridesRead(t *testing.T) {
	rules := []ToolRule{{Match: "project.*", Effect: "read"}, {Match: "*.update", Effect: "write"}}
	if ToolEffect(rules, "project.update") != "write" || ToolEffect(rules, "project.get") != "read" || ToolEffect(rules, "unknown") != "unknown" {
		t.Fatal("tool effect weakened the approval boundary")
	}
	if err := ValidateToolRules("test", []ToolRule{{Match: "*", Effect: "auto"}}); err == nil {
		t.Fatal("invalid effect accepted")
	}
}

func TestLoadRejectsConfigurationBoundaryViolations(t *testing.T) {
	valid := `
server:
  public_url: https://hub.example.com/mcp
auth:
  issuer: https://idp.example.com
backends:
  - id: alpha
    url: https://alpha.example.com/mcp
`
	tests := []struct {
		name    string
		content string
		want    string
	}{
		{name: "negative rate", content: valid + "    rate_limit: {requests_per_second: -1}\n", want: "requests_per_second"},
		{name: "nonfinite rate", content: valid + "    rate_limit: {requests_per_second: .inf}\n", want: "requests_per_second"},
		{name: "burst without rate", content: valid + "    rate_limit: {burst: 10}\n", want: "requires positive"},
		{name: "negative concurrency", content: valid + "    rate_limit: {max_concurrent: -1}\n", want: "nonnegative"},
		{
			name:    "missing environment variable",
			content: strings.Replace(valid, "https://alpha.example.com/mcp", "https://${MCPHUB_NOT_SET}/mcp", 1),
			want:    "MCPHUB_NOT_SET",
		},
		{
			name: "duplicate backend ID",
			content: valid + `
  - id: alpha
    url: https://other.example.com/mcp
`,
			want: "duplicate id",
		},
		{
			name:    "remote insecure backend",
			content: strings.Replace(valid, "url: https://alpha.example.com/mcp", "url: http://alpha.example.com/mcp\n    allow_insecure_http: true", 1),
			want:    "must use HTTPS",
		},
		{
			name:    "reserved public endpoint",
			content: strings.Replace(valid, "https://hub.example.com/mcp", "https://hub.example.com/readyz", 1),
			want:    "is reserved",
		},
		{
			name:    "encoded public endpoint path",
			content: strings.Replace(valid, "https://hub.example.com/mcp", "https://hub.example.com/%6dcp", 1),
			want:    "must not use percent-encoded",
		},
		{
			name: "conflicting backend authentication",
			content: strings.Replace(valid, "    url: https://alpha.example.com/mcp", `    url: https://alpha.example.com/mcp
    headers:
      Authorization: Bearer static
    oauth:
      type: client_credentials
      issuer: https://auth.example.com
      client_id: client
      client_secret: secret`, 1),
			want: "cannot be combined",
		},
		{
			name: "case-insensitive duplicate headers",
			content: strings.Replace(valid, "    url: https://alpha.example.com/mcp", `    url: https://alpha.example.com/mcp
    headers:
      X-API-Key: one
      x-api-key: two`, 1),
			want: "duplicates",
		},
		{
			name:    "transport-managed header",
			content: strings.Replace(valid, "    url: https://alpha.example.com/mcp", "    url: https://alpha.example.com/mcp\n    headers:\n      Content-Type: text/plain", 1),
			want:    "managed by the HTTP or MCP transport",
		},
		{
			name:    "empty tool rule match",
			content: strings.Replace(valid, "    url: https://alpha.example.com/mcp", "    url: https://alpha.example.com/mcp\n    tool_rules:\n      - match: \"\"\n        required_scopes: [mcp:alpha:write]", 1),
			want:    "match is required",
		},
		{
			name:    "invalid tool rule glob",
			content: strings.Replace(valid, "    url: https://alpha.example.com/mcp", "    url: https://alpha.example.com/mcp\n    tool_rules:\n      - match: \"[broken\"\n        required_scopes: [mcp:alpha:write]", 1),
			want:    "match \"[broken\" is invalid",
		},
		{
			name:    "empty tool rule scopes",
			content: strings.Replace(valid, "    url: https://alpha.example.com/mcp", "    url: https://alpha.example.com/mcp\n    tool_rules:\n      - match: delete_*\n        required_scopes: []", 1),
			want:    "requires required_scopes, resource_rules, effect or approval",
		},
		{
			name: "duplicate tool rule match",
			content: strings.Replace(valid, "    url: https://alpha.example.com/mcp", `    url: https://alpha.example.com/mcp
    tool_rules:
      - match: delete_*
        required_scopes: [mcp:alpha:write]
      - match: delete_*
        required_scopes: [mcp:alpha:dangerous]`, 1),
			want: "duplicate match",
		},
		{
			name: "duplicate tool rule scope",
			content: strings.Replace(valid, "    url: https://alpha.example.com/mcp", `    url: https://alpha.example.com/mcp
    tool_rules:
      - match: delete_*
        required_scopes: [mcp:alpha:write, mcp:alpha:write]`, 1),
			want: "duplicate scope",
		},
		{
			name: "invalid tool rule scope",
			content: strings.Replace(valid, "    url: https://alpha.example.com/mcp", `    url: https://alpha.example.com/mcp
    tool_rules:
      - match: delete_*
        required_scopes: ["mcp:alpha write"]`, 1),
			want: "invalid scope",
		},
		{
			name:    "proxy credential header",
			content: strings.Replace(valid, "    url: https://alpha.example.com/mcp", "    url: https://alpha.example.com/mcp\n    headers:\n      Proxy-Authorization: Basic secret", 1),
			want:    "managed by the HTTP or MCP transport",
		},
		{
			name:    "unknown field",
			content: strings.Replace(valid, "  public_url:", "  unexpected: true\n  public_url:", 1),
			want:    "field unexpected not found",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(writeConfig(t, tt.content))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Load() error = %v, want error containing %q", err, tt.want)
			}
		})
	}
}

func TestRequiredToolScopesUnionsAllMatchingRules(t *testing.T) {
	backend := BackendConfig{ToolRules: []ToolRule{
		{Match: "delete_*", RequiredScopes: []string{"mcp:write", "mcp:dangerous"}},
		{Match: "delete_[a-z]?", RequiredScopes: []string{"mcp:audit", "mcp:write"}},
		{Match: "Delete_*", RequiredScopes: []string{"mcp:uppercase"}},
	}}

	if got, want := strings.Join(backend.RequiredToolScopes("delete_ab"), ","), "mcp:audit,mcp:dangerous,mcp:write"; got != want {
		t.Fatalf("matching tool scopes = %q, want %q", got, want)
	}
	if got := backend.RequiredToolScopes("delete_abc"); len(got) != 2 || got[0] != "mcp:dangerous" || got[1] != "mcp:write" {
		t.Fatalf("full-string glob scopes = %v", got)
	}
	if got := backend.RequiredToolScopes("Delete_ab"); len(got) != 1 || got[0] != "mcp:uppercase" {
		t.Fatalf("case-sensitive glob scopes = %v", got)
	}
	if got := backend.RequiredToolScopes("read_ab"); len(got) != 0 {
		t.Fatalf("unmatched tool scopes = %v, want none", got)
	}
}

func TestBackendIDsRejectCaseInsensitiveCollisionsButPermitUppercase(t *testing.T) {
	base := `
server:
  public_url: https://hub.example.com/mcp
auth:
  issuer: https://idp.example.com
backends:
  - id: Alpha
    url: https://alpha.example.com/mcp
`
	_, err := Load(writeConfig(t, base+`
  - id: alpha
    url: https://other.example.com/mcp
`))
	if err == nil || !strings.Contains(err.Error(), "conflicts case-insensitively") {
		t.Fatalf("case-insensitive backend collision error = %v", err)
	}
	cfg, err := Load(writeConfig(t, base))
	if err != nil {
		t.Fatalf("single uppercase backend ID rejected: %v", err)
	}
	if got, want := cfg.Backends[0].ID, "Alpha"; got != want {
		t.Fatalf("uppercase backend ID = %q, want %q", got, want)
	}
}

func TestImmutableEqualSeparatesReloadableAndRestartOnlyFields(t *testing.T) {
	base := &Config{
		Server: ServerConfig{Listen: ":8080", PublicURL: "https://hub.example.com/mcp"},
		Auth:   AuthConfig{Issuer: "https://idp.example.com"},
		Backends: []BackendConfig{{
			ID: "alpha", URL: "https://alpha.example.com/mcp",
		}},
	}

	mutable := *base
	mutable.Server.AllowedOrigins = []string{"https://console.example.com"}
	mutable.Backends = []BackendConfig{{ID: "beta", URL: "https://beta.example.com/mcp"}}
	if err := base.ImmutableEqual(&mutable); err != nil {
		t.Fatalf("mutable reload rejected: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*Config)
		want   string
	}{
		{"listen", func(cfg *Config) { cfg.Server.Listen = ":9090" }, "server.listen"},
		{"public URL", func(cfg *Config) { cfg.Server.PublicURL = "https://new.example.com/mcp" }, "server.public_url"},
		{"issuer", func(cfg *Config) { cfg.Auth.Issuer = "https://new-idp.example.com" }, "auth.issuer"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			candidate := *base
			tt.mutate(&candidate)
			if err := base.ImmutableEqual(&candidate); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("ImmutableEqual() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(strings.TrimSpace(content)+"\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func TestResourcePolicyRejectsMissingOrOutOfRangeArguments(t *testing.T) {
	rules := []ToolRule{
		{Match: "*", ResourceRules: []ResourceRule{{Argument: "/project", AllowedValues: []string{"work", "other"}}}},
		{Match: "search", ResourceRules: []ResourceRule{{Argument: "/project", AllowedValues: []string{"work"}}, {Argument: "/body/db~1name~0", AllowedValues: []string{"reports"}}}},
	}
	if err := ValidateToolRules("test", rules); err != nil {
		t.Fatal(err)
	}
	resources := ToolResourceRules(rules, "search")
	for _, tc := range []struct {
		name, args string
		allowed    bool
	}{
		{"exact", `{"project":"work","body":{"db/name~":"reports"}}`, true},
		{"array", `{"project":["work","work"],"body":{"db/name~":"reports"}}`, true},
		{"overlapping rules", `{"project":"other","body":{"db/name~":"reports"}}`, false},
		{"mixed array", `{"project":["work","other"],"body":{"db/name~":"reports"}}`, false},
		{"empty array", `{"project":[],"body":{"db/name~":"reports"}}`, false},
		{"wrong type", `{"project":42,"body":{"db/name~":"reports"}}`, false},
		{"null", `{"project":null,"body":{"db/name~":"reports"}}`, false},
		{"missing", `{"body":{"db/name~":"reports"}}`, false},
		{"nested missing", `{"project":"work","body":{}}`, false},
		{"malformed", `{"project":"work"`, false},
		{"trailing", `{} {}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := CheckToolResources(resources, json.RawMessage(tc.args))
			if (err == nil) != tc.allowed {
				t.Fatalf("authorization: %v; want allowed=%v", err, tc.allowed)
			}
		})
	}
	normalized, err := CheckToolResources(resources, json.RawMessage(`{"project":"other","project":"work","body":{"db/name~":"reports"},"n":9007199254740993}`))
	if err != nil || strings.Count(string(normalized), `"project"`) != 1 || !strings.Contains(string(normalized), `9007199254740993`) {
		t.Fatalf("normalized arguments: %s, %v", normalized, err)
	}
	for _, pointer := range []string{"project", "/bad~", "/bad~2"} {
		invalid := []ToolRule{{Match: "search", ResourceRules: []ResourceRule{{Argument: pointer, AllowedValues: []string{"work"}}}}}
		if err := ValidateToolRules("test", invalid); err == nil {
			t.Fatalf("accepted invalid pointer %q", pointer)
		}
	}
	if err := ValidateToolRules("test", []ToolRule{{Match: "search", ResourceRules: []ResourceRule{{Argument: "/project"}}}}); err == nil {
		t.Fatal("accepted an empty resource allowlist")
	}
	for _, names := range [][]string{{"*"}, {"search", "search"}, {""}} {
		if err := validatePublishedTools(names); err == nil {
			t.Fatalf("accepted publication list: %v", names)
		}
	}
}

func TestApprovalPolicyIntersectsReviewerAndResourceGrants(t *testing.T) {
	policies := []ToolApprovalPolicy{
		{Approvers: []ApprovalGrant{{Subjects: []string{"reviewer"}, Resources: []ResourceRule{{Argument: "/project", AllowedValues: []string{"work"}}}}}, RequireDifferentReviewer: true},
		{Approvers: []ApprovalGrant{{Subjects: []string{"reviewer"}, Resources: []ResourceRule{{Argument: "/environment", AllowedValues: []string{"production"}}}}}},
	}
	for _, tc := range []struct {
		reviewer, requester, arguments string
		allowed                        bool
	}{
		{"reviewer", "requester", `{"project":"work","environment":"production"}`, true},
		{"outsider", "requester", `{"project":"work","environment":"production"}`, false},
		{"reviewer", "reviewer", `{"project":"work","environment":"production"}`, false},
		{"reviewer", "requester", `{"project":"other","environment":"production"}`, false},
		{"reviewer", "requester", `{"project":"work","environment":"test"}`, false},
	} {
		if got := CanReviewApproval(policies, tc.reviewer, tc.requester, []byte(tc.arguments), true); got != tc.allowed {
			t.Fatalf("reviewer/resource boundary: %+v allowed=%v", tc, got)
		}
	}
	rules := []ToolRule{{Match: "*", Effect: "read"}, {Match: "update", Approval: &policies[0]}}
	if ToolEffect(rules, "update") != "write" {
		t.Fatal("approval policy was bypassed by broader read classification")
	}
}

func TestRiskPolicyCannotBeWeakenedByArguments(t *testing.T) {
	policy := ToolApprovalPolicy{RiskRules: []ApprovalRiskRule{{Resources: []ResourceRule{{Argument: "/projects", AllowedValues: []string{"production"}}}, RequiredApprovals: 2, RequireStepUp: true}}}
	for _, body := range []string{`{}`, `{"projects":null}`, `{"projects":1}`, `{"projects":[]}`, `{"projects":["production",1]}`} {
		if _, err := ResolveApprovalPolicies([]ToolApprovalPolicy{policy}, []byte(body)); err == nil {
			t.Errorf("ambiguous risk input accepted: %s", body)
		}
	}
	for _, body := range []string{`{"projects":"production"}`, `{"projects":["development","production"]}`} {
		result, err := ResolveApprovalPolicies([]ToolApprovalPolicy{policy}, []byte(body))
		if err != nil || ApprovalQuorum(result) != 2 || !ApprovalNeedsStepUp(result) || CanReviewApproval(result, "caller", "caller", []byte(body), true) {
			t.Fatalf("high-risk policy weakened for %s: %v", body, err)
		}
	}
	result, err := ResolveApprovalPolicies([]ToolApprovalPolicy{policy}, []byte(`{"projects":"development"}`))
	if err != nil || ApprovalQuorum(result) != 1 {
		t.Fatal("nonmatching condition raised quorum")
	}
	policy.RequiredApprovals = 2
	result, err = ResolveApprovalPolicies([]ToolApprovalPolicy{policy}, []byte(`{"projects":"development"}`))
	if err != nil || ApprovalQuorum(result) != 2 {
		t.Fatal("nonmatching condition lowered base quorum")
	}
}
