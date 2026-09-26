package app

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"time"

	"github.com/SamuelSupe/mcphub/v2/internal/config"
	"github.com/SamuelSupe/mcphub/v2/internal/configstore"
	"github.com/SamuelSupe/mcphub/v2/internal/hub"
)

type accessCheckInput struct {
	Endpoint  string          `json:"endpoint"`
	Tool      string          `json:"tool"`
	Scopes    []string        `json:"scopes"`
	Subject   string          `json:"subject"`
	GrantID   string          `json:"grant_id"`
	Arguments json.RawMessage `json:"arguments"`
}

type accessCheckStep struct {
	Code    string   `json:"code"`
	Passed  bool     `json:"passed"`
	Missing []string `json:"missing_scopes,omitempty"`
}

type accessCheckResult struct {
	Simulated         bool              `json:"simulated"`
	Outcome           string            `json:"outcome"`
	EndpointRevision  int64             `json:"endpoint_revision"`
	EffectiveScopes   []string          `json:"effective_scopes"`
	Effect            string            `json:"effect"`
	RequiredApprovals int               `json:"required_approvals,omitempty"`
	RequireStepUp     bool              `json:"require_step_up"`
	Checks            []accessCheckStep `json:"checks"`
}

// Scope input is an administrator's assumption, never a credential. This route
// neither admits a request nor creates approvals, previews, or rate-limit leases.
func (a *App) serveAccessCheck(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		writeAPIError(w, 405, "method_not_allowed", "不支持此操作", "")
		return
	}
	var input accessCheckInput
	if !decodeAdminJSON(w, req, &input) {
		return
	}
	if input.Endpoint == "" || len(input.Endpoint) > 32 || input.Tool == "" || len(input.Tool) > 128 || len(input.Subject) > 1024 || len(input.GrantID) > 128 || (input.GrantID != "" && input.Subject == "") {
		writeAPIError(w, 400, "validation_failed", "请填写 endpoint、原始工具名；检查授权时还需填写对应用户 subject", "")
		return
	}
	if len(input.Arguments) == 0 {
		input.Arguments = json.RawMessage(`{}`)
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(input.Arguments, &object) != nil || object == nil {
		writeAPIError(w, 400, "validation_failed", "调用参数必须是 JSON 对象", "arguments")
		return
	}
	a.reloadMu.Lock()
	defer a.reloadMu.Unlock()
	rt := a.currentRuntime()
	endpoint, err := a.endpointToolPolicies(req.Context(), rt, input.Endpoint)
	if err != nil {
		writeToolStoreError(w, err, "Endpoint 不存在")
		return
	}
	result := accessCheckResult{Simulated: true, Outcome: "checks_passed", EndpointRevision: endpoint.Revision, EffectiveScopes: input.Scopes, Checks: []accessCheckStep{}}
	check := func(code string, passed bool, missing ...string) {
		result.Checks = append(result.Checks, accessCheckStep{Code: code, Passed: passed, Missing: missing})
		if !passed {
			result.Outcome = "denied"
		}
	}
	check("endpoint_enabled", endpoint.Enabled)
	check("endpoint_ready", endpoint.Ready)
	var tool *toolPolicyView
	for i := range endpoint.Tools {
		if endpoint.Tools[i].Name == input.Tool {
			tool = &endpoint.Tools[i]
			break
		}
	}
	check("tool_available", tool != nil && tool.Available)
	if tool == nil {
		writeJSON(w, 200, result)
		return
	}
	result.Effect = tool.Effect
	check("tool_published", tool.Published)
	if rt.cfg.Auth.SSO != nil {
		identity, identityErr := a.store.EffectiveIdentity(req.Context(), input.Subject)
		if identityErr == nil && (identity.Provider != rt.cfg.Auth.SSO.Upstream.Namespace() || (rt.cfg.Auth.SSO.DirectoryTokenEnv != "" && !identity.DirectoryManaged)) {
			identityErr = configstore.ErrIdentityDenied
		}
		check("user_active", identityErr == nil)
		if identityErr == nil {
			allowed := identity.Permissions.EffectiveScopes(rt.cfg.Admin)
			input.Scopes = slices.DeleteFunc(slices.Clone(input.Scopes), func(scope string) bool { return !slices.Contains(allowed, scope) })
			_, err := identity.Permissions.ToolArguments(endpoint.ID, input.Tool, tool.Effect, input.Arguments)
			check("user_tool_resource_allowed", err == nil)
		} else {
			input.Scopes = nil
		}
		result.EffectiveScopes = input.Scopes
	}
	if input.GrantID == "" {
		check("client_grant_required", !endpoint.RequireClientGrant)
	} else {
		result.EffectiveScopes = nil
		check("client_authorization_enabled", rt.cfg.ClientAuthorization.Enabled)
		g, err := a.store.GetClientGrant(req.Context(), input.GrantID, rt.cfg.Auth.Issuer, input.Subject)
		if err == nil {
			err = a.store.ValidateClientGrant(req.Context(), g)
		}
		if err != nil {
			var denied configstore.GrantError
			if !errors.As(err, &denied) {
				writeAPIError(w, 503, "storage_failed", "无法检查客户端授权", "")
				return
			}
			check(string(denied), false)
		} else {
			check("client_grant_active", time.Now().Before(g.ExpiresAt) && g.Resource == rt.cfg.Server.PublicURL)
			result.EffectiveScopes = g.EffectiveScopes(input.Scopes)
			check("client_tool_allowed", g.EndpointID == endpoint.ID && g.Capabilities.Tools && slices.Contains(g.AllowedTools, input.Tool) && (tool.Effect == "read" || g.AllowWriteRequests))
			_, resourceErr := config.CheckToolResources(g.ResourceRules, input.Arguments)
			check("client_resource_allowed", resourceErr == nil)
			name, _ := hub.ExposedToolName(endpoint.ID, input.Tool)
			params, _ := json.Marshal(map[string]any{"name": name, "arguments": input.Arguments})
			check("client_policy_current", rt.hub.CheckClientGrantRequest(g, "tools/call", params, input.Scopes) == nil)
		}
	}
	var missing []string
	for _, scope := range tool.RequiredScopes {
		if !slices.Contains(result.EffectiveScopes, scope) {
			missing = append(missing, scope)
		}
	}
	check("required_scopes", len(missing) == 0, missing...)
	arguments, err := config.CheckToolResources(tool.ResourceRules, input.Arguments)
	check("resource_allowed", err == nil)
	if tool.Effect != "read" {
		check("approval_service", rt.cfg.Admin.Remote())
		if err == nil {
			policies, policyErr := config.ResolveApprovalPolicies(tool.ApprovalPolicies, arguments)
			check("approval_arguments", policyErr == nil)
			result.RequiredApprovals = config.ApprovalQuorum(policies)
			result.RequireStepUp = config.ApprovalNeedsStepUp(policies)
		}
		if result.Outcome == "checks_passed" {
			result.Outcome = "requires_approval"
		}
	}
	writeJSON(w, 200, result)
}
