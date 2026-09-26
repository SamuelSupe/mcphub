package config

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"net/url"
)

type PolicyChangeSettings struct {
	Enabled        bool     `yaml:"enabled"`
	RequiredScopes []string `yaml:"required_scopes"`
	Subjects       []string `yaml:"subjects"`
	RequireStepUp  bool     `yaml:"require_step_up"`
}

func (s PolicyChangeSettings) Scopes() []string {
	if len(s.RequiredScopes) == 0 {
		return []string{"mcphub:security"}
	}
	return s.RequiredScopes
}

type ApprovalWebhook struct {
	URL    string `yaml:"url"`
	Secret string `yaml:"secret"`
}

type ApprovalArchive struct {
	URL        string `yaml:"url"`
	SigningKey string `yaml:"signing_key"`
	KeyID      string `yaml:"key_id"`
}

func (s ApprovalSettings) validateGovernance() error {
	if err := validateScopes("admin.approvals.policy_changes.required_scopes", s.PolicyChanges.Scopes()); err != nil {
		return err
	}
	if len(s.PolicyChanges.Subjects) > 0 {
		if err := (ToolApprovalPolicy{Approvers: []ApprovalGrant{{Subjects: s.PolicyChanges.Subjects}}}).Validate(); err != nil {
			return err
		}
	}
	for _, endpoint := range []string{s.Notifications.URL, s.AuditArchive.URL} {
		if endpoint == "" {
			continue
		}
		u, err := url.Parse(endpoint)
		if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" || u.RawQuery != "" {
			return fmt.Errorf("approval delivery URLs require HTTPS without credentials, query or fragment")
		}
	}
	if s.Notifications.URL != "" && len(s.Notifications.Secret) < 32 {
		return fmt.Errorf("approval notifications.secret requires at least 32 bytes")
	}
	if s.AuditArchive.URL != "" {
		key, err := base64.StdEncoding.DecodeString(s.AuditArchive.SigningKey)
		if err != nil || len(key) != ed25519.PrivateKeySize || s.AuditArchive.KeyID == "" || len(s.AuditArchive.KeyID) > 128 {
			return fmt.Errorf("approval audit_archive requires a Base64 Ed25519 private key and key_id")
		}
		if !bytes.Equal(key, ed25519.NewKeyFromSeed(key[:ed25519.SeedSize])) {
			return fmt.Errorf("invalid Ed25519 signing key")
		}
	}
	return nil
}
