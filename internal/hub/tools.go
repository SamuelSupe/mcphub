package hub

import (
	"fmt"
	"slices"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/SamuelSupe/mcphub/internal/backend"
	"github.com/SamuelSupe/mcphub/internal/config"
)

type toolDefinition struct {
	backendID      string
	original       string
	tool           *mcp.Tool
	fingerprint    string
	requiredScopes []string
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
		if tool == nil {
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
		fingerprint, err := fingerprint(&copyTool)
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
		definitions[exposed] = toolDefinition{
			backendID:      backendID,
			original:       tool.Name,
			tool:           &copyTool,
			fingerprint:    fingerprint,
			requiredScopes: backendConfig.RequiredToolScopes(tool.Name),
		}
	}
	return definitions, warnings
}

func (h *Hub) MissingToolScopes(exposedName string, scopes []string) ([]string, bool) {
	backendID := backendFromName(exposedName)
	if backendID == "" {
		return nil, false
	}
	definition, known := h.backendToolDefinitions(backendID, false)[exposedName]
	if !known {
		return nil, false
	}
	granted := make(map[string]struct{}, len(scopes))
	for _, scope := range scopes {
		granted[scope] = struct{}{}
	}
	missing := make([]string, 0, len(definition.requiredScopes))
	for _, scope := range definition.requiredScopes {
		if _, ok := granted[scope]; !ok {
			missing = append(missing, scope)
		}
	}
	slices.Sort(missing)
	return missing, true
}

func hasRequiredScopes(granted map[string]struct{}, required []string) bool {
	for _, scope := range required {
		if _, ok := granted[scope]; !ok {
			return false
		}
	}
	return true
}
