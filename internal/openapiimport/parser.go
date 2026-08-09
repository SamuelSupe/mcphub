package openapiimport

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/pb33f/libopenapi"
	"github.com/pb33f/libopenapi/datamodel"
	"gopkg.in/yaml.v3"

	"github.com/SamuelSupe/mcphub/internal/httptool"
)

const MaximumDocumentBytes = 5 << 20

var operationNamePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,95}$`)

type Preview struct {
	OpenAPIVersion string      `json:"openapi_version"`
	Title          string      `json:"title,omitempty"`
	Version        string      `json:"version,omitempty"`
	Servers        []string    `json:"servers"`
	SHA256         string      `json:"sha256"`
	Document       string      `json:"document"`
	Operations     []Operation `json:"operations"`
}

type Operation struct {
	Key               string              `json:"key"`
	Method            string              `json:"method"`
	Path              string              `json:"path"`
	OperationID       string              `json:"operation_id,omitempty"`
	Summary           string              `json:"summary,omitempty"`
	SuggestedToolName string              `json:"suggested_tool_name"`
	Eligible          bool                `json:"eligible"`
	Reason            string              `json:"reason,omitempty"`
	Tool              httptool.ToolConfig `json:"-"`
}

type ImportConfig struct {
	ID              string        `json:"id"`
	OriginType      string        `json:"origin_type"`
	SpecURL         string        `json:"spec_url,omitempty"`
	Filename        string        `json:"filename,omitempty"`
	RefreshInterval time.Duration `json:"refresh_interval"`
	OpenAPIVersion  string        `json:"openapi_version"`
	Title           string        `json:"title,omitempty"`
	Version         string        `json:"version,omitempty"`
	Selected        []Selection   `json:"selected"`
}

type Selection struct {
	OperationKey string `json:"operation_key"`
	ToolName     string `json:"tool_name"`
	Enabled      bool   `json:"enabled"`
}

func Parse(document []byte) (Preview, error) {
	if len(document) == 0 || len(document) > MaximumDocumentBytes {
		return Preview{}, fmt.Errorf("OpenAPI document must contain 1 to %d bytes", MaximumDocumentBytes)
	}
	configuration := &datamodel.DocumentConfiguration{AllowFileReferences: false, AllowRemoteReferences: false}
	parsed, err := libopenapi.NewDocumentWithConfiguration(document, configuration)
	if err != nil {
		return Preview{}, fmt.Errorf("parse OpenAPI document: %w", err)
	}
	if _, buildErr := parsed.BuildV3Model(); buildErr != nil {
		return Preview{}, fmt.Errorf("validate OpenAPI document: %w", buildErr)
	}
	var root map[string]any
	if err := yaml.Unmarshal(document, &root); err != nil {
		return Preview{}, fmt.Errorf("decode OpenAPI document: %w", err)
	}
	version, _ := root["openapi"].(string)
	if !strings.HasPrefix(version, "3.0.") && !strings.HasPrefix(version, "3.1.") {
		return Preview{}, fmt.Errorf("only OpenAPI 3.0 and 3.1 documents are supported")
	}
	if ref := firstExternalRef(root); ref != "" {
		return Preview{}, fmt.Errorf("external reference %q is not supported", ref)
	}
	if feature := unsupportedDocumentFeature(root); feature != "" {
		return Preview{}, fmt.Errorf("OpenAPI %s are not supported", feature)
	}
	sum := sha256.Sum256(document)
	preview := Preview{
		OpenAPIVersion: version, SHA256: hex.EncodeToString(sum[:]), Document: string(document),
		Servers: make([]string, 0), Operations: make([]Operation, 0),
	}
	if info, ok := root["info"].(map[string]any); ok {
		preview.Title, _ = info["title"].(string)
		preview.Version, _ = info["version"].(string)
	}
	if servers, ok := root["servers"].([]any); ok {
		for _, item := range servers {
			if server, ok := item.(map[string]any); ok {
				if value, ok := server["url"].(string); ok && value != "" {
					preview.Servers = append(preview.Servers, value)
				}
			}
		}
	}
	paths, _ := root["paths"].(map[string]any)
	pathNames := make([]string, 0, len(paths))
	for path := range paths {
		pathNames = append(pathNames, path)
	}
	slices.Sort(pathNames)
	for _, path := range pathNames {
		pathItem, ok := paths[path].(map[string]any)
		if !ok {
			continue
		}
		for _, method := range []string{"get", "post", "put", "patch", "delete", "head"} {
			operationMap, ok := pathItem[method].(map[string]any)
			if !ok {
				continue
			}
			operation := buildOperation(root, strings.ToUpper(method), path, pathItem, operationMap)
			preview.Operations = append(preview.Operations, operation)
		}
	}
	return preview, nil
}

func buildOperation(root map[string]any, method, path string, pathItem, operation map[string]any) Operation {
	operationID, _ := operation["operationId"].(string)
	name := suggestedName(operationID, method, path)
	result := Operation{
		Key: method + " " + path, Method: method, Path: path, OperationID: operationID,
		SuggestedToolName: name, Eligible: true,
	}
	result.Summary, _ = operation["summary"].(string)
	description, _ := operation["description"].(string)
	if description == "" {
		description = result.Summary
	}
	parameters := append(parameterList(pathItem["parameters"]), parameterList(operation["parameters"])...)
	type rawParameter struct {
		name, in, description string
		required              bool
		schema                map[string]any
	}
	var raw []rawParameter
	parameterIndexes := make(map[string]int)
	identities := make(map[string]int)
	for _, item := range parameters {
		resolved, err := resolveMap(root, item, map[string]bool{}, 0)
		if err != nil {
			return ineligible(result, err.Error())
		}
		name, _ := resolved["name"].(string)
		location, _ := resolved["in"].(string)
		if name == "" || (location != "path" && location != "query" && location != "header") {
			return ineligible(result, "only path, query, and header parameters are supported")
		}
		style, _ := resolved["style"].(string)
		if style != "" && ((location == "query" && style != "form") || (location != "query" && style != "simple")) {
			return ineligible(result, "parameter serialization style is unsupported")
		}
		if location == "header" && forbiddenParameterHeader(name) {
			return ineligible(result, "operation exposes a managed or credential header")
		}
		schema, ok := resolved["schema"].(map[string]any)
		if !ok {
			return ineligible(result, "parameter schema is required")
		}
		schema, err = resolveMap(root, schema, map[string]bool{}, 0)
		if err != nil {
			return ineligible(result, err.Error())
		}
		normalizeSchema(schema)
		if !supportedParameterSchema(schema) {
			return ineligible(result, "parameter schemas must use string, number, integer, boolean, or arrays of those values")
		}
		if allowReserved, _ := resolved["allowReserved"].(bool); allowReserved {
			return ineligible(result, "allowReserved query serialization is unsupported")
		}
		if explode, configured := resolved["explode"].(bool); location == "query" && configured && !explode && schema["type"] == "array" {
			return ineligible(result, "query arrays with explode=false are unsupported")
		}
		required, _ := resolved["required"].(bool)
		if location == "path" {
			required = true
		}
		parameterDescription, _ := resolved["description"].(string)
		identity := location + "\x00" + name
		if location == "header" {
			identity = location + "\x00" + strings.ToLower(name)
		}
		value := rawParameter{name: name, in: location, description: parameterDescription, required: required, schema: schema}
		if index, exists := parameterIndexes[identity]; exists {
			raw[index] = value
			continue
		}
		parameterIndexes[identity] = len(raw)
		raw = append(raw, value)
	}
	for _, value := range raw {
		identities[strings.ToLower(value.name)]++
	}
	tool := httptool.ToolConfig{Name: name, Description: description, Enabled: true, Method: method, Path: path, Origin: "openapi"}
	for _, value := range raw {
		argument := value.name
		if argument == "body" || identities[strings.ToLower(value.name)] > 1 {
			argument = value.in + "_" + value.name
		}
		tool.Parameters = append(tool.Parameters, httptool.Parameter{
			Name: value.name, Argument: argument, In: value.in, Required: value.required,
			Description: value.description, Schema: value.schema,
		})
	}
	if requestBody, ok := operation["requestBody"].(map[string]any); ok {
		resolved, err := resolveMap(root, requestBody, map[string]bool{}, 0)
		if err != nil {
			return ineligible(result, err.Error())
		}
		content, _ := resolved["content"].(map[string]any)
		if content["multipart/form-data"] != nil || content["application/x-www-form-urlencoded"] != nil {
			return ineligible(result, "multipart and form request bodies are unsupported")
		}
		jsonMedia, ok := content["application/json"].(map[string]any)
		if !ok {
			return ineligible(result, "request body must support application/json")
		}
		schema, ok := jsonMedia["schema"].(map[string]any)
		if !ok {
			return ineligible(result, "JSON request body schema is required")
		}
		tool.BodySchema, err = resolveMap(root, schema, map[string]bool{}, 0)
		if err != nil {
			return ineligible(result, err.Error())
		}
		normalizeSchema(tool.BodySchema)
		if schemaContainsBinary(tool.BodySchema) {
			return ineligible(result, "file and binary request bodies are unsupported")
		}
		tool.BodyRequired, _ = resolved["required"].(bool)
	}
	tool.OutputSchema = commonJSONOutputSchema(root, operation)
	if err := httptool.ValidateTool("preview", &tool); err != nil {
		return ineligible(result, err.Error())
	}
	result.Tool = tool
	return result
}

func commonJSONOutputSchema(root map[string]any, operation map[string]any) map[string]any {
	responses, _ := operation["responses"].(map[string]any)
	var selected map[string]any
	var fingerprint string
	for status, raw := range responses {
		if len(status) != 3 || status[0] != '2' {
			continue
		}
		response, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		response, err := resolveMap(root, response, map[string]bool{}, 0)
		if err != nil {
			return nil
		}
		content, _ := response["content"].(map[string]any)
		media, _ := content["application/json"].(map[string]any)
		schema, _ := media["schema"].(map[string]any)
		if schema == nil {
			continue
		}
		schema, err = resolveMap(root, schema, map[string]bool{}, 0)
		if err != nil {
			return nil
		}
		normalizeSchema(schema)
		encoded, _ := json.Marshal(schema)
		if selected == nil {
			selected, fingerprint = schema, string(encoded)
		} else if fingerprint != string(encoded) {
			return nil
		}
	}
	return selected
}

func parameterList(value any) []map[string]any {
	items, _ := value.([]any)
	result := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if parameter, ok := item.(map[string]any); ok {
			result = append(result, parameter)
		}
	}
	return result
}

func resolveMap(root map[string]any, value map[string]any, seen map[string]bool, depth int) (map[string]any, error) {
	if depth > 64 {
		return nil, fmt.Errorf("reference nesting is too deep")
	}
	if reference, ok := value["$ref"].(string); ok {
		if !strings.HasPrefix(reference, "#/") {
			return nil, fmt.Errorf("external reference %q is not supported", reference)
		}
		if seen[reference] {
			return nil, fmt.Errorf("circular input schema reference %q is unsupported", reference)
		}
		seen[reference] = true
		resolved, err := lookupPointer(root, reference)
		if err != nil {
			delete(seen, reference)
			return nil, err
		}
		result, err := resolveMap(root, resolved, seen, depth+1)
		delete(seen, reference)
		return result, err
	}
	encoded, _ := json.Marshal(value)
	var copyValue map[string]any
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	_ = decoder.Decode(&copyValue)
	for key, item := range copyValue {
		switch typed := item.(type) {
		case map[string]any:
			resolved, err := resolveMap(root, typed, seen, depth+1)
			if err != nil {
				return nil, err
			}
			copyValue[key] = resolved
		case []any:
			for index, arrayItem := range typed {
				if mapped, ok := arrayItem.(map[string]any); ok {
					resolved, err := resolveMap(root, mapped, seen, depth+1)
					if err != nil {
						return nil, err
					}
					typed[index] = resolved
				}
			}
		}
	}
	return copyValue, nil
}

func lookupPointer(root map[string]any, reference string) (map[string]any, error) {
	var current any = root
	for _, segment := range strings.Split(strings.TrimPrefix(reference, "#/"), "/") {
		segment = strings.ReplaceAll(strings.ReplaceAll(segment, "~1", "/"), "~0", "~")
		object, ok := current.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("reference %q does not resolve to an object", reference)
		}
		current, ok = object[segment]
		if !ok {
			return nil, fmt.Errorf("reference %q was not found", reference)
		}
	}
	result, ok := current.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("reference %q does not resolve to an object", reference)
	}
	return result, nil
}

func firstExternalRef(value any) string {
	switch typed := value.(type) {
	case map[string]any:
		for key, item := range typed {
			if key == "$ref" {
				if reference, ok := item.(string); ok && !strings.HasPrefix(reference, "#/") {
					return reference
				}
			}
			if reference := firstExternalRef(item); reference != "" {
				return reference
			}
		}
	case []any:
		for _, item := range typed {
			if reference := firstExternalRef(item); reference != "" {
				return reference
			}
		}
	}
	return ""
}

func unsupportedDocumentFeature(root map[string]any) string {
	if webhooks, exists := root["webhooks"]; exists && webhooks != nil {
		if value, ok := webhooks.(map[string]any); !ok || len(value) > 0 {
			return "webhooks"
		}
	}
	paths, _ := root["paths"].(map[string]any)
	for _, rawPath := range paths {
		pathItem, _ := rawPath.(map[string]any)
		for _, method := range []string{"get", "post", "put", "patch", "delete", "head"} {
			operation, _ := pathItem[method].(map[string]any)
			if operation == nil {
				continue
			}
			if callbacks, exists := operation["callbacks"]; exists && callbacks != nil {
				if value, ok := callbacks.(map[string]any); !ok || len(value) > 0 {
					return "callbacks"
				}
			}
		}
	}
	return ""
}

func supportedParameterSchema(schema map[string]any) bool {
	kind, _ := schema["type"].(string)
	switch kind {
	case "string", "number", "integer", "boolean":
		return true
	case "array":
		items, _ := schema["items"].(map[string]any)
		itemKind, _ := items["type"].(string)
		return itemKind == "string" || itemKind == "number" || itemKind == "integer" || itemKind == "boolean"
	default:
		return false
	}
}

func schemaContainsBinary(value any) bool {
	switch schema := value.(type) {
	case map[string]any:
		format, _ := schema["format"].(string)
		encoding, _ := schema["contentEncoding"].(string)
		if format == "binary" || format == "byte" || strings.EqualFold(encoding, "base64") {
			return true
		}
		for _, child := range schema {
			if schemaContainsBinary(child) {
				return true
			}
		}
	case []any:
		for _, child := range schema {
			if schemaContainsBinary(child) {
				return true
			}
		}
	}
	return false
}

func suggestedName(operationID, method, path string) string {
	if operationNamePattern.MatchString(operationID) {
		return operationID
	}
	value := strings.ToLower(method) + "_" + strings.Trim(path, "/")
	value = strings.NewReplacer("/", "_", "{", "", "}", "", "-", "_").Replace(value)
	value = regexp.MustCompile(`[^A-Za-z0-9_.-]+`).ReplaceAllString(value, "_")
	value = strings.Trim(value, "_.-")
	if len(value) > 95 {
		sum := sha256.Sum256([]byte(method + " " + path))
		value = value[:82] + "_" + hex.EncodeToString(sum[:6])
	}
	if value == "" {
		value = "operation"
	}
	return value
}

func forbiddenParameterHeader(name string) bool {
	switch strings.ToLower(name) {
	case "authorization", "proxy-authorization", "cookie", "host", "connection", "content-length", "content-type", "transfer-encoding", "upgrade", "te", "trailer":
		return true
	default:
		return false
	}
}

func ineligible(operation Operation, reason string) Operation {
	operation.Eligible = false
	operation.Reason = reason
	return operation
}

func normalizeSchema(value any) {
	switch schema := value.(type) {
	case map[string]any:
		if nullable, _ := schema["nullable"].(bool); nullable {
			if kind, ok := schema["type"].(string); ok {
				schema["type"] = []any{kind, "null"}
			}
		}
		delete(schema, "nullable")
		for _, key := range []string{"example", "xml", "externalDocs", "discriminator"} {
			delete(schema, key)
		}
		if exclusive, ok := schema["exclusiveMinimum"].(bool); ok {
			if exclusive {
				schema["exclusiveMinimum"] = schema["minimum"]
				delete(schema, "minimum")
			} else {
				delete(schema, "exclusiveMinimum")
			}
		}
		if exclusive, ok := schema["exclusiveMaximum"].(bool); ok {
			if exclusive {
				schema["exclusiveMaximum"] = schema["maximum"]
				delete(schema, "maximum")
			} else {
				delete(schema, "exclusiveMaximum")
			}
		}
		for _, item := range schema {
			normalizeSchema(item)
		}
	case []any:
		for _, item := range schema {
			normalizeSchema(item)
		}
	}
}
