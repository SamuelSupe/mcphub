package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadExpandsEnvironmentAndAppliesDefaults(t *testing.T) {
	t.Setenv("MCPHUB_TEST_KEY", "secret-value")
	path := writeConfig(t, `
server:
  public_url: https://hub.example.com/mcp
auth:
  issuer: https://idp.example.com
backends:
  - id: alpha
    url: https://alpha.example.com/mcp
    required_scopes: [mcp:alpha]
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
	if got, want := cfg.ResourceMetadataPath(), "/.well-known/oauth-protected-resource/mcp"; got != want {
		t.Fatalf("metadata path = %q, want %q", got, want)
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
