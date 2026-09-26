package app

import (
	"context"
	"crypto/subtle"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	mcpauth "github.com/modelcontextprotocol/go-sdk/auth"
	"golang.org/x/oauth2"

	"github.com/SamuelSupe/mcphub/v2/internal/authn"
	"github.com/SamuelSupe/mcphub/v2/internal/config"
	"github.com/SamuelSupe/mcphub/v2/internal/configstore"
)

const adminSessionCookie = "__Host-mcphub-admin"
const adminLoginCookie = "__Host-mcphub-login"

type adminAuthorization struct {
	userPortal bool
	resource   string
	cfg        config.AdminConfig
	issuer     string
	verifier   tokenVerifier
	client     *http.Client
	mu         sync.Mutex
	sessions   map[string]*adminSession
	logins     map[string]adminLogin
}

type adminSession struct {
	mu            sync.Mutex
	token         *oauth2.Token
	oauth         *oauth2.Config
	csrf, subject string
	expires       time.Time
	closed        bool
	stepUps       map[string]approvalVerification
}

type approvalVerification struct {
	expires time.Time
	detail  configstore.ApprovalDetail
}

type adminLogin struct {
	requiredScopes    []string
	browser, verifier string
	oauth             *oauth2.Config
	issuerRequired    bool
	expires           time.Time
	approvalID        string
	session           *adminSession
	subject, nonce    string
	started           time.Time
	idVerifier        *oidc.IDTokenVerifier
}

type adminIdentityKey struct{}
type adminIdentity struct {
	info    *mcpauth.TokenInfo
	session *adminSession
}

func newAdminAuthorization(cfg config.AdminConfig, issuer string, verifier tokenVerifier) *adminAuthorization {
	return &adminAuthorization{cfg: cfg, issuer: issuer, verifier: verifier, client: authn.LoginHTTPClient(), sessions: make(map[string]*adminSession), logins: make(map[string]adminLogin)}
}

func (a *App) secureAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		setAdminSecurityHeaders(w)
		w.Header().Set("Cache-Control", "no-store")
		cfg := a.currentConfig().Admin
		origin := "http://" + cfg.Listen
		if cfg.Remote() {
			origin = cfg.PublicURL
			u, _ := url.Parse(origin)
			if req.Host != u.Host {
				writeAdminDenied(w, req, "access_denied", "管理访问被拒绝")
				return
			}
			w.Header().Set("Strict-Transport-Security", "max-age=31536000")
		} else {
			host, _, err := net.SplitHostPort(req.RemoteAddr)
			ip := net.ParseIP(host)
			if err != nil || ip == nil || !ip.IsLoopback() || req.Host != cfg.Listen {
				writeAdminDenied(w, req, "access_denied", "管理访问被拒绝")
				return
			}
		}
		if value := req.Header.Get("Origin"); value != "" && value != origin {
			writeAdminDenied(w, req, "origin_denied", "管理请求来源被拒绝")
			return
		}
		if req.URL.Path == "/auth/session" && req.Method == http.MethodGet {
			if !cfg.Remote() {
				writeJSON(w, http.StatusOK, map[string]any{"mode": "local", "authenticated": true, "subject": "local"})
				return
			}
			a.adminAuth.serveSession(w, req)
			return
		}
		if cfg.Remote() {
			if a.adminAuth.servePublic(w, req) {
				return
			}
			if strings.HasPrefix(req.URL.Path, "/api/") {
				info, session, ok := a.adminAuth.authorize(w, req)
				if !ok {
					return
				}
				approvalPath := req.URL.Path == "/api/v1/approvals" || strings.HasPrefix(req.URL.Path, "/api/v1/approvals/")
				if approvalPath && session == nil {
					writeAPIError(w, http.StatusForbidden, "browser_approval_required", "审批必须使用管理员浏览器会话，不能使用调用凭证或管理 API Token", "")
					return
				}
				allowed := a.adminAuth.canConfigure(info)
				if approvalPath {
					allowed = (allowed && a.adminAuth.cfg.Approvals.PolicyChanges.Enabled) || a.adminAuth.canApprove(info) || a.adminAuth.canReviewConfiguration(info)
				}
				if !allowed && req.URL.Path != "/api/v1/me" {
					writeAPIError(w, http.StatusForbidden, "role_required", "当前角色无权访问此功能", "")
					return
				}
				req = req.WithContext(configstore.WithActor(req.Context(), info.UserID))
				req = req.WithContext(context.WithValue(req.Context(), adminIdentityKey{}, adminIdentity{info, session}))
				if req.URL.Path == "/api/v1/me" && req.Method == http.MethodGet {
					writeJSON(w, http.StatusOK, map[string]any{"subject": info.UserID, "issuer": a.adminAuth.issuer, "scopes": info.Scopes})
					return
				}
			}
		}
		next.ServeHTTP(w, req)
	})
}

func (a *adminAuthorization) servePublic(w http.ResponseWriter, req *http.Request) bool {
	switch req.URL.Path {
	case "/.well-known/oauth-protected-resource":
		if req.Method != http.MethodGet {
			http.Error(w, "method not allowed", 405)
			return true
		}
		writeJSON(w, http.StatusOK, map[string]any{"resource": a.cfg.PublicURL, "authorization_servers": []string{a.issuer}, "scopes_supported": a.cfg.RequiredScopes, "bearer_methods_supported": []string{"header"}})
	case "/auth/login":
		if req.Method != http.MethodGet {
			http.Error(w, "method not allowed", 405)
			return true
		}
		a.login(w, req)
	case "/auth/callback":
		if req.Method != http.MethodGet {
			http.Error(w, "method not allowed", 405)
			return true
		}
		a.callback(w, req)
	case "/auth/logout":
		if req.Method != http.MethodPost {
			http.Error(w, "method not allowed", 405)
			return true
		}
		session := a.session(req)
		if session != nil && (req.Header.Get("Origin") != a.cfg.PublicURL || subtle.ConstantTimeCompare([]byte(req.Header.Get("X-MCPHub-CSRF")), []byte(session.csrf)) != 1) {
			writeAPIError(w, http.StatusForbidden, "csrf_denied", "管理请求校验失败，请刷新页面", "")
			return true
		}
		if session != nil {
			session.mu.Lock()
			session.closed = true
			session.token = nil
			session.mu.Unlock()
		}
		if cookie, err := req.Cookie(a.sessionCookie()); err == nil {
			a.mu.Lock()
			delete(a.sessions, cookie.Value)
			a.mu.Unlock()
		}
		setAdminCookie(w, a.sessionCookie(), "", -1)
		w.WriteHeader(http.StatusNoContent)
	default:
		return false
	}
	return true
}

func (a *adminAuthorization) deny(w http.ResponseWriter, status int) {
	errorCode, message := "login_required", "请登录管理员账号"
	challenge := fmt.Sprintf(`Bearer resource_metadata=%q, scope=%q`, a.cfg.PublicURL+"/.well-known/oauth-protected-resource", strings.Join(a.cfg.RequiredScopes, " "))
	if status == http.StatusForbidden {
		errorCode, message = "insufficient_scope", "当前账号没有管理员权限"
		challenge += `, error="insufficient_scope"`
	}
	w.Header().Set("WWW-Authenticate", challenge)
	writeAPIError(w, status, errorCode, message, "")
}

func (a *adminAuthorization) verify(ctx context.Context, access string, req *http.Request) (*mcpauth.TokenInfo, int) {
	if !a.verifier.Ready() {
		return nil, http.StatusServiceUnavailable
	}
	info, err := a.verifier.Verify(ctx, access, req)
	if err != nil {
		return nil, http.StatusUnauthorized
	}
	if a.userPortal {
		if info.UserID == "" {
			return nil, http.StatusUnauthorized
		}
		return info, http.StatusOK
	}
	if !a.canConfigure(info) && !a.canApprove(info) && !a.canReviewConfiguration(info) {
		return nil, http.StatusForbidden
	}
	return info, http.StatusOK
}

func hasAdminScopes(info *mcpauth.TokenInfo, scopes []string) bool {
	return info != nil && info.UserID != "" && len(scopes) > 0 && !slices.ContainsFunc(scopes, func(scope string) bool { return !slices.Contains(info.Scopes, scope) })
}

func (a *adminAuthorization) canConfigure(info *mcpauth.TokenInfo) bool {
	return hasAdminScopes(info, a.cfg.RequiredScopes)
}
func (a *adminAuthorization) canApprove(info *mcpauth.TokenInfo) bool {
	return hasAdminScopes(info, a.cfg.Approvals.Scopes())
}

func (a *adminAuthorization) canReviewConfiguration(info *mcpauth.TokenInfo) bool {
	policy := a.cfg.Approvals.PolicyChanges
	return policy.Enabled && hasAdminScopes(info, policy.Scopes()) && (len(policy.Subjects) == 0 || slices.Contains(policy.Subjects, info.UserID))
}

func (a *adminAuthorization) authorize(w http.ResponseWriter, req *http.Request) (*mcpauth.TokenInfo, *adminSession, bool) {
	var info *mcpauth.TokenInfo
	var session *adminSession
	status := http.StatusUnauthorized
	if value := req.Header.Get("Authorization"); value != "" {
		parts := strings.Fields(value)
		if len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
			info, status = a.verify(req.Context(), parts[1], req)
		}
	} else {
		session = a.session(req)
		if session != nil {
			info, status = a.sessionInfo(req, session)
		}
	}
	if status != http.StatusOK {
		if status == http.StatusServiceUnavailable {
			writeAPIError(w, status, "identity_unavailable", "身份服务暂时不可用", "")
		} else {
			a.deny(w, status)
		}
		return nil, nil, false
	}
	if session != nil && req.Method != http.MethodGet && req.Method != http.MethodHead {
		if req.Header.Get("Origin") != a.cfg.PublicURL || subtle.ConstantTimeCompare([]byte(req.Header.Get("X-MCPHub-CSRF")), []byte(session.csrf)) != 1 {
			writeAPIError(w, http.StatusForbidden, "csrf_denied", "管理请求校验失败，请刷新页面", "")
			return nil, nil, false
		}
	}
	return info, session, true
}

func (a *adminAuthorization) session(req *http.Request) *adminSession {
	cookie, err := req.Cookie(a.sessionCookie())
	if err != nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	session := a.sessions[cookie.Value]
	if session != nil && time.Now().Before(session.expires) {
		return session
	}
	delete(a.sessions, cookie.Value)
	return nil
}

func (a *adminAuthorization) serveSession(w http.ResponseWriter, req *http.Request) {
	session := a.session(req)
	if session == nil {
		writeJSON(w, http.StatusOK, map[string]any{"mode": "remote", "authenticated": false})
		return
	}
	info, status := a.sessionInfo(req, session)
	if status != http.StatusOK {
		if status == http.StatusServiceUnavailable {
			writeAPIError(w, status, "identity_unavailable", "身份服务暂时不可用", "")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"mode": "remote", "authenticated": false, "forbidden": status == http.StatusForbidden})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"mode": "remote", "authenticated": true, "subject": info.UserID, "csrf": session.csrf, "expires_at": session.expires, "can_configure": a.canConfigure(info), "can_approve": a.canApprove(info), "can_review_configuration": a.canReviewConfiguration(info), "can_view_approvals": (a.cfg.Approvals.PolicyChanges.Enabled && a.canConfigure(info)) || a.canApprove(info) || a.canReviewConfiguration(info)})
}

func setAdminCookie(w http.ResponseWriter, name, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: "/", HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode, MaxAge: maxAge})
}

func (a *App) adminMutationContext(req *http.Request) context.Context {
	// Once a candidate is ready, a disconnected browser must not interrupt the
	// database/runtime commit. Carry only its verified identity into that lifetime.
	return configstore.WithActor(a.ctx, configstore.Actor(req.Context()))
}

func (a *adminAuthorization) sessionCookie() string {
	if a.userPortal {
		return "__Host-mcphub-user"
	}
	return adminSessionCookie
}
func (a *adminAuthorization) loginCookie() string {
	if a.userPortal {
		return "__Host-mcphub-user-login"
	}
	return adminLoginCookie
}
func (a *adminAuthorization) homePath() string {
	if a.userPortal {
		return "/client-auth/"
	}
	return "/"
}
func (a *adminAuthorization) resourceURL() string {
	if a.resource != "" {
		return a.resource
	}
	return a.cfg.PublicURL
}
