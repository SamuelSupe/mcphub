package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/SamuelSupe/mcphub/internal/backend"
	"github.com/SamuelSupe/mcphub/internal/configstore"
	"github.com/SamuelSupe/mcphub/internal/httptool"
	"github.com/SamuelSupe/mcphub/internal/openapiimport"
)

const maximumOpenAPIAdminBodyBytes = 6 << 20

var importIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)

type openAPIInspectInput struct {
	OriginType string `json:"origin_type"`
	SpecURL    string `json:"spec_url,omitempty"`
	Filename   string `json:"filename,omitempty"`
	Document   string `json:"document,omitempty"`
}

type openAPIImportInput struct {
	ID              string                    `json:"id"`
	OriginType      string                    `json:"origin_type"`
	SpecURL         string                    `json:"spec_url,omitempty"`
	Filename        string                    `json:"filename,omitempty"`
	Document        string                    `json:"document"`
	SHA256          string                    `json:"sha256"`
	RefreshInterval string                    `json:"refresh_interval,omitempty"`
	Selected        []openapiimport.Selection `json:"selected"`
}

type openAPIImportView struct {
	ID                 string                    `json:"id"`
	GroupID            string                    `json:"group_id"`
	OriginType         string                    `json:"origin_type"`
	SpecURL            string                    `json:"spec_url,omitempty"`
	Filename           string                    `json:"filename,omitempty"`
	RefreshInterval    string                    `json:"refresh_interval,omitempty"`
	OpenAPIVersion     string                    `json:"openapi_version"`
	Title              string                    `json:"title,omitempty"`
	Version            string                    `json:"version,omitempty"`
	SHA256             string                    `json:"sha256"`
	Selected           []openapiimport.Selection `json:"selected"`
	Revision           int64                     `json:"revision"`
	CreatedAt          time.Time                 `json:"created_at"`
	UpdatedAt          time.Time                 `json:"updated_at"`
	LastRefreshAt      *time.Time                `json:"last_refresh_at,omitempty"`
	LastRefreshOK      *bool                     `json:"last_refresh_ok,omitempty"`
	LastRefreshMessage string                    `json:"last_refresh_message,omitempty"`
}

func (a *App) serveAdminOpenAPIImportRoute(w http.ResponseWriter, req *http.Request, groupID string, parts []string) {
	switch {
	case len(parts) == 1 && parts[0] == "inspect" && req.Method == http.MethodPost:
		a.inspectAdminOpenAPI(w, req, groupID)
	case len(parts) == 0 && req.Method == http.MethodGet:
		a.listAdminOpenAPIImports(w, req, groupID)
	case len(parts) == 0 && req.Method == http.MethodPost:
		a.createAdminOpenAPIImport(w, req, groupID)
	case len(parts) == 1:
		switch req.Method {
		case http.MethodGet:
			a.getAdminOpenAPIImport(w, req, groupID, parts[0])
		case http.MethodPut:
			a.updateAdminOpenAPIImport(w, req, groupID, parts[0])
		case http.MethodDelete:
			a.deleteAdminOpenAPIImport(w, req, groupID, parts[0])
		default:
			writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "不支持此操作", "")
		}
	case len(parts) == 2 && parts[1] == "refresh" && req.Method == http.MethodPost:
		a.refreshAdminOpenAPIImport(w, req, groupID, parts[0])
	default:
		writeAPIError(w, http.StatusNotFound, "not_found", "OpenAPI 来源不存在", "")
	}
}

func (a *App) inspectAdminOpenAPI(w http.ResponseWriter, req *http.Request, groupID string) {
	group, err := a.store.GetToolGroup(req.Context(), groupID)
	if err != nil {
		writeToolStoreError(w, err, "工具组不存在")
		return
	}
	var input openAPIInspectInput
	if !decodeAdminJSONLimit(w, req, &input, maximumOpenAPIAdminBodyBytes) {
		return
	}
	document, err := a.openAPIDocument(req.Context(), group.Config, input.OriginType, input.SpecURL, input.Document)
	if err != nil {
		writeAPIError(w, http.StatusUnprocessableEntity, "openapi_unavailable", err.Error(), "")
		return
	}
	preview, err := openapiimport.Parse(document)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "openapi_invalid", "OpenAPI 文档无效或包含不支持的能力", "document")
		return
	}
	writeJSON(w, http.StatusOK, preview)
}

func (a *App) listAdminOpenAPIImports(w http.ResponseWriter, req *http.Request, groupID string) {
	if _, err := a.store.GetToolGroup(req.Context(), groupID); err != nil {
		writeToolStoreError(w, err, "工具组不存在")
		return
	}
	records, err := a.store.ListOpenAPIImports(req.Context(), groupID)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "storage_failed", "无法读取 OpenAPI 来源", "")
		return
	}
	views := make([]openAPIImportView, len(records))
	for index, record := range records {
		views[index] = makeOpenAPIImportView(record)
	}
	writeJSON(w, http.StatusOK, map[string]any{"imports": views})
}

func (a *App) getAdminOpenAPIImport(w http.ResponseWriter, req *http.Request, groupID, id string) {
	record, err := a.store.GetOpenAPIImport(req.Context(), groupID, id)
	if err != nil {
		writeToolStoreError(w, err, "OpenAPI 来源不存在")
		return
	}
	w.Header().Set("ETag", revisionETag(record.Revision))
	writeJSON(w, http.StatusOK, makeOpenAPIImportView(record))
}

func (a *App) createAdminOpenAPIImport(w http.ResponseWriter, req *http.Request, groupID string) {
	var input openAPIImportInput
	if !decodeAdminJSONLimit(w, req, &input, maximumOpenAPIAdminBodyBytes) {
		return
	}
	groupRecord, err := a.store.GetToolGroup(req.Context(), groupID)
	if err != nil {
		writeToolStoreError(w, err, "工具组不存在")
		return
	}
	record, tools, err := a.importFromInput(req.Context(), groupRecord.Config, input)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "validation_failed", err.Error(), "")
		return
	}
	groupID = groupRecord.Config.ID
	record.GroupID = groupID
	a.reloadMu.Lock()
	defer a.reloadMu.Unlock()
	latestGroup, err := a.store.GetToolGroup(req.Context(), groupID)
	if err != nil {
		writeToolStoreError(w, err, "工具组不存在")
		return
	}
	if latestGroup.Revision != groupRecord.Revision {
		writeAPIError(w, http.StatusConflict, "revision_conflict", "工具组已更新，请重新预览后提交", "group_id")
		return
	}
	groups, err := a.store.ListToolGroups(req.Context())
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "storage_failed", "无法读取工具组", "")
		return
	}
	desired := groupConfigs(groups)
	if err := addImportedTools(desired, groupID, tools, ""); err != nil {
		writeAPIError(w, http.StatusConflict, "revision_conflict", err.Error(), "selected")
		return
	}
	candidate, previous, ok := a.prepareToolGroupCandidate(w, desired)
	if !ok {
		return
	}
	var saved configstore.OpenAPIImportRecord
	var savedTools []configstore.HTTPToolRecord
	err = a.commitHTTPToolCandidate(candidate, previous, func() error {
		var commitErr error
		saved, savedTools, commitErr = a.store.CreateOpenAPIImport(a.ctx, record, tools)
		return commitErr
	})
	_ = savedTools
	if err != nil {
		writeAdminCommitError(w, err)
		return
	}
	w.Header().Set("ETag", revisionETag(saved.Revision))
	writeJSON(w, http.StatusCreated, makeOpenAPIImportView(saved))
}

func (a *App) updateAdminOpenAPIImport(w http.ResponseWriter, req *http.Request, groupID, id string) {
	expected, ok := requireRevision(w, req)
	if !ok {
		return
	}
	current, err := a.store.GetOpenAPIImport(req.Context(), groupID, id)
	if err != nil {
		writeToolStoreError(w, err, "OpenAPI 来源不存在")
		return
	}
	if current.Revision != expected {
		writeAPIError(w, http.StatusConflict, "revision_conflict", "OpenAPI 来源已更新，请刷新后重试", "")
		return
	}
	groupID, id = current.GroupID, current.Config.ID
	var input openAPIImportInput
	if !decodeAdminJSONLimit(w, req, &input, maximumOpenAPIAdminBodyBytes) {
		return
	}
	if input.ID != current.Config.ID {
		writeAPIError(w, http.StatusBadRequest, "validation_failed", "Import ID 创建后不能修改", "id")
		return
	}
	currentNames := make(map[string]string, len(current.Config.Selected))
	for _, selection := range current.Config.Selected {
		currentNames[selection.OperationKey] = selection.ToolName
	}
	for _, selection := range input.Selected {
		if previous, exists := currentNames[selection.OperationKey]; exists && previous != selection.ToolName {
			writeAPIError(w, http.StatusBadRequest, "validation_failed", "已发布 Tool name 不能修改；请删除后重新导入", "selected")
			return
		}
	}
	if input.OriginType == "upload" && input.Document == "" {
		input.Document = string(current.Document)
	}
	groupRecord, err := a.store.GetToolGroup(req.Context(), groupID)
	if err != nil {
		writeToolStoreError(w, err, "工具组不存在")
		return
	}
	record, tools, err := a.importFromInput(req.Context(), groupRecord.Config, input)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "validation_failed", err.Error(), "")
		return
	}
	record.GroupID = groupID
	a.replaceAdminOpenAPIImport(w, req, current, record, tools, groupRecord.Revision, "update")
}

func (a *App) refreshAdminOpenAPIImport(w http.ResponseWriter, req *http.Request, groupID, id string) {
	expected, ok := requireRevision(w, req)
	if !ok {
		return
	}
	current, err := a.store.GetOpenAPIImport(req.Context(), groupID, id)
	if err != nil {
		writeToolStoreError(w, err, "OpenAPI 来源不存在")
		return
	}
	if current.Revision != expected {
		writeAPIError(w, http.StatusConflict, "revision_conflict", "OpenAPI 来源已更新，请刷新后重试", "")
		return
	}
	groupID, id = current.GroupID, current.Config.ID
	if current.Config.OriginType != "url" {
		writeAPIError(w, http.StatusBadRequest, "validation_failed", "上传来源只能手工替换文档", "")
		return
	}
	group, err := a.store.GetToolGroup(req.Context(), groupID)
	if err != nil {
		writeToolStoreError(w, err, "工具组不存在")
		return
	}
	document, err := a.openAPIDocument(req.Context(), group.Config, "url", current.Config.SpecURL, "")
	if err != nil {
		if writeOpenAPIRefreshStateError(w, a.store.MarkOpenAPIRefreshFailure(a.ctx, groupID, id, current.Revision, "fetch_failed")) {
			return
		}
		writeAPIError(w, http.StatusUnprocessableEntity, "openapi_unavailable", "无法刷新 OpenAPI 来源，继续使用 last-known-good", "")
		return
	}
	sum := sha256.Sum256(document)
	if hex.EncodeToString(sum[:]) == current.SHA256 {
		if writeOpenAPIRefreshStateError(w, a.store.MarkOpenAPIRefreshSuccess(a.ctx, groupID, id, current.Revision)) {
			return
		}
		if updated, getErr := a.store.GetOpenAPIImport(req.Context(), groupID, id); getErr == nil {
			current = updated
		}
		w.Header().Set("ETag", revisionETag(current.Revision))
		writeJSON(w, http.StatusOK, makeOpenAPIImportView(current))
		return
	}
	preview, err := openapiimport.Parse(document)
	if err != nil {
		if writeOpenAPIRefreshStateError(w, a.store.MarkOpenAPIRefreshFailure(a.ctx, groupID, id, current.Revision, "invalid_spec")) {
			return
		}
		writeAPIError(w, http.StatusUnprocessableEntity, "openapi_invalid", "新规格无效，继续使用 last-known-good", "")
		return
	}
	tools, err := toolsForSelections(groupID, id, preview, current.Config.Selected, true)
	if err != nil {
		if writeOpenAPIRefreshStateError(w, a.store.MarkOpenAPIRefreshFailure(a.ctx, groupID, id, current.Revision, "selected_operation_invalid")) {
			return
		}
		writeAPIError(w, http.StatusUnprocessableEntity, "openapi_incompatible", "选中的 operation 已不兼容，继续使用 last-known-good", "")
		return
	}
	next := current
	next.Document, next.SHA256 = document, preview.SHA256
	next.Config.OpenAPIVersion, next.Config.Title, next.Config.Version = preview.OpenAPIVersion, preview.Title, preview.Version
	next.Config.Selected = existingSelections(preview, current.Config.Selected)
	a.replaceAdminOpenAPIImport(w, req, current, next, tools, group.Revision, "refresh")
}

func (a *App) deleteAdminOpenAPIImport(w http.ResponseWriter, req *http.Request, groupID, id string) {
	expected, ok := requireRevision(w, req)
	if !ok {
		return
	}
	current, err := a.store.GetOpenAPIImport(req.Context(), groupID, id)
	if err != nil {
		writeToolStoreError(w, err, "OpenAPI 来源不存在")
		return
	}
	if current.Revision != expected {
		writeAPIError(w, http.StatusConflict, "revision_conflict", "OpenAPI 来源已更新，请刷新后重试", "")
		return
	}
	groupID, id = current.GroupID, current.Config.ID
	a.reloadMu.Lock()
	defer a.reloadMu.Unlock()
	groups, err := a.store.ListToolGroups(req.Context())
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "storage_failed", "无法读取工具组", "")
		return
	}
	desired := groupConfigs(groups)
	removeImportedTools(desired, groupID, id)
	candidate, previous, ok := a.prepareToolGroupCandidate(w, desired)
	if !ok {
		return
	}
	if err := a.commitHTTPToolCandidate(candidate, previous, func() error { return a.store.DeleteOpenAPIImport(a.ctx, groupID, id, expected) }); err != nil {
		writeAdminCommitError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) replaceAdminOpenAPIImport(w http.ResponseWriter, req *http.Request, current, next configstore.OpenAPIImportRecord, tools []configstore.HTTPToolRecord, groupRevision int64, action string) {
	a.reloadMu.Lock()
	defer a.reloadMu.Unlock()
	latestGroup, err := a.store.GetToolGroup(req.Context(), current.GroupID)
	if err != nil {
		writeToolStoreError(w, err, "工具组不存在")
		return
	}
	if latestGroup.Revision != groupRevision {
		writeAPIError(w, http.StatusConflict, "revision_conflict", "工具组已更新，请重新预览后提交", "group_id")
		return
	}
	groups, err := a.store.ListToolGroups(req.Context())
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "storage_failed", "无法读取工具组", "")
		return
	}
	desired := groupConfigs(groups)
	removeImportedTools(desired, current.GroupID, current.Config.ID)
	if err := addImportedTools(desired, current.GroupID, tools, ""); err != nil {
		writeAPIError(w, http.StatusConflict, "revision_conflict", err.Error(), "selected")
		return
	}
	candidate, previous, ok := a.prepareToolGroupCandidate(w, desired)
	if !ok {
		return
	}
	var saved configstore.OpenAPIImportRecord
	err = a.commitHTTPToolCandidate(candidate, previous, func() error {
		var commitErr error
		saved, _, commitErr = a.store.ReplaceOpenAPIImport(a.ctx, next, current.Revision, tools, action)
		return commitErr
	})
	if err != nil {
		writeAdminCommitError(w, err)
		return
	}
	w.Header().Set("ETag", revisionETag(saved.Revision))
	writeJSON(w, http.StatusOK, makeOpenAPIImportView(saved))
}

func (a *App) importFromInput(ctx context.Context, group httptool.GroupConfig, input openAPIImportInput) (configstore.OpenAPIImportRecord, []configstore.HTTPToolRecord, error) {
	if !importIDPattern.MatchString(input.ID) {
		return configstore.OpenAPIImportRecord{}, nil, fmt.Errorf("import id must match %s", importIDPattern)
	}
	if len(input.Selected) == 0 {
		return configstore.OpenAPIImportRecord{}, nil, fmt.Errorf("至少选择一个 operation")
	}
	document, err := a.openAPIDocument(ctx, group, input.OriginType, input.SpecURL, input.Document)
	if err != nil {
		return configstore.OpenAPIImportRecord{}, nil, err
	}
	preview, err := openapiimport.Parse(document)
	if err != nil {
		return configstore.OpenAPIImportRecord{}, nil, fmt.Errorf("OpenAPI 文档无效或包含不支持的能力")
	}
	if input.SHA256 != "" && input.SHA256 != preview.SHA256 {
		return configstore.OpenAPIImportRecord{}, nil, fmt.Errorf("规格已变化，请重新预览后提交")
	}
	interval := time.Duration(0)
	if input.OriginType == "url" {
		interval = 15 * time.Minute
		if input.RefreshInterval != "" {
			interval, err = time.ParseDuration(input.RefreshInterval)
			if err != nil || interval < time.Minute || interval > 24*time.Hour {
				return configstore.OpenAPIImportRecord{}, nil, fmt.Errorf("refresh_interval 必须在 1m 到 24h 之间")
			}
		}
	} else if input.OriginType != "upload" {
		return configstore.OpenAPIImportRecord{}, nil, fmt.Errorf("origin_type 必须是 url 或 upload")
	}
	tools, err := toolsForSelections(group.ID, input.ID, preview, input.Selected, false)
	if err != nil {
		return configstore.OpenAPIImportRecord{}, nil, err
	}
	config := openapiimport.ImportConfig{
		ID: input.ID, OriginType: input.OriginType, SpecURL: input.SpecURL, Filename: input.Filename,
		RefreshInterval: interval, OpenAPIVersion: preview.OpenAPIVersion, Title: preview.Title, Version: preview.Version,
		Selected: slices.Clone(input.Selected),
	}
	return configstore.OpenAPIImportRecord{Config: config, Document: document, SHA256: preview.SHA256}, tools, nil
}

func toolsForSelections(groupID, importID string, preview openapiimport.Preview, selected []openapiimport.Selection, allowMissing bool) ([]configstore.HTTPToolRecord, error) {
	operations := make(map[string]openapiimport.Operation, len(preview.Operations))
	for _, operation := range preview.Operations {
		operations[operation.Key] = operation
	}
	seen := make(map[string]struct{}, len(selected))
	selectedOperations := make(map[string]struct{}, len(selected))
	tools := make([]configstore.HTTPToolRecord, 0, len(selected))
	for _, selection := range selected {
		if _, exists := selectedOperations[selection.OperationKey]; exists {
			return nil, fmt.Errorf("operation %s 被重复选择", selection.OperationKey)
		}
		selectedOperations[selection.OperationKey] = struct{}{}
		operation, ok := operations[selection.OperationKey]
		if !ok {
			if allowMissing {
				continue
			}
			return nil, fmt.Errorf("operation %s 不存在", selection.OperationKey)
		}
		if !operation.Eligible {
			return nil, fmt.Errorf("operation %s 无法转换: %s", selection.OperationKey, operation.Reason)
		}
		toolName := selection.ToolName
		if toolName == "" {
			toolName = operation.SuggestedToolName
		}
		identity := strings.ToLower(toolName)
		if _, exists := seen[identity]; exists {
			return nil, fmt.Errorf("导入的 tool name %q 重复", selection.ToolName)
		}
		seen[identity] = struct{}{}
		tool := operation.Tool
		tool.Name, tool.Enabled, tool.ImportID = toolName, selection.Enabled, importID
		if err := httptool.ValidateTool(groupID, &tool); err != nil {
			return nil, err
		}
		tools = append(tools, configstore.HTTPToolRecord{GroupID: groupID, Config: tool})
	}
	return tools, nil
}

func writeOpenAPIRefreshStateError(w http.ResponseWriter, err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, configstore.ErrConflict) || errors.Is(err, configstore.ErrNotFound) {
		writeAPIError(w, http.StatusConflict, "revision_conflict", "OpenAPI 来源已更新，请刷新后重试", "")
	} else {
		writeAPIError(w, http.StatusInternalServerError, "storage_failed", "无法更新 OpenAPI 刷新状态", "")
	}
	return true
}

func existingSelections(preview openapiimport.Preview, selected []openapiimport.Selection) []openapiimport.Selection {
	keys := make(map[string]struct{}, len(preview.Operations))
	for _, operation := range preview.Operations {
		keys[operation.Key] = struct{}{}
	}
	result := make([]openapiimport.Selection, 0, len(selected))
	for _, selection := range selected {
		if _, ok := keys[selection.OperationKey]; ok {
			result = append(result, selection)
		}
	}
	return result
}

func (a *App) openAPIDocument(ctx context.Context, group httptool.GroupConfig, originType, specURL, uploaded string) ([]byte, error) {
	if originType == "upload" {
		if len(uploaded) == 0 || len(uploaded) > openapiimport.MaximumDocumentBytes {
			return nil, fmt.Errorf("上传的 OpenAPI 文档必须小于 5 MiB")
		}
		return []byte(uploaded), nil
	}
	if originType != "url" {
		return nil, fmt.Errorf("origin_type 必须是 url 或 upload")
	}
	spec, err := url.Parse(specURL)
	if err != nil || !spec.IsAbs() || spec.Scheme != "https" || spec.Host == "" || spec.User != nil || spec.RawQuery != "" || spec.Fragment != "" {
		return nil, fmt.Errorf("spec_url 必须是无凭证、query 和 fragment 的 HTTPS URL")
	}
	base, _ := url.Parse(group.BaseURL)
	sameOrigin := spec.Scheme == base.Scheme && strings.EqualFold(spec.Host, base.Host)
	outbound := backend.OutboundHTTPConfig{RequestTimeout: group.RequestTimeout}
	if sameOrigin {
		outbound.Headers, outbound.OAuth = group.Headers, group.OAuth
	}
	client, err := backend.NewOutboundHTTPClient(ctx, outbound)
	if err != nil {
		return nil, fmt.Errorf("无法创建规格请求")
	}
	defer client.CloseIdleConnections()
	client.Timeout = group.RequestTimeout
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, spec.String(), nil)
	request.Header.Set("Accept", "application/json, application/yaml, text/yaml")
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("无法获取 OpenAPI 规格")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("规格服务器返回 HTTP %d", response.StatusCode)
	}
	document, err := io.ReadAll(io.LimitReader(response.Body, openapiimport.MaximumDocumentBytes+1))
	if err != nil || len(document) > openapiimport.MaximumDocumentBytes {
		return nil, fmt.Errorf("OpenAPI 规格超过 5 MiB 或无法读取")
	}
	return document, nil
}

func addImportedTools(groups []httptool.GroupConfig, groupID string, records []configstore.HTTPToolRecord, replacingImport string) error {
	for index := range groups {
		if !strings.EqualFold(groups[index].ID, groupID) {
			continue
		}
		seen := make(map[string]struct{}, len(groups[index].Tools)+len(records))
		for _, tool := range groups[index].Tools {
			if replacingImport != "" && strings.EqualFold(tool.ImportID, replacingImport) {
				continue
			}
			seen[strings.ToLower(tool.Name)] = struct{}{}
		}
		for _, record := range records {
			identity := strings.ToLower(record.Config.Name)
			if _, exists := seen[identity]; exists {
				return fmt.Errorf("Tool name %q 已在工具组中使用", record.Config.Name)
			}
			seen[identity] = struct{}{}
			groups[index].Tools = append(groups[index].Tools, record.Config)
		}
		return nil
	}
	return fmt.Errorf("工具组不存在")
}

func removeImportedTools(groups []httptool.GroupConfig, groupID, importID string) {
	for index := range groups {
		if !strings.EqualFold(groups[index].ID, groupID) {
			continue
		}
		tools := groups[index].Tools[:0]
		for _, tool := range groups[index].Tools {
			if !strings.EqualFold(tool.ImportID, importID) {
				tools = append(tools, tool)
			}
		}
		groups[index].Tools = tools
	}
}

func makeOpenAPIImportView(record configstore.OpenAPIImportRecord) openAPIImportView {
	interval := ""
	if record.Config.RefreshInterval > 0 {
		interval = record.Config.RefreshInterval.String()
	}
	return openAPIImportView{
		ID: record.Config.ID, GroupID: record.GroupID, OriginType: record.Config.OriginType,
		SpecURL: record.Config.SpecURL, Filename: record.Config.Filename, RefreshInterval: interval,
		OpenAPIVersion: record.Config.OpenAPIVersion, Title: record.Config.Title, Version: record.Config.Version,
		SHA256: record.SHA256, Selected: slices.Clone(record.Config.Selected), Revision: record.Revision,
		CreatedAt: record.CreatedAt, UpdatedAt: record.UpdatedAt, LastRefreshAt: record.LastRefreshAt,
		LastRefreshOK: record.LastRefreshOK, LastRefreshMessage: record.LastRefreshMessage,
	}
}

func decodeAdminJSONLimit(w http.ResponseWriter, req *http.Request, target any, maximum int64) bool {
	return decodeStrictJSON(w, req, target, maximum)
}
