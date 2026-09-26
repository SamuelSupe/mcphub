package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"io"

	"github.com/SamuelSupe/mcphub/v2/internal/config"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func (v *view) statusApproval(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var input struct {
		ID            string `json:"approval_id"`
		QueryUpstream bool   `json:"query_upstream"`
	}
	decoder := json.NewDecoder(bytes.NewReader(req.Params.Arguments))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || decoder.Decode(new(any)) != io.EOF || input.ID == "" || len(input.ID) > 128 {
		return toolFailure("Provide approval_id and optional query_upstream."), nil
	}
	issuer, subject := v.approvalIdentity(req)
	a, err := v.hub.approvalStore.GetApproval(ctx, input.ID)
	if err != nil || subject == "" || a.Intent.Subject != subject || a.Intent.Issuer != issuer || a.Intent.Kind == "configuration" || !v.ownsApproval(a) {
		return toolFailure("Approval not found for this caller."), nil
	}
	value := map[string]any{"approval_id": a.ID, "status": a.Status, "operation_id": a.Intent.OperationID, "required_approvals": config.ApprovalQuorum(a.Intent.Rules), "approved_by": a.ApprovedBy, "expires_at": a.ExpiresAt}
	def, ok := v.hub.backendToolDefinitions(a.Intent.BackendID, false)[a.Intent.Tool]
	if !ok {
		def, ok = v.hub.httpToolDefinitions(a.Intent.BackendID)[a.Intent.Tool]
	}
	var params mcp.CallToolParamsRaw
	if json.Unmarshal(a.Intent.Params, &params) != nil {
		return toolFailure("Stored request is invalid."), nil
	}
	forwarded := &mcp.CallToolRequest{Session: req.Session, Extra: req.Extra, Params: &params}
	_, resourceErr := config.CheckToolResources(def.resourceRules, params.Arguments)
	allowed := ok && !v.hub.approvalsRetired.Load() && v.canCallTool(forwarded, def) && resourceErr == nil && a.Intent.ConfigurationHash != "" && a.Intent.ConfigurationHash == def.configurationHash
	if allowed && len(a.Result) > 0 {
		value["result"] = a.Result
	}
	if input.QueryUpstream {
		if !allowed {
			return toolFailure("The current tool or policy no longer permits querying this operation."), nil
		}
		var name string
		for _, rule := range def.approvalRules {
			if rule.StatusTool == "" {
				continue
			}
			if name != "" && name != rule.StatusTool {
				return toolFailure("Conflicting operation status tools."), nil
			}
			name = rule.StatusTool
		}
		if name == "" {
			return toolFailure("No read-only operation status tool is configured."), nil
		}
		if v.hub.approvalLimits != nil {
			release, denied := v.hub.approvalLimits.Acquire([]string{def.backendID})
			if denied != nil {
				return toolFailure("Endpoint rate limit exceeded."), nil
			}
			defer release()
		}
		result, _, err := v.approvalRead(ctx, forwarded, def, name, params.Arguments)
		if err != nil {
			return toolFailure(err.Error()), nil
		}
		value["upstream_observation"] = result
	}
	text, err := json.Marshal(value)
	if err != nil {
		return toolFailure("Cannot encode approval status."), nil
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(text)}}, StructuredContent: value}, nil
}
