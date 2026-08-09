package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/SamuelSupe/mcphub/internal/backend"
	"github.com/SamuelSupe/mcphub/internal/config"
	"github.com/SamuelSupe/mcphub/internal/configstore"
	"github.com/SamuelSupe/mcphub/internal/version"
)

const maximumAdminBodyBytes = 1 << 20

var errRuntimeApply = errors.New("runtime changed before configuration commit")

type backendInput struct {
	ID                string            `json:"id"`
	URL               string            `json:"url"`
	Enabled           *bool             `json:"enabled,omitempty"`
	Required          bool              `json:"required"`
	RequiredScopes    []string          `json:"required_scopes"`
	ToolRules         []config.ToolRule `json:"tool_rules"`
	RequestTimeout    string            `json:"request_timeout"`
	AllowInsecureHTTP bool              `json:"allow_insecure_http"`
	Headers           []headerInput     `json:"headers"`
	OAuth             *oauthInput       `json:"oauth,omitempty"`
}

type headerInput struct {
	Name  string  `json:"name"`
	Value *string `json:"value,omitempty"`
}

type oauthInput struct {
	Type         string   `json:"type"`
	Issuer       string   `json:"issuer"`
	ClientID     string   `json:"client_id"`
	ClientSecret *string  `json:"client_secret,omitempty"`
	Scopes       []string `json:"scopes"`
}

type backendView struct {
	ID                string             `json:"id"`
	URL               string             `json:"url"`
	Enabled           bool               `json:"enabled"`
	Required          bool               `json:"required"`
	RequiredScopes    []string           `json:"required_scopes"`
	ToolRules         []config.ToolRule  `json:"tool_rules"`
	RequestTimeout    string             `json:"request_timeout"`
	AllowInsecureHTTP bool               `json:"allow_insecure_http"`
	Headers           []headerView       `json:"headers"`
	OAuth             *oauthView         `json:"oauth,omitempty"`
	Revision          int64              `json:"revision"`
	CreatedAt         time.Time          `json:"created_at"`
	UpdatedAt         time.Time          `json:"updated_at"`
	LastProbeAt       *time.Time         `json:"last_probe_at,omitempty"`
	LastProbeOK       *bool              `json:"last_probe_ok,omitempty"`
	LastProbe         json.RawMessage    `json:"last_probe,omitempty"`
	Runtime           backendRuntimeView `json:"runtime"`
}

type headerView struct {
	Name       string `json:"name"`
	Configured bool   `json:"configured"`
}

type oauthView struct {
	Type                   string   `json:"type"`
	Issuer                 string   `json:"issuer"`
	ClientID               string   `json:"client_id"`
	ClientSecretConfigured bool     `json:"client_secret_configured"`
	Scopes                 []string `json:"scopes"`
}

type backendRuntimeView struct {
	State             string `json:"state"`
	Tools             int    `json:"tools"`
	Prompts           int    `json:"prompts"`
	Resources         int    `json:"resources"`
	ResourceTemplates int    `json:"resource_templates"`
}

type apiErrorBody struct {
	Error apiError `json:"error"`
}

type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Field   string `json:"field,omitempty"`
}

func (a *App) adminHandler() http.Handler {
	return a.secureAdmin(http.HandlerFunc(a.serveAdmin))
}

func (a *App) secureAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		setAdminSecurityHeaders(w)
		host, _, err := net.SplitHostPort(req.RemoteAddr)
		ip := net.ParseIP(host)
		if err != nil || ip == nil || !ip.IsLoopback() || req.Host != a.currentConfig().Admin.Listen {
			writeAdminDenied(w, req, "access_denied", "管理访问被拒绝")
			return
		}
		origin := req.Header.Get("Origin")
		if origin != "" && origin != "http://"+a.currentConfig().Admin.Listen {
			writeAdminDenied(w, req, "origin_denied", "管理请求来源被拒绝")
			return
		}
		if strings.HasPrefix(req.URL.Path, "/api/") {
			w.Header().Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, req)
	})
}

func writeAdminDenied(w http.ResponseWriter, req *http.Request, code, message string) {
	if strings.HasPrefix(req.URL.Path, "/api/") {
		writeAPIError(w, http.StatusForbidden, code, message, "")
		return
	}
	http.Error(w, message, http.StatusForbidden)
}

func setAdminSecurityHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; object-src 'none'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
	w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
}

func (a *App) serveAdmin(w http.ResponseWriter, req *http.Request) {
	if !strings.HasPrefix(req.URL.Path, "/api/v1/") {
		a.serveAdminUI(w, req)
		return
	}
	path := strings.TrimPrefix(req.URL.Path, "/api/v1/")
	switch {
	case path == "overview" && req.Method == http.MethodGet:
		a.serveAdminOverview(w, req)
	case path == "events" && req.Method == http.MethodGet:
		a.serveAdminEvents(w, req)
	case path == "tool-groups" && (req.Method == http.MethodGet || req.Method == http.MethodPost):
		a.serveAdminToolGroups(w, req)
	case strings.HasPrefix(path, "tool-groups/"):
		a.serveAdminToolGroupRoute(w, req, strings.TrimPrefix(path, "tool-groups/"))
	case path == "backends" && req.Method == http.MethodGet:
		a.serveAdminBackends(w, req)
	case path == "backends" && req.Method == http.MethodPost:
		a.createAdminBackend(w, req)
	case path == "backends/probe" && req.Method == http.MethodPost:
		a.probeAdminInput(w, req)
	case strings.HasPrefix(path, "backends/"):
		a.serveAdminBackend(w, req, strings.TrimPrefix(path, "backends/"))
	default:
		writeAPIError(w, http.StatusNotFound, "not_found", "接口不存在", "")
	}
}

func (a *App) serveAdminOverview(w http.ResponseWriter, req *http.Request) {
	records, err := a.store.List(req.Context())
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "storage_failed", "无法读取配置", "")
		return
	}
	details := a.adminStatusDetails()
	ready, enabled, required, unavailable := 0, 0, 0, 0
	for _, record := range records {
		if record.Enabled {
			enabled++
			if record.Config.Required {
				required++
			}
			if detail, ok := details[strings.ToLower(record.Config.ID)]; ok && detail.Ready {
				ready++
			} else {
				unavailable++
			}
		}
	}
	groups, groupErr := a.store.ListToolGroups(req.Context())
	if groupErr != nil {
		writeAPIError(w, http.StatusInternalServerError, "storage_failed", "无法读取工具组", "")
		return
	}
	groupEnabled, httpTools := 0, 0
	for _, group := range groups {
		if group.Config.Enabled {
			groupEnabled++
		}
		for _, tool := range group.Config.Tools {
			if group.Config.Enabled && tool.Enabled {
				httpTools++
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"total": len(records), "enabled": enabled, "ready": ready,
		"required": required, "optional": enabled - required,
		"unavailable": unavailable, "version": version.Value,
		"tool_groups": len(groups), "tool_groups_enabled": groupEnabled, "http_tools": httpTools,
	})
}

func (a *App) serveAdminEvents(w http.ResponseWriter, req *http.Request) {
	limit, _ := strconv.Atoi(req.URL.Query().Get("limit"))
	events, err := a.store.Events(req.Context(), limit)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "storage_failed", "无法读取最近变更", "")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": events})
}

func (a *App) serveAdminBackends(w http.ResponseWriter, req *http.Request) {
	records, err := a.store.List(req.Context())
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "storage_failed", "无法读取后端", "")
		return
	}
	details := a.adminStatusDetails()
	views := make([]backendView, 0, len(records))
	for _, record := range records {
		views = append(views, makeBackendView(record, details))
	}
	writeJSON(w, http.StatusOK, map[string]any{"backends": views})
}

func (a *App) serveAdminBackend(w http.ResponseWriter, req *http.Request, suffix string) {
	id := suffix
	probe := false
	if strings.HasSuffix(suffix, "/probe") {
		id = strings.TrimSuffix(suffix, "/probe")
		probe = true
	}
	if id == "" || strings.Contains(id, "/") {
		writeAPIError(w, http.StatusNotFound, "not_found", "后端不存在", "")
		return
	}
	if probe && req.Method == http.MethodPost {
		a.probeStoredBackend(w, req, id)
		return
	}
	switch req.Method {
	case http.MethodGet:
		record, err := a.store.Get(req.Context(), id)
		if err != nil {
			writeStoreError(w, err)
			return
		}
		w.Header().Set("ETag", revisionETag(record.Revision))
		writeJSON(w, http.StatusOK, makeBackendView(record, a.adminStatusDetails()))
	case http.MethodPut:
		a.updateAdminBackend(w, req, id)
	case http.MethodDelete:
		a.deleteAdminBackend(w, req, id)
	default:
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "不支持此操作", "")
	}
}

func (a *App) createAdminBackend(w http.ResponseWriter, req *http.Request) {
	var input backendInput
	if !decodeAdminJSON(w, req, &input) {
		return
	}
	backendConfig, enabled, err := backendFromInput(input, nil)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "validation_failed", err.Error(), "")
		return
	}
	a.applyDefaultBackendTimeout(&backendConfig)
	a.reloadMu.Lock()
	defer a.reloadMu.Unlock()
	records, err := a.store.List(req.Context())
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "storage_failed", "无法读取配置", "")
		return
	}
	for _, record := range records {
		if strings.EqualFold(record.Config.ID, backendConfig.ID) {
			writeAPIError(w, http.StatusConflict, "revision_conflict", "Backend ID 已存在", "id")
			return
		}
	}
	groups, err := a.store.ListToolGroups(req.Context())
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "storage_failed", "无法读取工具组", "")
		return
	}
	for _, group := range groups {
		if strings.EqualFold(group.Config.ID, backendConfig.ID) {
			writeAPIError(w, http.StatusConflict, "revision_conflict", "Backend ID 已被工具组使用", "id")
			return
		}
	}
	desired := append(slices.Clone(records), configstore.Record{Config: backendConfig, Enabled: enabled})
	candidate, previous, ok := a.prepareAdminCandidate(w, desired, backendConfig.ID, "create")
	if !ok {
		return
	}
	var saved configstore.Record
	err = a.commitAdminCandidate(candidate, previous, func() error {
		var commitErr error
		saved, commitErr = a.store.Create(a.ctx, configstore.Record{Config: backendConfig, Enabled: enabled})
		return commitErr
	})
	if err != nil {
		writeAdminCommitError(w, err)
		return
	}
	w.Header().Set("ETag", revisionETag(saved.Revision))
	writeJSON(w, http.StatusCreated, makeBackendView(saved, a.adminStatusDetails()))
}

func (a *App) updateAdminBackend(w http.ResponseWriter, req *http.Request, id string) {
	expected, ok := requireRevision(w, req)
	if !ok {
		return
	}
	current, err := a.store.Get(req.Context(), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if current.Revision != expected {
		writeAPIError(w, http.StatusConflict, "revision_conflict", "配置已在其他页面更新，请刷新后重试", "")
		return
	}
	var input backendInput
	if !decodeAdminJSON(w, req, &input) {
		return
	}
	if input.ID != current.Config.ID {
		writeAPIError(w, http.StatusBadRequest, "validation_failed", "Backend ID 创建后不能修改", "id")
		return
	}
	backendConfig, enabled, err := backendFromInput(input, &current.Config)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "validation_failed", err.Error(), "")
		return
	}
	a.applyDefaultBackendTimeout(&backendConfig)
	a.reloadMu.Lock()
	defer a.reloadMu.Unlock()
	records, err := a.store.List(req.Context())
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "storage_failed", "无法读取配置", "")
		return
	}
	found := false
	for index := range records {
		if strings.EqualFold(records[index].Config.ID, id) {
			records[index].Config = backendConfig
			records[index].Enabled = enabled
			found = true
		}
	}
	if !found {
		writeAPIError(w, http.StatusNotFound, "not_found", "后端不存在", "")
		return
	}
	candidate, previous, ok := a.prepareAdminCandidate(w, records, id, "update")
	if !ok {
		return
	}
	var saved configstore.Record
	err = a.commitAdminCandidate(candidate, previous, func() error {
		var commitErr error
		saved, commitErr = a.store.Update(a.ctx, configstore.Record{Config: backendConfig, Enabled: enabled}, expected)
		return commitErr
	})
	if err != nil {
		writeAdminCommitError(w, err)
		return
	}
	w.Header().Set("ETag", revisionETag(saved.Revision))
	writeJSON(w, http.StatusOK, makeBackendView(saved, a.adminStatusDetails()))
}

func (a *App) deleteAdminBackend(w http.ResponseWriter, req *http.Request, id string) {
	expected, ok := requireRevision(w, req)
	if !ok {
		return
	}
	a.reloadMu.Lock()
	defer a.reloadMu.Unlock()
	records, err := a.store.List(req.Context())
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "storage_failed", "无法读取配置", "")
		return
	}
	found := false
	desired := records[:0]
	for _, record := range records {
		if strings.EqualFold(record.Config.ID, id) {
			found = true
			if record.Revision != expected {
				writeAPIError(w, http.StatusConflict, "revision_conflict", "配置已在其他页面更新，请刷新后重试", "")
				return
			}
			continue
		}
		desired = append(desired, record)
	}
	if !found {
		writeAPIError(w, http.StatusNotFound, "not_found", "后端不存在", "")
		return
	}
	candidate, previous, ok := a.prepareAdminCandidate(w, desired, id, "delete")
	if !ok {
		return
	}
	err = a.commitAdminCandidate(candidate, previous, func() error {
		return a.store.Delete(a.ctx, id, expected)
	})
	if err != nil {
		writeAdminCommitError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) prepareAdminCandidate(w http.ResponseWriter, records []configstore.Record, id, action string) (*runtime, *runtime, bool) {
	cfg := configWithRecords(a.currentConfig(), records)
	if err := cfg.Validate(); err != nil {
		writeAPIError(w, http.StatusBadRequest, "validation_failed", err.Error(), "")
		return nil, nil, false
	}
	candidate, previous, err := a.buildCandidate(cfg)
	if err != nil {
		a.store.RecordFailure(a.ctx, id, action, "required backend unavailable")
		writeAPIError(w, http.StatusUnprocessableEntity, "required_backend_unavailable", "Required 后端无法连接，当前配置未改变", "")
		return nil, nil, false
	}
	return candidate, previous, true
}

func (a *App) commitAdminCandidate(candidate, previous *runtime, commit func() error) error {
	a.runtimeMu.Lock()
	if a.stopping.Load() || a.runtime != previous {
		a.runtimeMu.Unlock()
		candidate.close()
		return errRuntimeApply
	}
	if err := commit(); err != nil {
		a.runtimeMu.Unlock()
		candidate.close()
		return err
	}
	a.runtime = candidate
	a.runtimeMu.Unlock()
	go previous.drain(previous.cfg.Server.DrainTimeout.Duration)
	return nil
}

func (a *App) adminStatusDetails() map[string]backend.StatusDetail {
	runtime := a.currentRuntime()
	if runtime == nil {
		return map[string]backend.StatusDetail{}
	}
	return runtime.manager.StatusDetails()
}

func (a *App) probeAdminInput(w http.ResponseWriter, req *http.Request) {
	var input backendInput
	if !decodeAdminJSON(w, req, &input) {
		return
	}
	var current *config.BackendConfig
	if input.ID != "" {
		if record, getErr := a.store.Get(req.Context(), input.ID); getErr == nil {
			current = &record.Config
		}
	}
	backendConfig, _, err := backendFromInput(input, current)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "validation_failed", err.Error(), "")
		return
	}
	a.applyDefaultBackendTimeout(&backendConfig)
	probeConfig := configWithRecords(a.currentConfig(), []configstore.Record{{Config: backendConfig, Enabled: true}})
	if err := probeConfig.Validate(); err != nil {
		writeAPIError(w, http.StatusBadRequest, "validation_failed", err.Error(), "")
		return
	}
	backendConfig = probeConfig.Backends[0]
	a.runProbe(w, req, backendConfig, "")
}

func (a *App) probeStoredBackend(w http.ResponseWriter, req *http.Request, id string) {
	record, err := a.store.Get(req.Context(), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	a.runProbe(w, req, record.Config, record.Config.ID)
}

func (a *App) runProbe(w http.ResponseWriter, req *http.Request, cfg config.BackendConfig, storedID string) {
	result, err := backend.Probe(req.Context(), cfg, a.currentConfig().Server.RefreshInterval.Duration)
	if err != nil {
		payload, _ := json.Marshal(map[string]any{"ok": false, "message": "无法连接或发现此 MCP 后端"})
		if storedID != "" {
			_ = a.store.UpdateProbe(a.ctx, storedID, false, payload)
		}
		writeAPIError(w, http.StatusUnprocessableEntity, "backend_unavailable", "无法连接或发现此 MCP 后端", "")
		return
	}
	payload, _ := json.Marshal(result)
	if storedID != "" {
		_ = a.store.UpdateProbe(a.ctx, storedID, true, payload)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "result": result})
}

func backendFromInput(input backendInput, current *config.BackendConfig) (config.BackendConfig, bool, error) {
	enabled := true
	if input.Enabled != nil {
		enabled = *input.Enabled
	}
	timeout := time.Duration(0)
	var err error
	if input.RequestTimeout != "" {
		timeout, err = time.ParseDuration(input.RequestTimeout)
		if err != nil {
			return config.BackendConfig{}, false, fmt.Errorf("request_timeout 无效")
		}
	}
	value := config.BackendConfig{
		ID: input.ID, URL: input.URL, Required: input.Required,
		RequiredScopes: input.RequiredScopes, ToolRules: input.ToolRules,
		RequestTimeout: config.Duration{Duration: timeout}, AllowInsecureHTTP: input.AllowInsecureHTTP,
		Headers: make(map[string]string, len(input.Headers)),
	}
	seenHeaders := make(map[string]struct{}, len(input.Headers))
	for _, header := range input.Headers {
		if header.Name == "" {
			return config.BackendConfig{}, false, fmt.Errorf("Header 名称不能为空")
		}
		identity := strings.ToLower(header.Name)
		if _, exists := seenHeaders[identity]; exists {
			return config.BackendConfig{}, false, fmt.Errorf("Header 名称不能重复")
		}
		seenHeaders[identity] = struct{}{}
		if header.Value != nil {
			value.Headers[header.Name] = *header.Value
			continue
		}
		if current == nil {
			return config.BackendConfig{}, false, fmt.Errorf("新增 Header 必须填写值")
		}
		preserved, ok := headerValue(current.Headers, header.Name)
		if !ok {
			return config.BackendConfig{}, false, fmt.Errorf("新增 Header 必须填写值")
		}
		value.Headers[header.Name] = preserved
	}
	if input.OAuth != nil {
		secret := ""
		if input.OAuth.ClientSecret != nil {
			secret = *input.OAuth.ClientSecret
		} else if current != nil && current.OAuth != nil {
			secret = current.OAuth.ClientSecret
		} else {
			return config.BackendConfig{}, false, fmt.Errorf("OAuth client_secret 必须填写")
		}
		value.OAuth = &config.OAuthConfig{
			Type: input.OAuth.Type, Issuer: input.OAuth.Issuer, ClientID: input.OAuth.ClientID,
			ClientSecret: secret, Scopes: input.OAuth.Scopes,
		}
	}
	return value, enabled, nil
}

func (a *App) applyDefaultBackendTimeout(value *config.BackendConfig) {
	if value.RequestTimeout.Duration == 0 {
		value.RequestTimeout = a.currentConfig().Server.RequestTimeout
	}
}

func headerValue(headers map[string]string, name string) (string, bool) {
	for existing, value := range headers {
		if strings.EqualFold(existing, name) {
			return value, true
		}
	}
	return "", false
}

func makeBackendView(record configstore.Record, details map[string]backend.StatusDetail) backendView {
	view := backendView{
		ID: record.Config.ID, URL: record.Config.URL, Enabled: record.Enabled, Required: record.Config.Required,
		RequiredScopes: append([]string{}, record.Config.RequiredScopes...), ToolRules: append([]config.ToolRule{}, record.Config.ToolRules...),
		RequestTimeout: record.Config.RequestTimeout.Duration.String(), AllowInsecureHTTP: record.Config.AllowInsecureHTTP,
		Revision: record.Revision, CreatedAt: record.CreatedAt, UpdatedAt: record.UpdatedAt,
		LastProbeAt: record.LastProbeAt, LastProbeOK: record.LastProbeOK, LastProbe: record.LastProbe,
		Runtime: backendRuntimeView{State: "disabled"}, Headers: make([]headerView, 0, len(record.Config.Headers)),
	}
	for name := range record.Config.Headers {
		view.Headers = append(view.Headers, headerView{Name: name, Configured: true})
	}
	slices.SortFunc(view.Headers, func(a, b headerView) int { return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)) })
	if record.Config.OAuth != nil {
		view.OAuth = &oauthView{
			Type: record.Config.OAuth.Type, Issuer: record.Config.OAuth.Issuer,
			ClientID: record.Config.OAuth.ClientID, ClientSecretConfigured: record.Config.OAuth.ClientSecret != "",
			Scopes: append([]string{}, record.Config.OAuth.Scopes...),
		}
	}
	if record.Enabled {
		view.Runtime.State = "unavailable"
		if detail, ok := details[strings.ToLower(record.Config.ID)]; ok {
			if detail.Ready {
				view.Runtime.State = "ready"
			}
			view.Runtime.Tools = detail.Tools
			view.Runtime.Prompts = detail.Prompts
			view.Runtime.Resources = detail.Resources
			view.Runtime.ResourceTemplates = detail.ResourceTemplates
		}
	}
	return view
}

func decodeAdminJSON(w http.ResponseWriter, req *http.Request, target any) bool {
	return decodeStrictJSON(w, req, target, maximumAdminBodyBytes)
}

func decodeStrictJSON(w http.ResponseWriter, req *http.Request, target any, maximum int64) bool {
	mediaType, _, err := mime.ParseMediaType(req.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeAPIError(w, http.StatusUnsupportedMediaType, "invalid_content_type", "请求必须使用 application/json", "")
		return false
	}
	body, err := io.ReadAll(io.LimitReader(req.Body, maximum+1))
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_json", "无法读取请求 JSON", "")
		return false
	}
	if int64(len(body)) > maximum {
		writeAPIError(w, http.StatusRequestEntityTooLarge, "body_too_large", "请求体超过允许大小", "")
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeAPIError(w, http.StatusBadRequest, "invalid_json", "请求 JSON 无效", "")
		return false
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		writeAPIError(w, http.StatusBadRequest, "invalid_json", "请求只能包含一个 JSON 值", "")
		return false
	}
	return true
}

func requireRevision(w http.ResponseWriter, req *http.Request) (int64, bool) {
	raw := strings.Trim(req.Header.Get("If-Match"), `"`)
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value < 1 {
		writeAPIError(w, http.StatusPreconditionRequired, "revision_required", "修改操作必须携带有效 If-Match", "")
		return 0, false
	}
	return value, true
}

func revisionETag(revision int64) string {
	return `"` + strconv.FormatInt(revision, 10) + `"`
}

func writeStoreError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, configstore.ErrNotFound):
		writeAPIError(w, http.StatusNotFound, "not_found", "后端不存在", "")
	case errors.Is(err, configstore.ErrConflict):
		writeAPIError(w, http.StatusConflict, "revision_conflict", "配置已被修改，请刷新后重试", "")
	default:
		writeAPIError(w, http.StatusInternalServerError, "storage_failed", "配置存储操作失败", "")
	}
}

func writeAdminCommitError(w http.ResponseWriter, err error) {
	if errors.Is(err, errRuntimeApply) {
		writeAPIError(w, http.StatusServiceUnavailable, "runtime_apply_failed", "服务正在变更，配置未保存，请重试", "")
		return
	}
	writeStoreError(w, err)
}

func writeAPIError(w http.ResponseWriter, status int, code, message, field string) {
	writeJSON(w, status, apiErrorBody{Error: apiError{Code: code, Message: message, Field: field}})
}
