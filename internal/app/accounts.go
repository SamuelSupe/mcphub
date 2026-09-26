package app

import (
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	mcpauth "github.com/modelcontextprotocol/go-sdk/auth"
	"golang.org/x/oauth2"

	"github.com/SamuelSupe/mcphub/v2/internal/config"
	"github.com/SamuelSupe/mcphub/v2/internal/configstore"
	"github.com/SamuelSupe/mcphub/v2/internal/sso"
	"github.com/SamuelSupe/mcphub/v2/internal/upstream"
)

type accountView struct {
	Endpoint   string    `json:"endpoint"`
	UID        string    `json:"uid"`
	Status     string    `json:"status"`
	Account    string    `json:"account,omitempty"`
	Revision   int64     `json:"revision"`
	OAuth      bool      `json:"oauth"`
	CanConnect bool      `json:"can_connect"`
	Connected  bool      `json:"connected"`
	Scopes     []string  `json:"scopes,omitempty"`
	ExpiresAt  time.Time `json:"expires_at"`
	Renewable  bool      `json:"renewable"`
}

func accountEndpoint(c config.BackendConfig) upstream.Endpoint {
	return upstream.Endpoint{ID: c.ID, UID: c.EndpointUID, URL: c.URL, Credentials: c.Credentials}
}

func personalBackend(rt *runtime, info *mcpauth.TokenInfo, id string) (config.BackendConfig, bool) {
	c, ok := rt.cfg.Backend(id)
	if !ok || c.Credentials == nil || c.Credentials.Mode != "personal" {
		return c, false
	}
	missing, known := rt.hub.MissingScopes(id, info.Scopes)
	if !known || len(missing) > 0 {
		return c, false
	}
	if p := sso.Identity(info); p != nil && !p.Permissions.AllowsEndpoint(id) {
		return c, false
	}
	return c, true
}

func (a *App) serveAccounts(w http.ResponseWriter, req *http.Request, rt *runtime, info *mcpauth.TokenInfo, session *adminSession, path string) {
	issuer, _ := info.Extra["issuer"].(string)
	if issuer != rt.cfg.Auth.Issuer || info.UserID == "" || session == nil {
		writeAccountError(w, upstream.ErrConnect)
		return
	}
	if a.credentials == nil {
		if path == "" && req.Method == http.MethodGet {
			writeJSON(w, 200, map[string]any{"accounts": []accountView{}})
			return
		}
		writeAccountError(w, upstream.ErrUnavailable)
		return
	}
	if path == "" && req.Method == http.MethodGet {
		a.listAccounts(w, req, rt, info, issuer)
		return
	}
	if path == "/callback" && req.Method == http.MethodGet {
		a.accountCallback(w, req, rt, info, session, issuer)
		return
	}
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	if len(parts) != 2 {
		http.NotFound(w, req)
		return
	}
	if parts[1] == "disconnect" && req.Method == http.MethodPost {
		var input struct {
			Revision int64 `json:"revision"`
		}
		if !decodeAdminJSON(w, req, &input) {
			return
		}
		b, err := a.store.CredentialBinding(req.Context(), issuer, info.UserID, parts[0])
		if err != nil || b.ID == "" {
			writeAccountError(w, upstream.ErrConflict)
			return
		}
		err = a.credentials.Disconnect(req.Context(), upstream.Endpoint{ID: b.EndpointID, UID: b.EndpointUID}, issuer, info.UserID, input.Revision)
		rt.hub.CloseCredentialViews(issuer, info.UserID, b.EndpointID)
		// A Vault outage cannot undo the local revocation. Explain retained secret
		// cleanup without presenting an already disconnected account as active.
		current, readErr := a.store.CredentialBinding(req.Context(), issuer, info.UserID, b.EndpointUID)
		if readErr == nil && !current.Connected {
			writeJSON(w, 200, map[string]any{"disconnected": true, "cleanup_pending": err != nil})
			return
		}
		writeAccountError(w, err)
		return
	}
	c, ok := personalBackend(rt, info, parts[0])
	if !ok {
		writeAPIError(w, 403, "account_access_denied", "You do not have access to this endpoint.", "")
		return
	}
	e := accountEndpoint(c)
	if parts[1] == "login" && req.Method == http.MethodPost && c.Credentials.OAuth != nil {
		target, err := a.credentials.StartLogin(req.Context(), e, issuer, info.UserID, session.csrf, a.userAuth.cfg.PublicURL+"/client-auth/api/accounts/callback")
		if err != nil {
			writeAccountError(w, err)
			return
		}
		writeJSON(w, 200, map[string]string{"authorization_url": target})
		return
	}
	if parts[1] == "token" && req.Method == http.MethodPost && c.Credentials.OAuth == nil {
		var input struct {
			Token     string    `json:"token"`
			Account   string    `json:"account"`
			ExpiresAt time.Time `json:"expires_at"`
			Revision  int64     `json:"revision"`
		}
		if !decodeStrictJSON(w, req, &input, 40<<10) {
			return
		}
		if len(input.Account) > 128 || strings.ContainsAny(input.Account, "\r\n\x00") || (!input.ExpiresAt.IsZero() && (!time.Now().Before(input.ExpiresAt) || input.ExpiresAt.After(time.Now().AddDate(10, 0, 0)))) {
			writeAccountError(w, upstream.ErrReconnect)
			return
		}
		if strings.TrimSpace(input.Account) == "" {
			input.Account = "Personal token"
		}
		b := configstore.CredentialBinding{Issuer: issuer, Subject: info.UserID, Revision: input.Revision, Account: input.Account}
		secret := upstream.Secret{Token: &oauth2.Token{AccessToken: input.Token, TokenType: "Bearer", Expiry: input.ExpiresAt}}
		if err := upstream.VerifyBackend(req.Context(), c, secret); err != nil {
			writeAccountError(w, err)
			return
		}
		if err := a.connectAccount(req, rt, c, b, secret); err != nil {
			writeAccountError(w, err)
			return
		}
		writeJSON(w, 200, map[string]bool{"connected": true})
		return
	}
	http.NotFound(w, req)
}

func (a *App) listAccounts(w http.ResponseWriter, req *http.Request, rt *runtime, info *mcpauth.TokenInfo, issuer string) {
	bindings, err := a.store.ListCredentialBindings(req.Context(), issuer, info.UserID)
	if err != nil {
		writeAccountError(w, upstream.ErrUnavailable)
		return
	}
	byUID := map[string]configstore.CredentialBinding{}
	for _, b := range bindings {
		byUID[b.EndpointUID] = b
	}
	accounts := []accountView{}
	for _, c := range rt.cfg.Backends {
		if _, ok := personalBackend(rt, info, c.ID); !ok {
			continue
		}
		b := byUID[c.EndpointUID]
		delete(byUID, c.EndpointUID)
		b, state := a.credentials.Status(req.Context(), accountEndpoint(c), b)
		accounts = append(accounts, accountView{Endpoint: c.ID, UID: c.EndpointUID, Status: state, Account: b.Account, Revision: b.Revision, OAuth: c.Credentials.OAuth != nil, CanConnect: true, Connected: b.Connected, ExpiresAt: b.ExpiresAt, Renewable: b.Renewable, Scopes: b.Scopes})
	}
	for _, b := range byUID {
		if b.Connected {
			accounts = append(accounts, accountView{Endpoint: b.EndpointID, UID: b.EndpointUID, Status: "access_removed", Account: b.Account, Revision: b.Revision, Connected: true})
		}
	}
	slices.SortFunc(accounts, func(a, b accountView) int { return strings.Compare(a.Endpoint, b.Endpoint) })
	writeJSON(w, 200, map[string]any{"accounts": accounts})
}

func (a *App) connectAccount(req *http.Request, rt *runtime, c config.BackendConfig, b configstore.CredentialBinding, secret upstream.Secret) error {
	a.reloadMu.Lock()
	defer a.reloadMu.Unlock()
	current := a.currentRuntime()
	if current != rt {
		return upstream.ErrConflict
	}
	if _, err := a.credentials.Connect(req.Context(), accountEndpoint(c), b, secret); err != nil {
		return err
	}
	rt.hub.CloseCredentialViews(b.Issuer, b.Subject, c.ID)
	return nil
}

func (a *App) accountCallback(w http.ResponseWriter, req *http.Request, rt *runtime, info *mcpauth.TokenInfo, session *adminSession, issuer string) {
	finish := func(state string) { http.Redirect(w, req, "/client-auth/?connection="+state, http.StatusSeeOther) }
	q, err := url.ParseQuery(req.URL.RawQuery)
	if err != nil || len(req.URL.RawQuery) > 8192 || len(q["state"]) != 1 || len(q["iss"]) > 1 || len(q["error"]) > 1 {
		finish("failed")
		return
	}
	l, err := a.credentials.TakeLogin(q.Get("state"), issuer, info.UserID, session.csrf)
	if err != nil {
		finish("expired")
		return
	}
	if q.Get("error") != "" {
		finish("cancelled")
		return
	}
	if len(q["code"]) != 1 || q.Get("code") == "" {
		finish("failed")
		return
	}
	c, ok := personalBackend(rt, info, l.Endpoint.ID)
	if !ok || c.EndpointUID != l.Endpoint.UID || accountEndpoint(c).Policy() != l.Endpoint.Policy() {
		finish("changed")
		return
	}
	b, secret, err := a.credentials.Exchange(req.Context(), l, q.Get("code"), q.Get("iss"))
	if err != nil {
		finish("failed")
		return
	}
	if err := upstream.VerifyBackend(req.Context(), c, secret); err != nil {
		finish("rejected")
		return
	}
	if err := a.connectAccount(req, rt, c, b, secret); err != nil {
		finish("failed")
		return
	}
	if b.Revision > 0 {
		finish("reconnected")
	} else {
		finish("connected")
	}
}

func writeAccountError(w http.ResponseWriter, err error) {
	status, code, message := 503, "credential_unavailable", upstream.ErrUnavailable.Error()
	switch {
	case errors.Is(err, upstream.ErrConflict):
		status, code, message = 409, "connection_changed", upstream.ErrConflict.Error()
	case errors.Is(err, upstream.ErrConnect):
		status, code, message = 403, "account_required", upstream.ErrConnect.Error()
	case errors.Is(err, upstream.ErrReconnect):
		status, code, message = 422, "account_rejected", upstream.ErrReconnect.Error()
	}
	writeAPIError(w, status, code, message, "")
}

func (a *App) serveVault(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodGet && req.Method != http.MethodPost {
		http.Error(w, "Method not allowed", 405)
		return
	}
	if a.credentials == nil {
		writeJSON(w, 200, map[string]any{"configured": false})
		return
	}
	if req.Method == http.MethodPost {
		if err := a.credentials.Vault.Check(req.Context()); err != nil {
			writeAccountError(w, err)
			return
		}
	}
	writeJSON(w, 200, map[string]any{"configured": true, "vault": a.currentConfig().Vault, "checked": req.Method == http.MethodPost, "callback_url": a.accountCallbackURL()})
}

func (a *App) accountCallbackURL() string {
	u, _ := url.Parse(a.currentConfig().Server.PublicURL)
	return u.Scheme + "://" + u.Host + "/client-auth/api/accounts/callback"
}
