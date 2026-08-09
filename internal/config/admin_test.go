package config

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateAdminRequiresNumericLoopbackAnd32ByteKey(t *testing.T) {
	const keyEnv = "MCPHUB_ADMIN_TEST_KEY"
	t.Setenv(keyEnv, base64.StdEncoding.EncodeToString(make([]byte, 32)))
	t.Setenv("MCPHUB_ADMIN_SHORT_KEY", base64.StdEncoding.EncodeToString(make([]byte, 31)))

	base := Config{
		Server: ServerConfig{
			Listen:              ":8080",
			PublicURL:           "https://hub.example.com/mcp",
			PageSize:            10,
			RequestTimeout:      Duration{Duration: 1},
			DrainTimeout:        Duration{Duration: 1},
			RefreshInterval:     Duration{Duration: 1},
			MaxRequestBodyBytes: 1,
		},
		Auth: AuthConfig{Issuer: "https://idp.example.com"},
		Admin: AdminConfig{
			Enabled:          true,
			Listen:           "127.0.0.1:8081",
			DatabasePath:     filepath.Join(t.TempDir(), "config.db"),
			EncryptionKeyEnv: keyEnv,
		},
	}
	if err := base.Validate(); err != nil {
		t.Fatalf("valid admin configuration rejected: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*Config)
		want   string
	}{
		{name: "non-loopback address", mutate: func(cfg *Config) { cfg.Admin.Listen = "0.0.0.0:8081" }, want: "admin.listen must use"},
		{name: "hostname address", mutate: func(cfg *Config) { cfg.Admin.Listen = "localhost:8081" }, want: "admin.listen must use"},
		{name: "invalid port", mutate: func(cfg *Config) { cfg.Admin.Listen = "127.0.0.1:0" }, want: "numeric port"},
		{name: "invalid key environment name", mutate: func(cfg *Config) { cfg.Admin.EncryptionKeyEnv = "not-a-variable" }, want: "environment variable name"},
		{name: "missing key", mutate: func(cfg *Config) {
			cfg.Admin.EncryptionKeyEnv = "MCPHUB_ADMIN_MISSING_KEY"
		}, want: "is not set"},
		{name: "wrong key size", mutate: func(cfg *Config) {
			cfg.Admin.EncryptionKeyEnv = "MCPHUB_ADMIN_SHORT_KEY"
		}, want: "32-byte key"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			candidate := base
			tt.mutate(&candidate)
			if err := candidate.Validate(); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Validate() error = %v, want text %q", err, tt.want)
			}
		})
	}
}

func TestLoadStaticIgnoresBootstrapBackendEnvironmentAndResolvesDatabasePath(t *testing.T) {
	const keyEnv = "MCPHUB_STATIC_TEST_KEY"
	t.Setenv(keyEnv, base64.StdEncoding.EncodeToString(make([]byte, 32)))
	path := filepath.Join(t.TempDir(), "config.yaml")
	content := `
server:
  public_url: https://hub.example.com/mcp
auth:
  issuer: https://idp.example.com
admin:
  enabled: true
  database_path: state/config.db
  encryption_key_env: MCPHUB_STATIC_TEST_KEY
backends:
  - id: bootstrap
    url: https://${MCPHUB_BOOTSTRAP_URL}/mcp
`
	if err := os.WriteFile(path, []byte(strings.TrimSpace(content)+"\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	static, err := LoadStatic(path)
	if err != nil {
		t.Fatalf("LoadStatic() error: %v", err)
	}
	if len(static.Backends) != 0 {
		t.Fatalf("LoadStatic() retained bootstrap backends: %#v", static.Backends)
	}
	if want := filepath.Join(filepath.Dir(path), "state", "config.db"); static.Admin.DatabasePath != want {
		t.Fatalf("database path = %q, want %q", static.Admin.DatabasePath, want)
	}
	if _, err := Load(path); err == nil || !strings.Contains(err.Error(), "MCPHUB_BOOTSTRAP_URL") {
		t.Fatalf("Load() error = %v, want missing bootstrap environment", err)
	}
}
