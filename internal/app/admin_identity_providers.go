package app

import (
	"errors"
	"net/http"
	"strings"

	"github.com/SamuelSupe/mcphub/v2/internal/config"
	"github.com/SamuelSupe/mcphub/v2/internal/configstore"
)

type identityProvidersInput struct {
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
	Source   string `json:"source,omitempty"`
	OIDC     struct {
		Enabled      bool                    `json:"enabled"`
		Provider     config.IdentityProvider `json:"provider"`
		ClientSecret *string                 `json:"client_secret,omitempty"`
	} `json:"oidc"`
	LDAP struct {
		config.LDAPConnection
		BindPassword *string `json:"bind_password,omitempty"`
	} `json:"ldap"`
}

func identityProvidersView(c config.EnterpriseConnections, issuer string, managed bool) any {
	type oidcView struct {
		Enabled                bool                    `json:"enabled"`
		Provider               config.IdentityProvider `json:"provider"`
		ClientSecretConfigured bool                    `json:"client_secret_configured"`
	}
	type ldapView struct {
		config.LDAPConnection
		BindPassword           *string `json:"bind_password,omitempty"`
		BindPasswordConfigured bool    `json:"bind_password_configured"`
	}
	c.OIDC.Provider.ClientSecretEnv = ""
	return struct {
		Revision    int64             `json:"revision"`
		CallbackURL string            `json:"callback_url"`
		Managed     bool              `json:"managed"`
		ProviderIDs map[string]string `json:"provider_ids"`
		OIDC        oidcView          `json:"oidc"`
		LDAP        ldapView          `json:"ldap"`
	}{c.Revision, issuer + "/callback", managed, map[string]string{"oidc": c.OIDC.Provider.Namespace(), "ldap": c.LDAP.Namespace()},
		oidcView{c.OIDC.Enabled, c.OIDC.Provider, c.OIDC.ClientSecret != ""},
		ldapView{LDAPConnection: c.LDAP, BindPasswordConfigured: c.LDAP.BindPassword != ""}}
}

func (a *App) serveIdentityProviders(w http.ResponseWriter, r *http.Request, suffix string) {
	if a.sso == nil {
		writeAPIError(w, 409, "identity_storage_required", "请启用内建认证与管理存储后配置身份服务。", "")
		return
	}
	if suffix == "" && r.Method == http.MethodGet {
		c := a.sso.Connections()
		w.Header().Set("ETag", revisionETag(c.Revision))
		writeJSON(w, 200, identityProvidersView(c, a.currentConfig().Auth.Issuer, a.sso.Builtin()))
		return
	}
	if strings.HasPrefix(suffix, "/tests/") && r.Method == http.MethodGet {
		result, err := a.sso.ConnectionTest(configstore.Actor(r.Context()), strings.TrimPrefix(suffix, "/tests/"))
		if err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, 200, result)
		return
	}
	probe, test := suffix == "/probe", suffix == "/test"
	if suffix != "" && !probe && !test {
		http.NotFound(w, r)
		return
	}
	if !a.sso.Builtin() {
		writeAPIError(w, 409, "builtin_required", "UI 身份服务配置需要内建认证，并保留本地恢复管理员。", "")
		return
	}
	if (!probe && !test && r.Method != http.MethodPut) || ((probe || test) && r.Method != http.MethodPost) {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	previous := a.sso.Connections()
	// The first managed record has revision zero, including an inherited YAML
	// connection. Optimistic concurrency protects omitted secret replacements.
	if r.Header.Get("If-Match") != revisionETag(previous.Revision) {
		writeAPIError(w, 409, "revision_conflict", "配置已变化，请刷新后重试。", "")
		return
	}
	var input identityProvidersInput
	if !decodeAdminJSON(w, r, &input) {
		return
	}
	next := config.EnterpriseConnections{OIDC: config.OIDCConnection{Enabled: input.OIDC.Enabled, Provider: input.OIDC.Provider, ClientSecret: previous.OIDC.ClientSecret}, LDAP: input.LDAP.LDAPConnection}
	next.LDAP.BindPassword = previous.LDAP.BindPassword
	if input.OIDC.ClientSecret != nil {
		next.OIDC.ClientSecret = *input.OIDC.ClientSecret
	}
	if input.LDAP.BindPassword != nil {
		next.LDAP.BindPassword = *input.LDAP.BindPassword
	}
	if test {
		next.Revision = previous.Revision
		result, err := a.sso.TestConnection(r.Context(), next, input.Source, configstore.Actor(r.Context()), input.Username, input.Password, r.RemoteAddr)
		if err != nil {
			writeAPIError(w, 400, "identity_test_failed", err.Error(), input.Source)
			return
		}
		writeJSON(w, 200, result)
		return
	}
	if probe {
		if err := a.sso.ProbeConnections(r.Context(), next, input.Source); err != nil {
			writeAPIError(w, 400, "identity_probe_failed", err.Error(), input.Source)
			return
		}
		writeJSON(w, 200, map[string]any{"ok": true, "source": input.Source})
		return
	}
	updated, err := a.sso.UpdateConnections(r.Context(), previous.Revision, next)
	if errors.Is(err, configstore.ErrConflict) {
		writeAPIError(w, 409, "revision_conflict", "配置已变化，请刷新后重试。", "")
		return
	}
	if err != nil {
		writeAPIError(w, 400, "identity_configuration_failed", err.Error(), "")
		return
	}
	a.currentRuntime().hub.PruneIdentityViews(r.Context())
	w.Header().Set("ETag", revisionETag(updated.Revision))
	writeJSON(w, 200, identityProvidersView(updated, a.currentConfig().Auth.Issuer, true))
}
