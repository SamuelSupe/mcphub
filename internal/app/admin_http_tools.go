package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/SamuelSupe/mcphub/internal/backend"
	"github.com/SamuelSupe/mcphub/internal/config"
	"github.com/SamuelSupe/mcphub/internal/configstore"
	"github.com/SamuelSupe/mcphub/internal/httptool"
)

type toolGroupInput struct {
	ID                   string            `json:"id"`
	BaseURL              string            `json:"base_url"`
	Enabled              *bool             `json:"enabled,omitempty"`
	RequiredScopes       []string          `json:"required_scopes"`
	ToolRules            []config.ToolRule `json:"tool_rules"`
	RequestTimeout       string            `json:"request_timeout"`
	MaxResponseBodyBytes int64             `json:"max_response_body_bytes"`
	Headers              []headerInput     `json:"headers"`
	OAuth                *oauthInput       `json:"oauth,omitempty"`
}

type toolGroupView struct {
	ID                   string            `json:"id"`
	BaseURL              string            `json:"base_url"`
	Enabled              bool              `json:"enabled"`
	RequiredScopes       []string          `json:"required_scopes"`
	ToolRules            []config.ToolRule `json:"tool_rules"`
	RequestTimeout       string            `json:"request_timeout"`
	MaxResponseBodyBytes int64             `json:"max_response_body_bytes"`
	Headers              []headerView      `json:"headers"`
	OAuth                *oauthView        `json:"oauth,omitempty"`
	Revision             int64             `json:"revision"`
	CreatedAt            time.Time         `json:"created_at"`
	UpdatedAt            time.Time         `json:"updated_at"`
	LastProbeAt          *time.Time        `json:"last_probe_at,omitempty"`
	LastProbeOK          *bool             `json:"last_probe_ok,omitempty"`
	LastProbe            json.RawMessage   `json:"last_probe,omitempty"`
	Runtime              struct {
		State string `json:"state"`
		Tools int    `json:"tools"`
	} `json:"runtime"`
}

type httpToolInput struct {
	Name         string               `json:"name"`
	Description  string               `json:"description"`
	Enabled      *bool                `json:"enabled,omitempty"`
	Method       string               `json:"method"`
	Path         string               `json:"path"`
	Parameters   []httptool.Parameter `json:"parameters"`
	BodySchema   map[string]any       `json:"body_schema,omitempty"`
	BodyRequired bool                 `json:"body_required"`
	OutputSchema map[string]any       `json:"output_schema,omitempty"`
}

type httpToolView struct {
	GroupID      string               `json:"group_id"`
	Name         string               `json:"name"`
	Description  string               `json:"description"`
	Enabled      bool                 `json:"enabled"`
	Method       string               `json:"method"`
	Path         string               `json:"path"`
	Parameters   []httptool.Parameter `json:"parameters"`
	BodySchema   map[string]any       `json:"body_schema,omitempty"`
	BodyRequired bool                 `json:"body_required"`
	OutputSchema map[string]any       `json:"output_schema,omitempty"`
	Origin       string               `json:"origin"`
	ImportID     string               `json:"import_id,omitempty"`
	Revision     int64                `json:"revision"`
	CreatedAt    time.Time            `json:"created_at"`
	UpdatedAt    time.Time            `json:"updated_at"`
}

func (a *App) serveAdminToolGroups(w http.ResponseWriter, req *http.Request) {
	if req.Method == http.MethodPost {
		a.createAdminToolGroup(w, req)
		return
	}
	records, err := a.store.ListToolGroups(req.Context())
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "storage_failed", "无法读取工具组", "")
		return
	}
	views := make([]toolGroupView, 0, len(records))
	for _, record := range records {
		views = append(views, a.makeToolGroupView(record))
	}
	writeJSON(w, http.StatusOK, map[string]any{"tool_groups": views})
}

func (a *App) serveAdminToolGroupRoute(w http.ResponseWriter, req *http.Request, suffix string) {
	parts := strings.Split(suffix, "/")
	if len(parts) == 0 || parts[0] == "" {
		writeAPIError(w, http.StatusNotFound, "not_found", "工具组不存在", "")
		return
	}
	groupID := parts[0]
	switch {
	case len(parts) == 1:
		switch req.Method {
		case http.MethodGet:
			record, err := a.store.GetToolGroup(req.Context(), groupID)
			if err != nil {
				writeToolStoreError(w, err, "工具组不存在")
				return
			}
			w.Header().Set("ETag", revisionETag(record.Revision))
			writeJSON(w, http.StatusOK, a.makeToolGroupView(record))
		case http.MethodPut:
			a.updateAdminToolGroup(w, req, groupID)
		case http.MethodDelete:
			a.deleteAdminToolGroup(w, req, groupID)
		default:
			writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "不支持此操作", "")
		}
	case len(parts) == 2 && parts[1] == "probe" && req.Method == http.MethodPost:
		a.probeAdminToolGroup(w, req, groupID)
	case len(parts) == 2 && parts[1] == "tools":
		a.serveAdminHTTPTools(w, req, groupID)
	case len(parts) == 3 && parts[1] == "tools":
		a.serveAdminHTTPTool(w, req, groupID, parts[2])
	case len(parts) >= 2 && parts[1] == "imports":
		a.serveAdminOpenAPIImportRoute(w, req, groupID, parts[2:])
	default:
		writeAPIError(w, http.StatusNotFound, "not_found", "接口不存在", "")
	}
}

func (a *App) createAdminToolGroup(w http.ResponseWriter, req *http.Request) {
	var input toolGroupInput
	if !decodeAdminJSON(w, req, &input) {
		return
	}
	group, err := a.toolGroupFromInput(input, nil)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "validation_failed", err.Error(), "")
		return
	}
	a.reloadMu.Lock()
	defer a.reloadMu.Unlock()
	groups, err := a.store.ListToolGroups(req.Context())
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "storage_failed", "无法读取工具组", "")
		return
	}
	desired := groupConfigs(groups)
	desired = append(desired, group)
	candidate, previous, ok := a.prepareToolGroupCandidate(w, desired)
	if !ok {
		return
	}
	var saved configstore.ToolGroupRecord
	err = a.commitHTTPToolCandidate(candidate, previous, func() error {
		var commitErr error
		saved, commitErr = a.store.CreateToolGroup(a.ctx, configstore.ToolGroupRecord{Config: group})
		return commitErr
	})
	if err != nil {
		writeAdminCommitError(w, err)
		return
	}
	w.Header().Set("ETag", revisionETag(saved.Revision))
	writeJSON(w, http.StatusCreated, a.makeToolGroupView(saved))
}

func (a *App) updateAdminToolGroup(w http.ResponseWriter, req *http.Request, id string) {
	expected, ok := requireRevision(w, req)
	if !ok {
		return
	}
	current, err := a.store.GetToolGroup(req.Context(), id)
	if err != nil {
		writeToolStoreError(w, err, "工具组不存在")
		return
	}
	if current.Revision != expected {
		writeAPIError(w, http.StatusConflict, "revision_conflict", "工具组已更新，请刷新后重试", "")
		return
	}
	var input toolGroupInput
	if !decodeAdminJSON(w, req, &input) {
		return
	}
	if input.ID != current.Config.ID {
		writeAPIError(w, http.StatusBadRequest, "validation_failed", "工具组 ID 创建后不能修改", "id")
		return
	}
	group, err := a.toolGroupFromInput(input, &current.Config)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "validation_failed", err.Error(), "")
		return
	}
	a.reloadMu.Lock()
	defer a.reloadMu.Unlock()
	records, err := a.store.ListToolGroups(req.Context())
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "storage_failed", "无法读取工具组", "")
		return
	}
	desired := groupConfigs(records)
	for index := range desired {
		if strings.EqualFold(desired[index].ID, id) {
			group.Tools = desired[index].Tools
			desired[index] = group
		}
	}
	candidate, previous, ok := a.prepareToolGroupCandidate(w, desired)
	if !ok {
		return
	}
	var saved configstore.ToolGroupRecord
	err = a.commitHTTPToolCandidate(candidate, previous, func() error {
		var commitErr error
		saved, commitErr = a.store.UpdateToolGroup(a.ctx, configstore.ToolGroupRecord{Config: group}, expected)
		return commitErr
	})
	if err != nil {
		writeAdminCommitError(w, err)
		return
	}
	w.Header().Set("ETag", revisionETag(saved.Revision))
	writeJSON(w, http.StatusOK, a.makeToolGroupView(saved))
}

func (a *App) deleteAdminToolGroup(w http.ResponseWriter, req *http.Request, id string) {
	expected, ok := requireRevision(w, req)
	if !ok {
		return
	}
	a.reloadMu.Lock()
	defer a.reloadMu.Unlock()
	records, err := a.store.ListToolGroups(req.Context())
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "storage_failed", "无法读取工具组", "")
		return
	}
	var desired []httptool.GroupConfig
	found := false
	for _, record := range records {
		if strings.EqualFold(record.Config.ID, id) {
			found = true
			if record.Revision != expected {
				writeAPIError(w, http.StatusConflict, "revision_conflict", "工具组已更新，请刷新后重试", "")
				return
			}
			continue
		}
		desired = append(desired, record.Config)
	}
	if !found {
		writeAPIError(w, http.StatusNotFound, "not_found", "工具组不存在", "")
		return
	}
	candidate, previous, ok := a.prepareToolGroupCandidate(w, desired)
	if !ok {
		return
	}
	if err := a.commitHTTPToolCandidate(candidate, previous, func() error { return a.store.DeleteToolGroup(a.ctx, id, expected) }); err != nil {
		writeAdminCommitError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) serveAdminHTTPTools(w http.ResponseWriter, req *http.Request, groupID string) {
	if req.Method == http.MethodPost {
		a.createAdminHTTPTool(w, req, groupID)
		return
	}
	if req.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "不支持此操作", "")
		return
	}
	if _, err := a.store.GetToolGroup(req.Context(), groupID); err != nil {
		writeToolStoreError(w, err, "工具组不存在")
		return
	}
	records, err := a.store.ListHTTPTools(req.Context(), groupID)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "storage_failed", "无法读取 HTTP Tools", "")
		return
	}
	views := make([]httpToolView, len(records))
	for index, record := range records {
		views[index] = makeHTTPToolView(record)
	}
	writeJSON(w, http.StatusOK, map[string]any{"tools": views})
}

func (a *App) serveAdminHTTPTool(w http.ResponseWriter, req *http.Request, groupID, name string) {
	switch req.Method {
	case http.MethodGet:
		record, err := a.store.GetHTTPTool(req.Context(), groupID, name)
		if err != nil {
			writeToolStoreError(w, err, "HTTP Tool 不存在")
			return
		}
		w.Header().Set("ETag", revisionETag(record.Revision))
		writeJSON(w, http.StatusOK, makeHTTPToolView(record))
	case http.MethodPut:
		a.updateAdminHTTPTool(w, req, groupID, name)
	case http.MethodDelete:
		a.deleteAdminHTTPTool(w, req, groupID, name)
	default:
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "不支持此操作", "")
	}
}

func (a *App) createAdminHTTPTool(w http.ResponseWriter, req *http.Request, groupID string) {
	var input httpToolInput
	if !decodeAdminJSON(w, req, &input) {
		return
	}
	tool := toolFromInput(input)
	if err := httptool.ValidateTool(groupID, &tool); err != nil {
		writeAPIError(w, http.StatusBadRequest, "validation_failed", err.Error(), "")
		return
	}
	a.reloadMu.Lock()
	defer a.reloadMu.Unlock()
	records, err := a.store.ListToolGroups(req.Context())
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "storage_failed", "无法读取工具组", "")
		return
	}
	desired := groupConfigs(records)
	found := false
	canonicalGroupID := ""
	for index := range desired {
		if !strings.EqualFold(desired[index].ID, groupID) {
			continue
		}
		found = true
		canonicalGroupID = desired[index].ID
		for _, existing := range desired[index].Tools {
			if strings.EqualFold(existing.Name, tool.Name) {
				writeAPIError(w, http.StatusConflict, "revision_conflict", "Tool name 已存在", "name")
				return
			}
		}
		desired[index].Tools = append(desired[index].Tools, tool)
	}
	if !found {
		writeAPIError(w, http.StatusNotFound, "not_found", "工具组不存在", "")
		return
	}
	candidate, previous, ok := a.prepareToolGroupCandidate(w, desired)
	if !ok {
		return
	}
	var saved configstore.HTTPToolRecord
	err = a.commitHTTPToolCandidate(candidate, previous, func() error {
		var commitErr error
		saved, commitErr = a.store.CreateHTTPTool(a.ctx, configstore.HTTPToolRecord{GroupID: canonicalGroupID, Config: tool})
		return commitErr
	})
	if err != nil {
		writeAdminCommitError(w, err)
		return
	}
	w.Header().Set("ETag", revisionETag(saved.Revision))
	writeJSON(w, http.StatusCreated, makeHTTPToolView(saved))
}

func (a *App) updateAdminHTTPTool(w http.ResponseWriter, req *http.Request, groupID, name string) {
	expected, ok := requireRevision(w, req)
	if !ok {
		return
	}
	current, err := a.store.GetHTTPTool(req.Context(), groupID, name)
	if err != nil {
		writeToolStoreError(w, err, "HTTP Tool 不存在")
		return
	}
	if current.Revision != expected {
		writeAPIError(w, http.StatusConflict, "revision_conflict", "HTTP Tool 已更新，请刷新后重试", "")
		return
	}
	if current.Config.Origin == "openapi" {
		writeAPIError(w, http.StatusBadRequest, "validation_failed", "OpenAPI Tool 必须通过对应来源管理", "")
		return
	}
	var input httpToolInput
	if !decodeAdminJSON(w, req, &input) {
		return
	}
	if input.Name != current.Config.Name {
		writeAPIError(w, http.StatusBadRequest, "validation_failed", "Tool name 创建后不能修改", "name")
		return
	}
	tool := toolFromInput(input)
	tool.Origin, tool.ImportID = current.Config.Origin, current.Config.ImportID
	if err := httptool.ValidateTool(groupID, &tool); err != nil {
		writeAPIError(w, http.StatusBadRequest, "validation_failed", err.Error(), "")
		return
	}
	a.applyHTTPToolUpdate(w, req, current.GroupID, tool, expected)
}

func (a *App) deleteAdminHTTPTool(w http.ResponseWriter, req *http.Request, groupID, name string) {
	expected, ok := requireRevision(w, req)
	if !ok {
		return
	}
	current, err := a.store.GetHTTPTool(req.Context(), groupID, name)
	if err != nil {
		writeToolStoreError(w, err, "HTTP Tool 不存在")
		return
	}
	if current.Config.Origin == "openapi" {
		writeAPIError(w, http.StatusBadRequest, "validation_failed", "OpenAPI Tool 必须通过对应来源管理", "")
		return
	}
	a.reloadMu.Lock()
	defer a.reloadMu.Unlock()
	records, err := a.store.ListToolGroups(req.Context())
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "storage_failed", "无法读取工具组", "")
		return
	}
	desired := groupConfigs(records)
	found := false
	for groupIndex := range desired {
		if !strings.EqualFold(desired[groupIndex].ID, groupID) {
			continue
		}
		tools := desired[groupIndex].Tools[:0]
		for _, tool := range desired[groupIndex].Tools {
			if strings.EqualFold(tool.Name, name) {
				found = true
				continue
			}
			tools = append(tools, tool)
		}
		desired[groupIndex].Tools = tools
	}
	if !found {
		writeAPIError(w, http.StatusNotFound, "not_found", "HTTP Tool 不存在", "")
		return
	}
	candidate, previous, ok := a.prepareToolGroupCandidate(w, desired)
	if !ok {
		return
	}
	if err := a.commitHTTPToolCandidate(candidate, previous, func() error { return a.store.DeleteHTTPTool(a.ctx, current.GroupID, current.Config.Name, expected) }); err != nil {
		writeAdminCommitError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) applyHTTPToolUpdate(w http.ResponseWriter, req *http.Request, groupID string, tool httptool.ToolConfig, expected int64) {
	a.reloadMu.Lock()
	defer a.reloadMu.Unlock()
	records, err := a.store.ListToolGroups(req.Context())
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "storage_failed", "无法读取工具组", "")
		return
	}
	desired := groupConfigs(records)
	found := false
	for groupIndex := range desired {
		if !strings.EqualFold(desired[groupIndex].ID, groupID) {
			continue
		}
		for toolIndex := range desired[groupIndex].Tools {
			if strings.EqualFold(desired[groupIndex].Tools[toolIndex].Name, tool.Name) {
				desired[groupIndex].Tools[toolIndex] = tool
				found = true
			}
		}
	}
	if !found {
		writeAPIError(w, http.StatusNotFound, "not_found", "HTTP Tool 不存在", "")
		return
	}
	candidate, previous, ok := a.prepareToolGroupCandidate(w, desired)
	if !ok {
		return
	}
	var saved configstore.HTTPToolRecord
	err = a.commitHTTPToolCandidate(candidate, previous, func() error {
		var commitErr error
		saved, commitErr = a.store.UpdateHTTPTool(a.ctx, configstore.HTTPToolRecord{GroupID: groupID, Config: tool}, expected)
		return commitErr
	})
	if err != nil {
		writeAdminCommitError(w, err)
		return
	}
	w.Header().Set("ETag", revisionETag(saved.Revision))
	writeJSON(w, http.StatusOK, makeHTTPToolView(saved))
}

func (a *App) prepareToolGroupCandidate(w http.ResponseWriter, groups []httptool.GroupConfig) (*httptool.Manager, *runtime, bool) {
	if err := httptool.ValidateGroups(groups, backendIDs(a.currentConfig())); err != nil {
		writeAPIError(w, http.StatusBadRequest, "validation_failed", err.Error(), "")
		return nil, nil, false
	}
	previous := a.currentRuntime()
	if previous == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "runtime_apply_failed", "服务正在关闭", "")
		return nil, nil, false
	}
	candidate, err := httptool.NewManager(previous.ctx, groups, a.logger)
	if err != nil {
		writeAPIError(w, http.StatusUnprocessableEntity, "runtime_apply_failed", "工具组运行时无法应用，当前配置未改变", "")
		return nil, nil, false
	}
	return candidate, previous, true
}

func (a *App) commitHTTPToolCandidate(candidate *httptool.Manager, runtime *runtime, commit func() error) error {
	a.runtimeMu.Lock()
	defer a.runtimeMu.Unlock()
	if a.stopping.Load() || a.runtime != runtime {
		candidate.Close()
		return errRuntimeApply
	}
	if err := commit(); err != nil {
		candidate.Close()
		return err
	}
	runtime.hub.ReplaceHTTPTools(candidate)
	return nil
}

func (a *App) toolGroupFromInput(input toolGroupInput, current *httptool.GroupConfig) (httptool.GroupConfig, error) {
	enabled := true
	if input.Enabled != nil {
		enabled = *input.Enabled
	}
	timeout := a.currentConfig().Server.RequestTimeout.Duration
	if input.RequestTimeout != "" {
		value, err := time.ParseDuration(input.RequestTimeout)
		if err != nil {
			return httptool.GroupConfig{}, fmt.Errorf("request_timeout 无效")
		}
		timeout = value
	}
	value := httptool.GroupConfig{
		ID: input.ID, BaseURL: input.BaseURL, Enabled: enabled, RequiredScopes: input.RequiredScopes,
		ToolRules: input.ToolRules, RequestTimeout: timeout, MaxResponseBodyBytes: input.MaxResponseBodyBytes,
		Headers: make(map[string]string, len(input.Headers)),
	}
	seen := make(map[string]struct{}, len(input.Headers))
	for _, header := range input.Headers {
		identity := strings.ToLower(header.Name)
		if header.Name == "" || containsKey(seen, identity) {
			return httptool.GroupConfig{}, fmt.Errorf("Header 名称不能为空或重复")
		}
		seen[identity] = struct{}{}
		if header.Value != nil {
			value.Headers[header.Name] = *header.Value
		} else if currentValue, ok := preservedHeader(current, header.Name); ok {
			value.Headers[header.Name] = currentValue
		} else {
			return httptool.GroupConfig{}, fmt.Errorf("新增 Header 必须填写值")
		}
	}
	if input.OAuth != nil {
		secret := ""
		if input.OAuth.ClientSecret != nil {
			secret = *input.OAuth.ClientSecret
		} else if current != nil && current.OAuth != nil {
			secret = current.OAuth.ClientSecret
		} else {
			return httptool.GroupConfig{}, fmt.Errorf("OAuth client_secret 必须填写")
		}
		value.OAuth = &config.OAuthConfig{Type: input.OAuth.Type, Issuer: input.OAuth.Issuer, ClientID: input.OAuth.ClientID, ClientSecret: secret, Scopes: input.OAuth.Scopes}
	}
	if current != nil {
		value.Tools = current.Tools
	}
	if err := httptool.ValidateGroup(&value); err != nil {
		return httptool.GroupConfig{}, err
	}
	return value, nil
}

func (a *App) probeAdminToolGroup(w http.ResponseWriter, req *http.Request, id string) {
	record, err := a.store.GetToolGroup(req.Context(), id)
	if err != nil {
		writeToolStoreError(w, err, "工具组不存在")
		return
	}
	started := time.Now()
	client, err := backend.NewOutboundHTTPClient(req.Context(), backend.OutboundHTTPConfig{Headers: record.Config.Headers, OAuth: record.Config.OAuth, RequestTimeout: record.Config.RequestTimeout})
	if err == nil {
		defer client.CloseIdleConnections()
		probeCtx, cancel := context.WithTimeout(req.Context(), record.Config.RequestTimeout)
		defer cancel()
		var request *http.Request
		request, err = http.NewRequestWithContext(probeCtx, http.MethodHead, record.Config.BaseURL, nil)
		if err == nil {
			var response *http.Response
			response, err = client.Do(request)
			if response != nil {
				_ = response.Body.Close()
				if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
					err = errors.New("upstream rejected credentials")
				}
			}
		}
	}
	result := map[string]any{"ok": err == nil, "duration_ms": time.Since(started).Milliseconds()}
	if err != nil {
		payload, _ := json.Marshal(result)
		_ = a.store.UpdateToolGroupProbe(a.ctx, id, false, payload)
		writeAPIError(w, http.StatusUnprocessableEntity, "upstream_unavailable", "工具组 Base URL 或认证不可用", "")
		return
	}
	payload, _ := json.Marshal(result)
	_ = a.store.UpdateToolGroupProbe(a.ctx, id, true, payload)
	writeJSON(w, http.StatusOK, result)
}

func (a *App) makeToolGroupView(record configstore.ToolGroupRecord) toolGroupView {
	view := toolGroupView{
		ID: record.Config.ID, BaseURL: record.Config.BaseURL, Enabled: record.Config.Enabled,
		RequiredScopes: slices.Clone(record.Config.RequiredScopes), ToolRules: slices.Clone(record.Config.ToolRules),
		RequestTimeout: record.Config.RequestTimeout.String(), MaxResponseBodyBytes: record.Config.MaxResponseBodyBytes,
		Revision: record.Revision, CreatedAt: record.CreatedAt, UpdatedAt: record.UpdatedAt,
		LastProbeAt: record.LastProbeAt, LastProbeOK: record.LastProbeOK, LastProbe: record.LastProbe,
	}
	view.Runtime.State = "disabled"
	view.Runtime.Tools = len(record.Config.Tools)
	if record.Config.Enabled {
		view.Runtime.State = "active"
	}
	for name := range record.Config.Headers {
		view.Headers = append(view.Headers, headerView{Name: name, Configured: true})
	}
	slices.SortFunc(view.Headers, func(a, b headerView) int { return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)) })
	if record.Config.OAuth != nil {
		view.OAuth = &oauthView{Type: record.Config.OAuth.Type, Issuer: record.Config.OAuth.Issuer, ClientID: record.Config.OAuth.ClientID, ClientSecretConfigured: true, Scopes: slices.Clone(record.Config.OAuth.Scopes)}
	}
	return view
}

func makeHTTPToolView(record configstore.HTTPToolRecord) httpToolView {
	return httpToolView{
		GroupID: record.GroupID, Name: record.Config.Name, Description: record.Config.Description,
		Enabled: record.Config.Enabled, Method: record.Config.Method, Path: record.Config.Path,
		Parameters: record.Config.Parameters, BodySchema: record.Config.BodySchema, BodyRequired: record.Config.BodyRequired,
		OutputSchema: record.Config.OutputSchema, Origin: record.Config.Origin, ImportID: record.Config.ImportID,
		Revision: record.Revision, CreatedAt: record.CreatedAt, UpdatedAt: record.UpdatedAt,
	}
}

func toolFromInput(input httpToolInput) httptool.ToolConfig {
	enabled := true
	if input.Enabled != nil {
		enabled = *input.Enabled
	}
	return httptool.ToolConfig{Name: input.Name, Description: input.Description, Enabled: enabled, Method: input.Method, Path: input.Path, Parameters: input.Parameters, BodySchema: input.BodySchema, BodyRequired: input.BodyRequired, OutputSchema: input.OutputSchema, Origin: "manual"}
}

func groupConfigs(records []configstore.ToolGroupRecord) []httptool.GroupConfig {
	result := make([]httptool.GroupConfig, len(records))
	for index, record := range records {
		result[index] = record.Config
	}
	return result
}

func writeToolStoreError(w http.ResponseWriter, err error, notFound string) {
	switch {
	case errors.Is(err, configstore.ErrNotFound):
		writeAPIError(w, http.StatusNotFound, "not_found", notFound, "")
	case errors.Is(err, configstore.ErrConflict):
		writeAPIError(w, http.StatusConflict, "revision_conflict", "配置已被修改，请刷新后重试", "")
	default:
		writeAPIError(w, http.StatusInternalServerError, "storage_failed", "工具配置存储失败", "")
	}
}

func preservedHeader(current *httptool.GroupConfig, name string) (string, bool) {
	if current == nil {
		return "", false
	}
	for existing, value := range current.Headers {
		if strings.EqualFold(existing, name) {
			return value, true
		}
	}
	return "", false
}

func containsKey(values map[string]struct{}, key string) bool {
	_, ok := values[key]
	return ok
}
