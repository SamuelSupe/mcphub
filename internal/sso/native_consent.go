package sso

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/SamuelSupe/mcphub/v2/internal/config"
	"github.com/SamuelSupe/mcphub/v2/internal/configstore"
)

type NativeSelection struct {
	Endpoint     string
	Tools        []string
	Capabilities configstore.GrantCapabilities
	Write        bool
	Resources    []config.ResourceRule
}

type nativeConsent struct {
	Code     authorizationCode
	Identity configstore.EffectiveIdentity
	Browser  string
	Expires  time.Time
	Options  []config.ClientEndpointOption
	Name     string
	Lang     string
}

func (s *Server) ConfigureNativeConsent(catalog func(context.Context, configstore.EffectiveIdentity, []string) []config.ClientEndpointOption, confirm func(context.Context, configstore.EffectiveIdentity, string, []NativeSelection, time.Duration) ([]configstore.GrantBinding, error)) {
	s.nativeCatalog, s.nativeConfirm = catalog, confirm
}

func (s *Server) registeredClients(ctx context.Context) ([]config.SSOClient, error) {
	managed, err := s.store.NativeClients(ctx)
	return slices.Concat(s.cfg.Auth.SSO.Clients, managed.Clients), err
}

func (s *Server) consentClient(ctx context.Context, id string) (config.SSOClient, bool) {
	clients, err := s.registeredClients(ctx)
	if err != nil {
		return config.SSOClient{}, false
	}
	index := slices.IndexFunc(clients, func(client config.SSOClient) bool { return client.ID == id })
	if index < 0 {
		return config.SSOClient{}, false
	}
	return clients[index], true
}

func clientPolicy(client config.SSOClient) string {
	data, _ := json.Marshal(client)
	return configstore.SecretHash(string(data))
}

func (s *Server) finishAuthorization(w http.ResponseWriter, r *http.Request, code authorizationCode) {
	client, ok := s.consentClient(r.Context(), code.ClientID)
	if !ok || !s.allowedClient(code.ClientID, code.Redirect, code.Resource) {
		s.redirectResult(w, r, code, "", "unauthorized_client")
		return
	}
	if client.RequireConsent && code.Resource == s.cfg.Server.PublicURL {
		code.ClientPolicy = clientPolicy(client)
		identity, err := s.tokenIdentity(r.Context(), code.UserID)
		if err != nil || s.nativeCatalog == nil || s.nativeConfirm == nil {
			s.redirectResult(w, r, code, "", "access_denied")
			return
		}
		if len(code.Scopes) == 0 {
			code.Scopes = append(identity.Permissions.EffectiveScopes(s.cfg.Admin), "offline_access")
		}
		code.Scopes = s.grantedScopes(identity, code.Scopes)
		id, browser := rand.Text(), rand.Text()
		name := client.Name
		if name == "" {
			name = client.ID
		}
		pending := nativeConsent{Code: code, Identity: identity, Browser: browser, Expires: time.Now().Add(5 * time.Minute), Options: s.nativeCatalog(r.Context(), identity, code.Scopes), Name: name, Lang: r.FormValue("lang")}
		s.mu.Lock()
		for key, item := range s.nativePending {
			if !time.Now().Before(item.Expires) {
				delete(s.nativePending, key)
			}
		}
		if len(s.nativePending) >= 128 {
			s.mu.Unlock()
			http.Error(w, "too many pending consents", 429)
			return
		}
		s.nativePending[id] = pending
		s.mu.Unlock()
		http.SetCookie(w, &http.Cookie{Name: "__Host-mcphub-consent-" + id, Value: browser, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: 300})
		s.renderConsent(w, id, pending, "", nil)
		return
	}
	s.issueCode(w, r, code)
}

func (s *Server) issueCode(w http.ResponseWriter, r *http.Request, code authorizationCode) {
	raw := rand.Text() + rand.Text()
	code.Expires = time.Now().Add(time.Minute)
	s.mu.Lock()
	s.cleanupLocked()
	if len(s.codes) >= 1024 {
		s.mu.Unlock()
		http.Error(w, "too many authorization codes", 429)
		return
	}
	s.codes[configstore.SecretHash(raw)] = code
	s.mu.Unlock()
	s.redirectResult(w, r, code, raw, "")
}

func (s *Server) nativeConsent(w http.ResponseWriter, r *http.Request) {
	issuer, _ := url.Parse(s.cfg.Auth.Issuer)
	if r.Method != "POST" || r.Header.Get("Origin") != issuer.Scheme+"://"+issuer.Host || len(r.URL.Query()) != 0 {
		http.Error(w, "invalid consent request", 400)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	if r.ParseForm() != nil || len(r.PostForm["request"]) != 1 || len(r.PostForm["decision"]) != 1 || len(r.PostForm["ttl"]) != 1 || len(r.PostForm["lang"]) > 1 {
		http.Error(w, "invalid consent request", 400)
		return
	}
	id := r.PostForm.Get("request")
	s.mu.Lock()
	pending, ok := s.nativePending[id]
	s.mu.Unlock()
	cookie, err := r.Cookie("__Host-mcphub-consent-" + id)
	if !ok || !time.Now().Before(pending.Expires) || err != nil || subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(pending.Browser)) != 1 {
		http.Error(w, "consent expired; restart login", 400)
		return
	}
	if lang := r.PostForm.Get("lang"); lang != "" {
		if lang != "en" && lang != "zh-CN" {
			http.Error(w, "invalid consent language", 400)
			return
		}
		pending.Lang = lang
	}
	if r.PostForm.Get("decision") == "language" {
		if pending.Lang == "en" {
			pending.Lang = "zh-CN"
		} else {
			pending.Lang = "en"
		}
		s.renderConsent(w, id, pending, "", r.PostForm)
		return
	}
	consume := func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		if _, ok := s.nativePending[id]; !ok {
			return false
		}
		delete(s.nativePending, id)
		http.SetCookie(w, &http.Cookie{Name: "__Host-mcphub-consent-" + id, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: -1})
		return true
	}
	if r.PostForm.Get("decision") == "deny" {
		if consume() {
			s.redirectResult(w, r, pending.Code, "", "access_denied")
		}
		return
	}
	if r.PostForm.Get("decision") != "confirm" {
		http.Error(w, "invalid consent decision", 400)
		return
	}
	identity, err := s.tokenIdentity(r.Context(), pending.Identity.ID)
	client, registered := s.consentClient(r.Context(), pending.Code.ClientID)
	if err != nil || identity.Version != pending.Identity.Version || !registered || clientPolicy(client) != pending.Code.ClientPolicy || !s.allowedClient(pending.Code.ClientID, pending.Code.Redirect, pending.Code.Resource) {
		consume()
		s.redirectResult(w, r, pending.Code, "", "access_denied")
		return
	}
	ttl, err := strconv.ParseInt(r.PostForm.Get("ttl"), 10, 64)
	if err != nil || ttl < 60 || ttl > int64(s.cfg.ClientAuthorization.GrantTTL()/time.Second) {
		s.renderConsent(w, id, pending, "授权时长不可用 / Invalid duration", r.PostForm)
		return
	}
	selections := []NativeSelection{}
	fields := map[string]bool{"request": true, "decision": true, "ttl": true, "lang": true}
	for _, option := range pending.Options {
		for _, prefix := range []string{"tool:", "write:", "prompts:", "resources:", "subscriptions:", "rules:"} {
			fields[prefix+option.ID] = true
			if prefix != "tool:" && len(r.PostForm[prefix+option.ID]) > 1 {
				http.Error(w, "duplicate consent field", 400)
				return
			}
		}
		tools := r.PostForm["tool:"+option.ID]
		tools = slices.Compact(slices.Sorted(slices.Values(tools)))
		for _, name := range tools {
			if !slices.ContainsFunc(option.Tools, func(tool config.ClientToolOption) bool { return tool.Name == name }) {
				http.Error(w, "invalid tool selection", 400)
				return
			}
		}
		selection := NativeSelection{Endpoint: option.ID, Tools: tools, Capabilities: configstore.GrantCapabilities{Tools: len(tools) > 0}, Write: r.PostForm.Get("write:"+option.ID) == "on"}
		selection.Capabilities.Prompts = r.PostForm.Get("prompts:"+option.ID) == "on"
		selection.Capabilities.Resources = r.PostForm.Get("resources:"+option.ID) == "on"
		selection.Capabilities.Subscriptions = r.PostForm.Get("subscriptions:"+option.ID) == "on"
		if !selection.Write && slices.ContainsFunc(option.Tools, func(tool config.ClientToolOption) bool {
			return slices.Contains(tools, tool.Name) && tool.Effect != "read"
		}) {
			s.renderConsent(w, id, pending, "写工具需要勾选允许申请写操作 / Select allow write requests for write tools", r.PostForm)
			return
		}
		if (selection.Capabilities.Prompts && !option.Prompts) || (selection.Capabilities.Resources && !option.Resources) || (selection.Capabilities.Subscriptions && (!option.Subscriptions || !selection.Capabilities.Resources)) {
			s.renderConsent(w, id, pending, "能力选择不可用 / Invalid capabilities", r.PostForm)
			return
		}
		if raw := strings.TrimSpace(r.PostForm.Get("rules:" + option.ID)); raw != "" && (json.Unmarshal([]byte(raw), &selection.Resources) != nil || len(selection.Resources) > 32) {
			s.renderConsent(w, id, pending, "资源条件格式错误 / Invalid resource conditions", r.PostForm)
			return
		}
		if len(selection.Resources) > 0 && (selection.Capabilities.Prompts || selection.Capabilities.Resources || selection.Capabilities.Subscriptions || config.ValidateToolRules("consent", []config.ToolRule{{Match: "*", ResourceRules: selection.Resources}}) != nil) {
			s.renderConsent(w, id, pending, "工具参数条件不能用于提示词或资源，请分开授权 / Tool argument rules require a separate tool-only grant", r.PostForm)
			return
		}
		if selection.Capabilities != (configstore.GrantCapabilities{}) {
			selections = append(selections, selection)
		}
	}
	for field := range r.PostForm {
		if !fields[field] {
			http.Error(w, "unknown consent field", 400)
			return
		}
	}
	if len(selections) == 0 || len(selections) > 16 {
		s.renderConsent(w, id, pending, "请选择 1–16 个服务 / Select 1–16 services", r.PostForm)
		return
	}
	if !consume() {
		http.Error(w, "consent already consumed", 409)
		return
	}
	bindings, err := s.nativeConfirm(r.Context(), identity, pending.Name, selections, time.Duration(ttl)*time.Second)
	if err != nil {
		s.redirectResult(w, r, pending.Code, "", "access_denied")
		return
	}
	pending.Code.Grants = bindings
	s.issueCode(w, r, pending.Code)
}
