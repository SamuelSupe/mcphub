package config

import (
	"fmt"
	"net/url"
	"slices"
)

const LocalIdentityProvider = "mcphub:local"
const LocalMFAACR = "urn:mcphub:auth:password-totp"

func (a AuthConfig) Builtin() bool { return a.Mode == "builtin" }

func (cfg *Config) configureAuthentication() error {
	if cfg.Auth.Mode == "" {
		if cfg.Auth.Issuer == "" {
			cfg.Auth.Mode = "builtin"
		} else {
			cfg.Auth.Mode = "external"
		}
	}
	if cfg.Auth.Mode != "builtin" && cfg.Auth.Mode != "external" {
		return fmt.Errorf("auth.mode must be builtin or external")
	}
	if !cfg.Auth.Builtin() {
		return nil
	}
	if !cfg.Admin.Enabled {
		return fmt.Errorf("built-in accounts require admin storage")
	}
	public, _ := url.Parse(cfg.Server.PublicURL)
	issuer := public.Scheme + "://" + public.Host + "/sso"
	if cfg.Auth.Issuer != "" && cfg.Auth.Issuer != issuer {
		return fmt.Errorf("built-in auth.issuer is derived from server.public_url")
	}
	cfg.Auth.Issuer = issuer
	if cfg.Auth.SSO == nil {
		cfg.Auth.SSO = &SSOConfig{}
	}
	if cfg.Admin.ClientID == "" {
		cfg.Admin.ClientID = "mcphub-admin"
	}
	if !cfg.Admin.Remote() {
		cfg.Admin.PublicURL = "http://" + cfg.Admin.Listen
	}
	if cfg.ClientAuthorization.ClientID == "" {
		cfg.ClientAuthorization.ClientID = "mcphub-portal"
	}
	clients := []SSOClient{
		{ID: "mcpbridge", RedirectURIs: []string{"http://127.0.0.1/oauth/callback"}, Resources: []string{cfg.Server.PublicURL}},
		{ID: "mcpbridge-admin", RedirectURIs: []string{"http://127.0.0.1/oauth/callback"}, Resources: []string{cfg.Admin.PublicURL}},
		{ID: cfg.Admin.ClientID, RedirectURIs: []string{cfg.Admin.PublicURL + "/auth/callback"}, Resources: []string{cfg.Admin.PublicURL}},
		{ID: cfg.ClientAuthorization.ClientID, RedirectURIs: []string{public.Scheme + "://" + public.Host + "/client-auth/auth/callback"}, Resources: []string{cfg.Server.PublicURL}},
	}
	for _, c := range clients {
		if !slices.ContainsFunc(cfg.Auth.SSO.Clients, func(existing SSOClient) bool { return existing.ID == c.ID }) {
			cfg.Auth.SSO.Clients = append(cfg.Auth.SSO.Clients, c)
		}
	}
	if len(cfg.Admin.Approvals.StepUpACRValues) == 0 {
		cfg.Admin.Approvals.StepUpACRValues = []string{LocalMFAACR}
	}
	return nil
}
