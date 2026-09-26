package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/SamuelSupe/mcphub/v2/internal/config"
	"github.com/SamuelSupe/mcphub/v2/internal/configstore"
	"github.com/SamuelSupe/mcphub/v2/internal/diagnostics"
)

const resumeApprovalTool = "mcphub_resume_approval"
const cancelApprovalTool = "mcphub_cancel_approval"
const statusApprovalTool = "mcphub_approval_status"

var operationIDPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

func toolFailure(message string) *mcp.CallToolResult {
	r := &mcp.CallToolResult{}
	r.SetError(fmt.Errorf("%s", message))
	return r
}

func (v *view) approvalIdentity(req *mcp.CallToolRequest) (issuer, subject string) {
	if req.Extra == nil || req.Extra.TokenInfo == nil {
		return "", ""
	}
	token := req.Extra.TokenInfo
	issuer, _ = token.Extra["issuer"].(string)
	if issuer == "" || issuer != v.hub.cfg.Auth.Issuer {
		return "", ""
	}
	return issuer, token.UserID
}

func (v *view) approvalPolicy(def toolDefinition) string {
	// A runtime replacement invalidates earlier approval intents, including
	// endpoint, credentials, scopes and policy changes not visible in tool schema.
	value, _ := fingerprint([]string{v.hub.approvalGeneration, def.fingerprint})
	return value
}

func (v *view) requestApproval(ctx context.Context, req *mcp.CallToolRequest, def toolDefinition, arguments json.RawMessage) *mcp.CallToolResult {
	if v.hub.approvalStore == nil || v.hub.approvalsRetired.Load() {
		diagnostics.Outcome(ctx, "policy_denied", "approval_service_unavailable")
		return toolFailure("Write and unclassified tools require remote administrator browser approval. Configure remote administration and a database, or have an administrator classify a verified read-only tool with effect: read.")
	}
	issuer, subject := v.approvalIdentity(req)
	if subject == "" {
		return toolFailure("Approval requires an authenticated issuer and subject.")
	}
	// Normalize duplicate keys before both review and execution, retaining number
	// precision. Never send the original, potentially ambiguous arguments later.
	if len(arguments) == 0 {
		arguments = json.RawMessage(`{}`)
	}
	decoder := json.NewDecoder(bytes.NewReader(arguments))
	decoder.UseNumber()
	var object map[string]any
	if err := decoder.Decode(&object); err != nil || object == nil {
		return toolFailure("Tool arguments must be a JSON object.")
	}
	if decoder.Decode(new(any)) != io.EOF {
		return toolFailure("Invalid tool arguments.")
	}
	arguments, err := json.Marshal(object)
	if err != nil {
		return toolFailure("Invalid tool arguments.")
	}
	params := *req.Params
	params.Arguments = arguments
	payload, err := json.Marshal(params)
	if err != nil {
		return toolFailure("Cannot encode approval request.")
	}
	if len(payload) > 60<<10 {
		return toolFailure("Approval requests must be at most 60 KiB for review.")
	}
	intent := configstore.ApprovalIntent{
		ConfigurationHash: def.configurationHash,
		Issuer:            issuer, Subject: subject, Tool: def.tool.Name, BackendID: def.backendID,
		Target: def.target, Effect: def.effect, Policy: v.approvalPolicy(def), Params: payload,
		Rules: def.approvalRules, PendingTTL: v.hub.cfg.Admin.Approvals.WaitDuration(), ExecutionTTL: v.hub.cfg.Admin.Approvals.ExecuteDuration(),
	}
	intent.EndpointUID, _, err = v.hub.approvalStore.ClientEndpointPolicy(ctx, def.backendID)
	if err != nil {
		return toolFailure("Current endpoint identity is unavailable; approval was not created.")
	}
	if v.grant != nil {
		binding := v.grant.GrantBinding
		intent.ClientGrant = &binding
	}
	intent.Rules, err = config.ResolveApprovalPolicies(def.approvalRules, arguments)
	if err != nil {
		return toolFailure(err.Error())
	}
	for _, policy := range intent.Rules {
		if policy.OperationIDArgument == "" {
			continue
		}
		value, _ := config.ArgumentValue(object, policy.OperationIDArgument)
		id, _ := value.(string)
		if !operationIDPattern.MatchString(id) || (intent.OperationID != "" && intent.OperationID != id) {
			return toolFailure("Provide one consistent business operation ID (1-128 ASCII letters, digits, '.', '_', ':', '-').")
		}
		intent.OperationID = id
	}
	intent.Summary, err = approvalSummary(def, object)
	if err != nil {
		return toolFailure(err.Error())
	}
	intent.Preview, intent.PreviewPolicy, err = v.approvalPreview(ctx, req, def, arguments)
	if err != nil {
		return toolFailure(err.Error())
	}
	a, err := v.hub.approvalStore.CreateApproval(ctx, intent)
	if err != nil {
		if err == configstore.ErrOperationConflict {
			return toolFailure(err.Error())
		}
		if err == configstore.ErrApprovalLimit {
			return toolFailure("Too many pending approvals; resolve existing requests or wait for expiry.")
		}
		return toolFailure("Approval storage unavailable; tool was not executed.")
	}
	return v.approvalResult(ctx, a)
}

func (v *view) approvalResult(ctx context.Context, a configstore.Approval) *mcp.CallToolResult {
	diagnostics.Update(ctx, func(r *diagnostics.Record) {
		r.ApprovalID = a.ID
		r.Reason = "approval_" + a.Status
		r.Outcome = "approval_pending"
		if a.Status != "pending" && a.Status != "approved" {
			r.Outcome = "policy_denied"
		}
	})
	url := v.hub.cfg.Admin.PublicURL + "/?approval=" + a.ID + "#approvals"
	text := fmt.Sprintf("MCPHub approval %s is %s. Review at %s. After approval, call %s with approval_id=%s; do not resubmit the original tool. Expiry: %s. No new execution was started by this response.", a.ID, a.Status, url, resumeApprovalTool, a.ID, a.ExpiresAt.Format(time.RFC3339))
	return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: text}}, StructuredContent: map[string]any{
		"code": "approval_" + a.Status, "approval_id": a.ID, "status": a.Status, "approval_url": url, "expires_at": a.ExpiresAt,
		"resume_tool": resumeApprovalTool,
		"cancel_tool": cancelApprovalTool,
		"status_tool": statusApprovalTool, "required_approvals": config.ApprovalQuorum(a.Intent.Rules), "approved_by": a.ApprovedBy, "reused": a.Reused,
	}}
}

func (v *view) resumeApproval(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var input struct {
		ID string `json:"approval_id"`
	}
	decoder := json.NewDecoder(bytes.NewReader(req.Params.Arguments))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || input.ID == "" || len(input.ID) > 128 {
		return toolFailure("Provide only approval_id."), nil
	}
	issuer, subject := v.approvalIdentity(req)
	a, err := v.hub.approvalStore.GetApproval(ctx, input.ID)
	if err != nil || subject == "" || a.Intent.Subject != subject || a.Intent.Issuer != issuer || a.Intent.Kind == "configuration" || !v.ownsApproval(a) {
		return toolFailure("Approval not found for this caller."), nil
	}
	def, ok := v.backendToolDefinitions(a.Intent.BackendID)[a.Intent.Tool]
	if !ok {
		def, ok = v.hub.httpToolDefinitions(a.Intent.BackendID)[a.Intent.Tool]
	}
	if !ok || v.hub.approvalsRetired.Load() || v.approvalPolicy(def) != a.Intent.Policy {
		_ = v.hub.approvalStore.ExpireApproval(ctx, a.ID)
		return toolFailure("Tool or policy changed; request new approval."), nil
	}
	var params mcp.CallToolParamsRaw
	decoder = json.NewDecoder(bytes.NewReader(a.Intent.Params))
	decoder.UseNumber()
	if decoder.Decode(&params) != nil {
		return toolFailure("Stored approval request is invalid."), nil
	}
	forwarded := &mcp.CallToolRequest{Session: req.Session, Extra: req.Extra, Params: &params}
	if !v.canCallTool(forwarded, def) {
		return toolFailure("Current permissions do not allow this tool."), nil
	}
	arguments, err := config.CheckToolResources(def.resourceRules, params.Arguments)
	if err != nil {
		return toolFailure("Current resource policy does not allow this operation."), nil
	}
	if len(a.Result) > 0 {
		var result mcp.CallToolResult
		decoder = json.NewDecoder(bytes.NewReader(a.Result))
		decoder.UseNumber()
		if decoder.Decode(&result) == nil {
			return &result, nil
		}
		return toolFailure("Saved result is unavailable; do not replay the write."), nil
	}
	if a.Status != "approved" {
		return v.approvalResult(ctx, a), nil
	}
	if v.hub.approvalLimits != nil {
		release, denied := v.hub.approvalLimits.Acquire([]string{def.backendID})
		if denied != nil {
			diagnostics.Outcome(ctx, "rate_limited", "endpoint_limit")
			return toolFailure("Endpoint rate limit exceeded; approval has not been consumed."), nil
		}
		defer release()
	}
	if a.Intent.PreviewPolicy != "" {
		preview, policy, err := v.approvalPreview(ctx, forwarded, def, arguments)
		if err != nil || policy != a.Intent.PreviewPolicy || !bytes.Equal(preview, a.Intent.Preview) {
			_ = v.hub.approvalStore.ExpireApproval(ctx, a.ID)
			return toolFailure("Preview or resource version changed/unavailable; approval was not executed. Request a new preview and approval."), nil
		}
	}
	ctx = configstore.WithActor(ctx, subject)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := v.hub.approvalStore.ClaimApproval(ctx, a.ID); err != nil {
		return toolFailure("Approval expired, was already consumed, or storage is unavailable. Check its status using the same approval_id; do not resubmit the write."), nil
	}
	diagnostics.Update(ctx, func(r *diagnostics.Record) {
		r.ApprovalID = a.ID
		r.ApprovalWaitMS = time.Since(a.CreatedAt).Milliseconds()
		r.Endpoint, r.Tool = def.backendID, def.original
	})
	// Only the progress token belongs to the resumed transport. All business
	// metadata, arguments and continuation input stay bound to the reviewed intent.
	delete(params.Meta, "progressToken")
	if progress, ok := req.Params.Meta["progressToken"]; ok {
		if params.Meta == nil {
			params.Meta = mcp.Meta{}
		}
		params.Meta["progressToken"] = progress
	}
	result, callErr := v.forwardTool(ctx, forwarded, def, arguments)
	status := "succeeded"
	if callErr != nil || result == nil || result.Meta["io.mcphub/executionOutcome"] == "unknown" {
		status = "unknown"
		result = toolFailure("Upstream execution outcome is unknown; inspect the target system before requesting any new write. This approval cannot execute again.")
	} else if result.IsError {
		status = "failed"
	}
	payload, marshalErr := json.Marshal(result)
	if marshalErr != nil || len(payload) > 16<<20 {
		status, payload = "unknown", nil
	}
	saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := v.hub.approvalStore.FinishApproval(saveCtx, a.ID, status, payload); err != nil {
		return toolFailure("Execution was attempted but its result could not be saved. Do not replay the write; an administrator must inspect the target system."), nil
	}
	return result, nil
}

func (v *view) cancelApproval(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var input struct {
		ID     string `json:"approval_id"`
		Reason string `json:"reason"`
	}
	decoder := json.NewDecoder(bytes.NewReader(req.Params.Arguments))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || input.ID == "" || len(input.ID) > 128 || len(input.Reason) > 2048 {
		return toolFailure("Provide approval_id and a cancellation reason (at most 2048 bytes)."), nil
	}
	issuer, subject := v.approvalIdentity(req)
	if input.Reason == "" {
		input.Reason = "Cancelled by requester"
	}
	a, err := v.hub.approvalStore.GetApproval(ctx, input.ID)
	if err != nil || !v.ownsApproval(a) {
		return toolFailure("Approval not found for this caller."), nil
	}
	if err := v.hub.approvalStore.CancelApproval(ctx, input.ID, issuer, subject, input.Reason); err != nil {
		return toolFailure("Cannot cancel this request: it is not yours, expired, or execution has already started. Cancellation cannot roll back an admitted write."), nil
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "Approval cancelled; it can no longer start execution."}}}, nil
}
