package hub

import (
	"fmt"
	"slices"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/SamuelSupe/mcphub/v2/internal/backend"
	"github.com/SamuelSupe/mcphub/v2/internal/config"
	"github.com/SamuelSupe/mcphub/v2/internal/httptool"
)

type toolDefinition struct {
	grantPolicy       string
	configurationHash string
	approvalRules     []config.ToolApprovalPolicy
	effect            string
	target            string
	backendID         string
	original          string
	httpTool          bool
	tool              *mcp.Tool
	fingerprint       string
	requiredScopes    []string
	resourceRules     []config.ResourceRule
	httpManager       *httptool.Manager
}

func (h *Hub) httpToolDefinitions(groupID string) map[string]toolDefinition {
	manager, generation := h.currentHTTPToolsSnapshot()
	values := manager.Definitions(groupID)
	definitions := make(map[string]toolDefinition, len(values))
	for exposed, value := range values {
		copyTool := *value.Tool
		setToolEffect(&copyTool, value.Effect)
		requiredScopes := slices.Clone(value.RequiredScopes)
		fingerprint, err := fingerprint(struct {
			Tool           *mcp.Tool `json:"tool"`
			RequiredScopes []string  `json:"required_scopes"`
			Generation     uint64    `json:"generation"`
		}{Tool: &copyTool, RequiredScopes: requiredScopes, Generation: generation})
		if err != nil {
			continue
		}
		definitions[exposed] = toolDefinition{
			grantPolicy:       value.GrantPolicy,
			configurationHash: value.ConfigurationHash,
			approvalRules:     value.ApprovalRules,
			effect:            value.Effect, target: value.Target,
			backendID: groupID, original: value.Original, httpTool: true,
			tool: &copyTool, fingerprint: fingerprint, requiredScopes: requiredScopes, httpManager: manager, resourceRules: value.ResourceRules,
		}
	}
	return definitions
}

type toolDefinitionCache struct {
	catalog     *backend.Catalog
	definitions map[string]toolDefinition
	warnings    []toolDefinitionWarning
	warned      bool
}

type toolDefinitionWarning struct {
	message    string
	attributes []any
}

func (h *Hub) backendToolDefinitions(backendID string, warn bool) map[string]toolDefinition {
	client, ok := h.manager.Client(backendID)
	if !ok {
		return nil
	}
	return h.toolDefinitions(backendID, client.Config(), client.Catalog(), warn)
}

func (h *Hub) toolDefinitions(backendID string, backendConfig config.BackendConfig, catalog *backend.Catalog, warn bool) map[string]toolDefinition {
	if catalog == nil {
		return nil
	}
	h.toolDefinitionsMu.Lock()
	cached := h.toolDefinitionCaches[backendID]
	if cached == nil || cached.catalog != catalog {
		definitions, warnings := buildToolDefinitions(backendID, backendConfig, catalog)
		cached = &toolDefinitionCache{
			catalog:     catalog,
			definitions: definitions,
			warnings:    warnings,
		}
		h.toolDefinitionCaches[backendID] = cached
	}
	var warnings []toolDefinitionWarning
	if warn && !cached.warned {
		cached.warned = true
		warnings = cached.warnings
	}
	definitions := cached.definitions
	h.toolDefinitionsMu.Unlock()

	for _, warning := range warnings {
		h.logger.Warn(warning.message, warning.attributes...)
	}
	return definitions
}

func buildToolDefinitions(backendID string, backendConfig config.BackendConfig, catalog *backend.Catalog) (map[string]toolDefinition, []toolDefinitionWarning) {
	definitions := make(map[string]toolDefinition)
	var warnings []toolDefinitionWarning
	for _, tool := range catalog.Tools {
		if tool == nil || !slices.Contains(backendConfig.PublishedTools, tool.Name) {
			continue
		}
		exposed, err := exposeName(backendID, tool.Name)
		if err != nil {
			warnings = append(warnings, toolDefinitionWarning{
				message:    "omit invalid backend tool",
				attributes: []any{"backend", backendID, "capability_id", shortHash(tool.Name), "error_type", fmt.Sprintf("%T", err)},
			})
			continue
		}
		copyTool := *tool
		copyTool.Name = exposed
		effect := config.ToolEffect(backendConfig.ToolRules, tool.Name)
		setToolEffect(&copyTool, effect)
		toolFingerprint, err := fingerprint(&copyTool)
		if err != nil {
			warnings = append(warnings, toolDefinitionWarning{
				message:    "omit backend tool with invalid metadata",
				attributes: []any{"backend", backendID, "name", tool.Name, "error_type", fmt.Sprintf("%T", err)},
			})
			continue
		}
		if _, collision := definitions[exposed]; collision {
			warnings = append(warnings, toolDefinitionWarning{
				message:    "omit colliding backend tool",
				attributes: []any{"backend", backendID, "name", tool.Name, "exposed_name", exposed},
			})
			continue
		}
		configurationHash, err := fingerprint([]any{copyTool, backendConfig})
		if err != nil {
			continue
		}
		definitions[exposed] = toolDefinition{
			configurationHash: configurationHash,
			approvalRules:     config.ToolApprovalPolicies(backendConfig.ToolRules, tool.Name),
			effect:            effect, target: backendConfig.URL,
			backendID:      backendID,
			original:       tool.Name,
			tool:           &copyTool,
			fingerprint:    toolFingerprint,
			requiredScopes: backendConfig.RequiredToolScopes(tool.Name),
			resourceRules:  config.ToolResourceRules(backendConfig.ToolRules, tool.Name),
		}
	}
	return definitions, warnings
}

func setToolEffect(tool *mcp.Tool, effect string) {
	annotations := &mcp.ToolAnnotations{}
	if tool.Annotations != nil {
		*annotations = *tool.Annotations
	}
	annotations.ReadOnlyHint = effect == "read"
	if effect != "read" {
		annotations.IdempotentHint = false
		annotations.DestructiveHint = nil
		tool.Description += "\nMCPHub requires one-time human approval before execution. Query mcphub_approval_status to check progress. After approval, call mcphub_resume_approval with the approval_id."
	}
	tool.Annotations = annotations
}

func (h *Hub) MissingToolScopes(exposedName string, scopes []string) ([]string, bool) {
	backendID := backendFromName(exposedName)
	if backendID == "" {
		return nil, false
	}
	definition, known := h.backendToolDefinitions(backendID, false)[exposedName]
	required := definition.requiredScopes
	if !known {
		d, ok := h.currentHTTPTools().Definitions(backendID)[exposedName]
		required, known = d.RequiredScopes, ok
	}
	if !known {
		return nil, false
	}
	granted := make(map[string]struct{}, len(scopes))
	for _, scope := range scopes {
		granted[scope] = struct{}{}
	}
	missing := make([]string, 0, len(required))
	for _, scope := range required {
		if _, ok := granted[scope]; !ok {
			missing = append(missing, scope)
		}
	}
	slices.Sort(missing)
	return missing, true
}

// ToolTarget resolves only known published names, including hashed long names.
func (h *Hub) ToolTarget(name string) (string, string, bool) {
	id := backendFromName(name)
	if d, ok := h.backendToolDefinitions(id, false)[name]; ok {
		return d.backendID, d.original, true
	}
	d, ok := h.currentHTTPTools().Definitions(id)[name]
	return d.GroupID, d.Original, ok
}

func hasRequiredScopes(granted map[string]struct{}, required []string) bool {
	for _, scope := range required {
		if _, ok := granted[scope]; !ok {
			return false
		}
	}
	return true
}
