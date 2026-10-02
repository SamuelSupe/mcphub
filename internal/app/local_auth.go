package app

import (
	"crypto/rand"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/SamuelSupe/mcphub/v2/internal/config"
	"github.com/SamuelSupe/mcphub/v2/internal/configstore"
	"github.com/SamuelSupe/mcphub/v2/internal/sso"
)

type localLoginInput struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Code     string `json:"code"`
}

func writeCredentialsError(w http.ResponseWriter, err error) {
	status := http.StatusUnauthorized
	if errors.Is(err, configstore.ErrLoginLimited) {
		status = http.StatusTooManyRequests
		w.Header().Set("Retry-After", "300")
	}
	writeAPIError(w, status, "credentials_invalid", "账号、密码或动态验证码无效，或账号不可用。请稍后重试。", "")
}

func (a *adminAuthorization) localLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "method not allowed", 405)
		return
	}
	if r.Header.Get("Origin") != a.cfg.PublicURL {
		writeAPIError(w, 403, "origin_denied", "登录请求来源被拒绝", "")
		return
	}
	var input localLoginInput
	if !decodeAdminJSON(w, r, &input) {
		return
	}
	user, _, err := a.local.AuthenticateLocal(r.Context(), input.Username, input.Password, input.Code, r.RemoteAddr, false)
	if err != nil {
		writeCredentialsError(w, err)
		return
	}
	token, err := a.local.LocalCredentials(r.Context(), user, a.cfg.ClientID, a.resourceURL(), a.loginScopes)
	if err != nil {
		writeCredentialsError(w, err)
		return
	}
	if _, status := a.verify(r.Context(), token.AccessToken, r); status != http.StatusOK {
		a.deny(w, status)
		return
	}
	a.mu.Lock()
	a.prune()
	if len(a.sessions) >= 1024 {
		a.mu.Unlock()
		writeAPIError(w, 429, "session_limit", "会话过多，请稍后重试", "")
		return
	}
	id := rand.Text() + rand.Text()
	a.sessions[id] = &adminSession{token: token, csrf: rand.Text(), subject: user.ID, expires: time.Now().Add(8 * time.Hour), stepUps: map[string]approvalVerification{}}
	a.mu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: a.sessionCookie(), Value: id, Path: "/", HttpOnly: true, Secure: a.userPortal || a.cfg.Remote(), SameSite: http.SameSiteLaxMode, MaxAge: 8 * 3600})
	w.WriteHeader(http.StatusNoContent)
}

func (a *adminAuthorization) serveLocalSession(w http.ResponseWriter, r *http.Request) {
	result := map[string]any{"mode": a.cfg.Mode, "builtin": true, "initialized": a.local.Initialized(r.Context()), "enterprise": a.local.EnterpriseEnabled(), "authenticated": false}
	session := a.session(r)
	if session != nil {
		info, status := a.sessionInfo(r, session)
		if status == http.StatusOK {
			identity := sso.Identity(info)
			result["local_account"] = identity != nil && identity.Provider == config.LocalIdentityProvider
			if identity != nil {
				result["display_name"] = identity.Name
			}
			result["authenticated"], result["subject"], result["csrf"] = true, info.UserID, session.csrf
			result["can_configure"], result["can_approve"], result["can_review_configuration"] = a.canConfigure(info), a.canApprove(info), a.canReviewConfiguration(info)
			result["can_view_approvals"] = (a.cfg.Approvals.PolicyChanges.Enabled && a.canConfigure(info)) || a.canApprove(info) || a.canReviewConfiguration(info)
		} else if status == http.StatusServiceUnavailable {
			writeAPIError(w, status, "identity_unavailable", "身份服务暂时不可用", "")
			return
		}
	}
	writeJSON(w, 200, result)
}

func (a *App) serveLocalAccount(w http.ResponseWriter, r *http.Request, auth *adminAuthorization) {
	info, session, ok := auth.authorize(w, r)
	if !ok {
		return
	}
	if session == nil {
		writeAPIError(w, 403, "browser_required", "请使用浏览器会话管理账号", "")
		return
	}
	user, err := a.store.Identity(r.Context(), info.UserID)
	if err != nil || user.Provider != config.LocalIdentityProvider {
		writeAPIError(w, 403, "external_identity", "企业账号请在所属身份服务管理密码与 MFA", "")
		return
	}
	if r.URL.Path == "/auth/account" && r.Method == "GET" {
		status, err := a.store.LocalAccountStatus(r.Context(), user.ID)
		if err != nil {
			writeStoreError(w, err)
			return
		}
		writeJSON(w, 200, map[string]any{"username": user.ExternalID, "mfa_enabled": status.MFAEnabled})
		return
	}
	if r.Method != "POST" {
		http.NotFound(w, r)
		return
	}
	var input struct {
		Password    string `json:"password"`
		NewPassword string `json:"new_password"`
		Code        string `json:"code"`
	}
	if !decodeAdminJSON(w, r, &input) {
		return
	}
	verified, _, err := a.sso.AuthenticateLocal(r.Context(), user.ExternalID, input.Password, input.Code, r.RemoteAddr, false)
	if err != nil || verified.ID != user.ID {
		writeCredentialsError(w, err)
		return
	}
	switch r.URL.Path {
	case "/auth/account/password":
		err = a.store.SetLocalPassword(configstore.WithActor(r.Context(), user.ID), user.ID, input.NewPassword, verified.CredentialVersion, false)
	case "/auth/account/mfa/setup":
		secret, err := a.store.BeginLocalMFA(r.Context(), user.ID, verified.CredentialVersion)
		if err != nil {
			writeAPIError(w, 400, "mfa_setup_failed", err.Error(), "")
			return
		}
		uri := "otpauth://totp/" + url.PathEscape("MCPHub:"+user.ExternalID) + "?" + url.Values{"secret": {secret}, "issuer": {"MCPHub"}, "algorithm": {"SHA1"}, "digits": {"6"}, "period": {"30"}}.Encode()
		writeJSON(w, 200, map[string]string{"secret": secret, "uri": uri})
		return
	case "/auth/account/mfa/confirm":
		err = a.store.ConfirmLocalMFA(configstore.WithActor(r.Context(), user.ID), user.ID, input.Code, verified.CredentialVersion)
	default:
		http.NotFound(w, r)
		return
	}
	if err != nil {
		writeAPIError(w, 400, "account_update_failed", err.Error(), "")
		return
	}
	a.currentRuntime().hub.PruneIdentityViews(r.Context())
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) verifyLocalApproval(w http.ResponseWriter, r *http.Request, id string, identity adminIdentity) {
	user, err := a.store.Identity(r.Context(), identity.info.UserID)
	if err != nil || user.Provider != config.LocalIdentityProvider {
		writeAPIError(w, 403, "enterprise_verification", "企业账号请使用企业加强认证", "")
		return
	}
	var input localLoginInput
	if !decodeAdminJSON(w, r, &input) {
		return
	}
	verified, mfa, err := a.sso.AuthenticateLocal(r.Context(), user.ExternalID, input.Password, input.Code, r.RemoteAddr, true)
	if err != nil || !mfa || verified.ID != user.ID {
		writeCredentialsError(w, err)
		return
	}
	// A reset or disable between password verification and proof creation must
	// invalidate the browser session; the proof is bound to that exact session.
	identity.session.mu.Lock()
	defer identity.session.mu.Unlock()
	if identity.session.closed || !time.Now().Before(identity.session.expires) {
		writeCredentialsError(w, configstore.ErrCredentials)
		return
	}
	if _, err := a.sso.Verifier(a.adminAuth.resourceURL()).Verify(r.Context(), identity.session.token.AccessToken, r); err != nil {
		writeCredentialsError(w, err)
		return
	}
	if identity.session.stepUps == nil {
		identity.session.stepUps = map[string]approvalVerification{}
	}
	for key, proof := range identity.session.stepUps {
		if !time.Now().Before(proof.expires) {
			delete(identity.session.stepUps, key)
		}
	}
	if len(identity.session.stepUps) >= 32 {
		writeAPIError(w, 429, "proof_limit", "验证请求过多", "")
		return
	}
	identity.session.stepUps[id] = approvalVerification{expires: time.Now().Add(2 * time.Minute), detail: configstore.ApprovalDetail{ACR: config.LocalMFAACR, AuthTime: time.Now()}}
	writeJSON(w, 200, map[string]any{"verified": true})
}

func (a *App) identityProviderAllowed(provider string, directoryManaged bool) bool {
	return a.sso != nil && a.sso.ProviderAllowed(provider, directoryManaged)
}

func localAccountAction(suffix string) (string, string) {
	parts := strings.Split(strings.TrimPrefix(suffix, "/"), "/")
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	return "", ""
}
