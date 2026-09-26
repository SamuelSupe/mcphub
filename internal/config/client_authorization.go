package config

import (
	"fmt"
	"os"
	"time"
)

type ClientAuthorizationConfig struct {
	Enabled            bool     `yaml:"enabled"`
	RequireClientGrant bool     `yaml:"require_client_grant"`
	MaxGrantTTL        Duration `yaml:"max_grant_ttl"`
	ClientID           string   `yaml:"client_id"`
	ClientSecretEnv    string   `yaml:"client_secret_env"`
}

type ClientToolOption struct {
	Name           string         `json:"name"`
	Effect         string         `json:"effect"`
	RequiredScopes []string       `json:"required_scopes"`
	ResourceRules  []ResourceRule `json:"resource_rules"`
}

type ClientEndpointOption struct {
	ID    string             `json:"id"`
	Tools []ClientToolOption `json:"tools"`
}

func (c ClientAuthorizationConfig) GrantTTL() time.Duration {
	if c.MaxGrantTTL.Duration == 0 {
		return 8 * time.Hour
	}
	return c.MaxGrantTTL.Duration
}

func (cfg *Config) validateClientAuthorization() error {
	c := cfg.ClientAuthorization
	if !c.Enabled {
		if c.RequireClientGrant {
			return fmt.Errorf("client_authorization.enabled is required for strict authorization")
		}
		for _, b := range cfg.Backends {
			if b.RequireClientGrant {
				return fmt.Errorf("backend %s requires client_authorization.enabled", b.ID)
			}
		}
		return nil
	}
	if !cfg.Admin.Enabled || c.ClientID == "" {
		return fmt.Errorf("client_authorization requires managed database storage and an OIDC client_id for the user portal")
	}
	if c.ClientSecretEnv != "" && (!envNamePattern.MatchString(c.ClientSecretEnv) || os.Getenv(c.ClientSecretEnv) == "") {
		return fmt.Errorf("client_authorization.client_secret_env must name a nonempty environment variable")
	}
	if c.GrantTTL() < time.Minute || c.GrantTTL() > 8*time.Hour {
		return fmt.Errorf("client_authorization.max_grant_ttl must be between 1m and 8h")
	}
	return nil
}
