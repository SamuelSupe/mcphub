package app

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"

	"github.com/SamuelSupe/mcphub/v2/internal/config"
	"github.com/SamuelSupe/mcphub/v2/internal/configstore"
)

type toolPolicyView struct {
	Name             string                      `json:"name"`
	Description      string                      `json:"description"`
	Published        bool                        `json:"published"`
	Available        bool                        `json:"available"`
	Effect           string                      `json:"effect"`
	RequiredScopes   []string                    `json:"required_scopes"`
	ResourceRules    []config.ResourceRule       `json:"resource_rules"`
	MatchingRules    []config.ToolRule           `json:"matching_rules"`
	ApprovalPolicies []config.ToolApprovalPolicy `json:"approval_policies"`
}

type endpointPolicies struct {
	ID                 string           `json:"id"`
	Kind               string           `json:"kind"`
	Revision           int64            `json:"revision"`
	Enabled            bool             `json:"enabled"`
	Ready              bool             `json:"ready"`
	RequireClientGrant bool             `json:"require_client_grant"`
	Tools              []toolPolicyView `json:"tools"`
}

// Read the saved policy and current catalog without probing or calling upstream.
// Configured tools absent from the catalog remain visible, but are unavailable.
func (a *App) endpointToolPolicies(ctx context.Context, rt *runtime, id string) (endpointPolicies, error) {
	result := endpointPolicies{Tools: []toolPolicyView{}}
	tools := make(map[string]toolPolicyView)
	var rules []config.ToolRule
	var scopes []string
	record, err := a.store.Get(ctx, id)
	if err == nil {
		cfg := record.Config
		result.ID, result.Kind, result.Revision = cfg.ID, "backend", record.Revision
		result.Enabled, result.RequireClientGrant = record.Enabled, cfg.RequireClientGrant
		rules, scopes = cfg.ToolRules, cfg.RequiredScopes
		for _, name := range cfg.PublishedTools {
			tools[name] = toolPolicyView{Name: name, Published: true}
		}
		if client, ok := rt.manager.Client(cfg.ID); ok {
			result.Ready = client.Ready()
			if catalog := client.Catalog(); catalog != nil {
				for _, tool := range catalog.Tools {
					if tool != nil {
						tools[tool.Name] = toolPolicyView{Name: tool.Name, Description: tool.Description, Available: true, Published: slices.Contains(cfg.PublishedTools, tool.Name)}
					}
				}
			}
		}
	} else {
		if !errors.Is(err, configstore.ErrNotFound) {
			return result, err
		}
		group, err := a.store.GetToolGroup(ctx, id)
		if err != nil {
			return result, err
		}
		cfg := group.Config
		result.ID, result.Kind, result.Revision = cfg.ID, "tool-group", group.Revision
		result.Enabled, result.Ready, result.RequireClientGrant = cfg.Enabled, cfg.Enabled, cfg.RequireClientGrant
		rules, scopes = cfg.ToolRules, cfg.RequiredScopes
		for _, tool := range cfg.Tools {
			tools[tool.Name] = toolPolicyView{Name: tool.Name, Description: tool.Description, Available: true, Published: tool.Enabled}
		}
	}
	result.RequireClientGrant = result.RequireClientGrant || rt.cfg.ClientAuthorization.RequireClientGrant
	for _, rule := range rules {
		if !strings.ContainsAny(rule.Match, "*?[\\") {
			if _, ok := tools[rule.Match]; !ok {
				tools[rule.Match] = toolPolicyView{Name: rule.Match}
			}
		}
	}
	for name, tool := range tools {
		tool.Effect = config.ToolEffect(rules, name)
		tool.RequiredScopes = append(slices.Clone(scopes), (config.BackendConfig{ToolRules: rules}).RequiredToolScopes(name)...)
		slices.Sort(tool.RequiredScopes)
		tool.RequiredScopes = slices.Compact(tool.RequiredScopes)
		tool.ResourceRules = config.ToolResourceRules(rules, name)
		tool.ApprovalPolicies = config.ToolApprovalPolicies(rules, name)
		for _, rule := range rules {
			if rule.Matches(name) {
				tool.MatchingRules = append(tool.MatchingRules, rule)
			}
		}
		result.Tools = append(result.Tools, tool)
	}
	slices.SortFunc(result.Tools, func(a, b toolPolicyView) int { return strings.Compare(a.Name, b.Name) })
	return result, nil
}

func (a *App) serveToolPolicies(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodGet {
		writeAPIError(w, 405, "method_not_allowed", "不支持此操作", "")
		return
	}
	id := req.URL.Query().Get("endpoint")
	if id == "" || len(id) > 32 {
		writeAPIError(w, 400, "validation_failed", "请选择 endpoint", "endpoint")
		return
	}
	a.reloadMu.Lock()
	defer a.reloadMu.Unlock()
	result, err := a.endpointToolPolicies(req.Context(), a.currentRuntime(), id)
	if err != nil {
		writeToolStoreError(w, err, "Endpoint 不存在")
		return
	}
	w.Header().Set("ETag", revisionETag(result.Revision))
	writeJSON(w, 200, result)
}
