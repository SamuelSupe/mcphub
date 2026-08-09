package httptool

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/SamuelSupe/mcphub/internal/backend"
)

type Manager struct {
	logger  *slog.Logger
	groups  map[string]*groupRuntime
	aliases map[string]*groupRuntime
	ids     []string
}

type groupRuntime struct {
	config      GroupConfig
	client      *http.Client
	definitions map[string]Definition
	tools       map[string]ToolConfig
}

type Definition struct {
	GroupID        string
	Original       string
	Tool           *mcp.Tool
	RequiredScopes []string
}

type StatusDetail struct {
	ID      string `json:"id"`
	Enabled bool   `json:"enabled"`
	Tools   int    `json:"tools"`
}

func NewManager(ctx context.Context, groups []GroupConfig, logger *slog.Logger) (*Manager, error) {
	if err := ValidateGroups(groups, nil); err != nil {
		return nil, err
	}
	manager := &Manager{
		logger: logger, groups: make(map[string]*groupRuntime, len(groups)),
		aliases: make(map[string]*groupRuntime, len(groups)), ids: make([]string, 0, len(groups)),
	}
	for _, group := range groups {
		runtime := &groupRuntime{config: cloneGroup(group), definitions: make(map[string]Definition), tools: make(map[string]ToolConfig)}
		manager.groups[group.ID] = runtime
		for _, tool := range group.Tools {
			definition, err := buildDefinition(group, tool)
			if err != nil {
				manager.Close()
				return nil, err
			}
			if group.Enabled && tool.Enabled {
				runtime.definitions[definition.Tool.Name] = definition
				runtime.tools[strings.ToLower(tool.Name)] = cloneTool(tool)
			}
		}
		if group.Enabled {
			client, err := backend.NewOutboundHTTPClient(ctx, backend.OutboundHTTPConfig{
				Headers: group.Headers, OAuth: group.OAuth, RequestTimeout: group.RequestTimeout,
			})
			if err != nil {
				manager.Close()
				return nil, fmt.Errorf("initialize HTTP tool group %s: %w", group.ID, err)
			}
			client.Timeout = group.RequestTimeout
			runtime.client = client
		}
		manager.aliases[strings.ToLower(group.ID)] = runtime
		manager.ids = append(manager.ids, group.ID)
	}
	slices.Sort(manager.ids)
	return manager, nil
}

func validateDefinition(definition Definition) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("JSON Schema is not accepted by the MCP SDK")
		}
	}()
	server := mcp.NewServer(&mcp.Implementation{Name: "mcphub-schema-check", Version: "0"}, nil)
	server.AddTool(definition.Tool, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{}, nil
	})
	return nil
}

func (m *Manager) IDs() []string { return slices.Clone(m.ids) }

func (m *Manager) Close() {
	for _, runtime := range m.groups {
		if runtime.client != nil {
			runtime.client.CloseIdleConnections()
		}
	}
}

func (m *Manager) AllScopes() []string {
	set := make(map[string]struct{})
	for _, runtime := range m.groups {
		for _, scope := range runtime.config.RequiredScopes {
			set[scope] = struct{}{}
		}
		for _, rule := range runtime.config.ToolRules {
			for _, scope := range rule.RequiredScopes {
				set[scope] = struct{}{}
			}
		}
	}
	result := make([]string, 0, len(set))
	for scope := range set {
		result = append(result, scope)
	}
	slices.Sort(result)
	return result
}

func (m *Manager) Definitions(id string) map[string]Definition {
	runtime, ok := m.groups[id]
	if !ok {
		runtime, ok = m.aliases[strings.ToLower(id)]
	}
	if !ok || !runtime.config.Enabled {
		return nil
	}
	return runtime.definitions
}

func (m *Manager) StatusDetails() map[string]StatusDetail {
	result := make(map[string]StatusDetail, len(m.groups))
	for _, id := range m.ids {
		runtime := m.groups[id]
		result[strings.ToLower(id)] = StatusDetail{ID: id, Enabled: runtime.config.Enabled, Tools: len(runtime.definitions)}
	}
	return result
}

func (m *Manager) AllowedProfile(scopes []string) ([]string, []string) {
	granted := make(map[string]struct{}, len(scopes))
	for _, scope := range scopes {
		granted[scope] = struct{}{}
	}
	var allowed []string
	relevant := make(map[string]struct{})
	for _, id := range m.ids {
		runtime := m.groups[id]
		if !runtime.config.Enabled || !hasScopes(granted, runtime.config.RequiredScopes) {
			continue
		}
		allowed = append(allowed, id)
		for _, rule := range runtime.config.ToolRules {
			for _, scope := range rule.RequiredScopes {
				if _, ok := granted[scope]; ok {
					relevant[scope] = struct{}{}
				}
			}
		}
	}
	profile := make([]string, 0, len(relevant))
	for scope := range relevant {
		profile = append(profile, scope)
	}
	slices.Sort(profile)
	return allowed, profile
}

func (m *Manager) MissingScopes(id string, scopes []string) ([]string, bool) {
	runtime, ok := m.aliases[strings.ToLower(id)]
	if !ok || !runtime.config.Enabled {
		return nil, false
	}
	granted := make(map[string]struct{}, len(scopes))
	for _, scope := range scopes {
		granted[scope] = struct{}{}
	}
	var missing []string
	for _, scope := range runtime.config.RequiredScopes {
		if _, ok := granted[scope]; !ok {
			missing = append(missing, scope)
		}
	}
	slices.Sort(missing)
	return missing, true
}

func (m *Manager) Call(ctx context.Context, groupID, toolName string, arguments json.RawMessage) (*mcp.CallToolResult, error) {
	runtime, ok := m.aliases[strings.ToLower(groupID)]
	if !ok || !runtime.config.Enabled || runtime.client == nil {
		return nil, fmt.Errorf("HTTP tool group unavailable")
	}
	tool, ok := runtime.tools[strings.ToLower(toolName)]
	if !ok {
		return nil, fmt.Errorf("HTTP tool unavailable")
	}
	var args map[string]any
	if len(arguments) == 0 {
		args = make(map[string]any)
	} else {
		decoded, err := decodeJSONValue(arguments)
		if err != nil {
			return nil, fmt.Errorf("invalid tool arguments")
		}
		var ok bool
		args, ok = decoded.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("invalid tool arguments")
		}
	}
	request, err := buildRequest(ctx, runtime.config, tool, args)
	if err != nil {
		return nil, err
	}
	started := time.Now()
	response, err := runtime.client.Do(request)
	if err != nil {
		m.logger.Warn("HTTP tool request failed", "tool_group", runtime.config.ID, "tool", tool.Name, "duration_ms", time.Since(started).Milliseconds(), "error_type", fmt.Sprintf("%T", err))
		return errorResult("upstream API is unavailable"), nil
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, runtime.config.MaxResponseBodyBytes+1))
	if err != nil {
		return errorResult("upstream API response could not be read"), nil
	}
	if int64(len(body)) > runtime.config.MaxResponseBodyBytes {
		return errorResult("upstream API response exceeded the configured limit"), nil
	}
	return responseResult(response.StatusCode, response.Header.Get("Content-Type"), body, runtime.config), nil
}

func buildDefinition(group GroupConfig, tool ToolConfig) (Definition, error) {
	properties := make(map[string]any, len(tool.Parameters)+1)
	var required []string
	for _, parameter := range tool.Parameters {
		schema := cloneMap(parameter.Schema)
		if parameter.Description != "" {
			schema["description"] = parameter.Description
		}
		properties[parameter.Argument] = schema
		if parameter.Required {
			required = append(required, parameter.Argument)
		}
	}
	if len(tool.BodySchema) > 0 {
		properties["body"] = cloneMap(tool.BodySchema)
		if tool.BodyRequired {
			required = append(required, "body")
		}
	}
	slices.Sort(required)
	input := map[string]any{"type": "object", "properties": properties, "additionalProperties": false}
	if len(required) > 0 {
		input["required"] = required
	}
	publicName := group.ID + "." + tool.Name
	if len(publicName) > 128 {
		return Definition{}, fmt.Errorf("tool group %q tool %q: public MCP name exceeds 128 characters", group.ID, tool.Name)
	}
	definition := &mcp.Tool{Name: publicName, Description: tool.Description, InputSchema: input}
	if len(tool.OutputSchema) > 0 {
		definition.OutputSchema = cloneMap(tool.OutputSchema)
	}
	return Definition{GroupID: group.ID, Original: tool.Name, Tool: definition, RequiredScopes: group.RequiredToolScopes(tool.Name)}, nil
}

func buildRequest(ctx context.Context, group GroupConfig, tool ToolConfig, args map[string]any) (*http.Request, error) {
	path := tool.Path
	query := make(url.Values)
	headers := make(http.Header)
	for _, parameter := range tool.Parameters {
		value, exists := args[parameter.Argument]
		if !exists {
			value, exists = parameter.Schema["default"]
			if !exists && parameter.Required {
				return nil, fmt.Errorf("required argument %q is missing", parameter.Argument)
			}
			if !exists {
				continue
			}
		}
		values, err := stringValues(value)
		if err != nil {
			return nil, fmt.Errorf("argument %q has an unsupported value", parameter.Argument)
		}
		switch parameter.In {
		case "path":
			if len(values) == 0 {
				return nil, fmt.Errorf("path argument %q must not be empty", parameter.Argument)
			}
			escaped := make([]string, len(values))
			for index, value := range values {
				if value == "." || value == ".." || strings.ContainsAny(value, `/\`) {
					return nil, fmt.Errorf("path argument %q must be a single safe segment", parameter.Argument)
				}
				escaped[index] = url.PathEscape(value)
			}
			path = strings.ReplaceAll(path, "{"+parameter.Name+"}", strings.Join(escaped, ","))
		case "query":
			for _, value := range values {
				query.Add(parameter.Name, value)
			}
		case "header":
			headers.Set(parameter.Name, strings.Join(values, ","))
		}
	}
	base, _ := url.Parse(group.BaseURL)
	rawPath := strings.TrimRight(base.EscapedPath(), "/") + path
	decodedPath, err := url.PathUnescape(rawPath)
	if err != nil {
		return nil, fmt.Errorf("upstream path is invalid")
	}
	base.Path, base.RawPath = decodedPath, rawPath
	base.RawQuery = query.Encode()
	var body io.Reader
	if value, ok := args["body"]; ok {
		encoded, err := json.Marshal(value)
		if err != nil {
			return nil, fmt.Errorf("body argument is not valid JSON")
		}
		body = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, tool.Method, base.String(), body)
	if err != nil {
		return nil, fmt.Errorf("build upstream request")
	}
	for name, values := range headers {
		request.Header[name] = values
	}
	request.Header.Set("Accept", "application/json, text/plain;q=0.9")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	return request, nil
}

func stringValues(value any) ([]string, error) {
	switch typed := value.(type) {
	case string:
		return []string{typed}, nil
	case float64:
		return []string{strconv.FormatFloat(typed, 'f', -1, 64)}, nil
	case json.Number:
		return []string{typed.String()}, nil
	case bool:
		return []string{strconv.FormatBool(typed)}, nil
	case []any:
		values := make([]string, 0, len(typed))
		for _, item := range typed {
			encoded, err := stringValues(item)
			if err != nil || len(encoded) != 1 {
				return nil, errors.New("nested arrays are unsupported")
			}
			values = append(values, encoded[0])
		}
		return values, nil
	default:
		return nil, errors.New("objects are unsupported")
	}
}

func responseResult(status int, contentType string, body []byte, group GroupConfig) *mcp.CallToolResult {
	mediaType, _, _ := mime.ParseMediaType(contentType)
	jsonContent := mediaType == "application/json" || strings.HasSuffix(mediaType, "+json")
	textContent := strings.HasPrefix(mediaType, "text/") || mediaType == ""
	redaction := newResponseRedaction(group)
	if status == http.StatusNoContent || len(body) == 0 {
		text := fmt.Sprintf("upstream API returned HTTP %d", status)
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}, IsError: status < 200 || status >= 300}
	}
	if status < 200 || status >= 300 {
		redacted := redactResponse(body, jsonContent, redaction)
		text := fmt.Sprintf("upstream API returned HTTP %d: %s", status, redacted)
		var publicBody any = redacted
		if jsonContent {
			if decoded, err := decodeJSONValue([]byte(redacted)); err == nil {
				publicBody = decoded
			}
		}
		return &mcp.CallToolResult{
			Content:           []mcp.Content{&mcp.TextContent{Text: text}},
			StructuredContent: map[string]any{"status": status, "body": publicBody}, IsError: true,
		}
	}
	if jsonContent {
		structured, err := decodeJSONValue(body)
		if err != nil {
			return errorResult("upstream API returned invalid JSON")
		}
		redactJSON(structured, redaction)
		compact, _ := json.Marshal(structured)
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(compact)}}, StructuredContent: structured}
	}
	if textContent {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: redactText(string(body), redaction)}}}
	}
	return errorResult("upstream API returned an unsupported content type")
}

func errorResult(message string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: message}}, IsError: true}
}

type responseRedaction struct {
	keys    map[string]struct{}
	secrets []string
}

func newResponseRedaction(group GroupConfig) responseRedaction {
	policy := responseRedaction{keys: make(map[string]struct{}, len(group.Headers)), secrets: make([]string, 0, len(group.Headers)+1)}
	for name, value := range group.Headers {
		policy.keys[normalizeSensitiveKey(name)] = struct{}{}
		if value != "" {
			policy.secrets = append(policy.secrets, value)
		}
	}
	if group.OAuth != nil && group.OAuth.ClientSecret != "" {
		policy.secrets = append(policy.secrets, group.OAuth.ClientSecret)
	}
	return policy
}

func redactResponse(body []byte, jsonContent bool, policy responseRedaction) string {
	if jsonContent {
		if value, err := decodeJSONValue(body); err == nil {
			redactJSON(value, policy)
			encoded, _ := json.Marshal(value)
			return string(encoded)
		}
	}
	return redactText(string(body), policy)
}

func decodeJSONValue(body []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("multiple JSON values")
	}
	return value, nil
}

func redactText(text string, policy responseRedaction) string {
	for _, marker := range []string{"Bearer ", "Basic "} {
		offset := 0
		for {
			relative := strings.Index(strings.ToLower(text[offset:]), strings.ToLower(marker))
			if relative < 0 {
				break
			}
			start := offset + relative
			end := start + len(marker)
			for end < len(text) && !strings.ContainsRune(" \t\r\n,;\"'", rune(text[end])) {
				end++
			}
			text = text[:start] + marker + "[redacted]" + text[end:]
			offset = start + len(marker) + len("[redacted]")
		}
	}
	for _, secret := range policy.secrets {
		if text == secret {
			return "[redacted]"
		}
		if len(secret) >= 4 {
			text = strings.ReplaceAll(text, secret, "[redacted]")
		}
	}
	return text
}

func redactJSON(value any, policy responseRedaction) {
	switch typed := value.(type) {
	case map[string]any:
		for key, item := range typed {
			identity := normalizeSensitiveKey(key)
			_, configuredHeader := policy.keys[identity]
			if configuredHeader || strings.Contains(identity, "token") || strings.Contains(identity, "secret") || strings.Contains(identity, "password") || strings.Contains(identity, "credential") || identity == "authorization" || identity == "apikey" || identity == "cookie" || identity == "setcookie" {
				typed[key] = "[redacted]"
				continue
			}
			if text, ok := item.(string); ok {
				typed[key] = redactText(text, policy)
				continue
			}
			redactJSON(item, policy)
		}
	case []any:
		for index, item := range typed {
			if text, ok := item.(string); ok {
				typed[index] = redactText(text, policy)
				continue
			}
			redactJSON(item, policy)
		}
	}
}

func normalizeSensitiveKey(value string) string {
	var normalized strings.Builder
	normalized.Grow(len(value))
	for _, character := range strings.ToLower(value) {
		if character >= 'a' && character <= 'z' || character >= '0' && character <= '9' {
			normalized.WriteRune(character)
		}
	}
	return normalized.String()
}

func hasScopes(granted map[string]struct{}, required []string) bool {
	for _, scope := range required {
		if _, ok := granted[scope]; !ok {
			return false
		}
	}
	return true
}

func cloneGroup(group GroupConfig) GroupConfig {
	result := group
	result.RequiredScopes = slices.Clone(group.RequiredScopes)
	result.ToolRules = slices.Clone(group.ToolRules)
	result.Headers = make(map[string]string, len(group.Headers))
	for name, value := range group.Headers {
		result.Headers[name] = value
	}
	if group.OAuth != nil {
		value := *group.OAuth
		value.Scopes = slices.Clone(group.OAuth.Scopes)
		result.OAuth = &value
	}
	result.Tools = make([]ToolConfig, len(group.Tools))
	for index, tool := range group.Tools {
		result.Tools[index] = cloneTool(tool)
	}
	return result
}

func cloneTool(tool ToolConfig) ToolConfig {
	result := tool
	result.Parameters = make([]Parameter, len(tool.Parameters))
	for index, parameter := range tool.Parameters {
		result.Parameters[index] = parameter
		result.Parameters[index].Schema = cloneMap(parameter.Schema)
	}
	result.BodySchema = cloneMap(tool.BodySchema)
	result.OutputSchema = cloneMap(tool.OutputSchema)
	return result
}

func cloneMap(value map[string]any) map[string]any {
	if value == nil {
		return nil
	}
	encoded, _ := json.Marshal(value)
	decoded, _ := decodeJSONValue(encoded)
	result, _ := decoded.(map[string]any)
	return result
}
