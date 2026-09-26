package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/SamuelSupe/mcphub/v2/internal/config"
	"github.com/SamuelSupe/mcphub/v2/internal/configstore"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func approvalSummary(def toolDefinition, args map[string]any) ([]configstore.ApprovalSummary, error) {
	policies := def.approvalRules
	if len(policies) == 0 {
		policies = []config.ToolApprovalPolicy{{}}
	}
	var summaries []configstore.ApprovalSummary
	for _, policy := range policies {
		summary := configstore.ApprovalSummary{Action: policy.Action, Environment: policy.Environment, Resources: map[string]json.RawMessage{}, VersionArgument: policy.VersionArgument}
		if summary.Action == "" {
			summary.Action = def.tool.Name
		}
		for _, pointer := range policy.ResourceArguments {
			value, ok := config.ArgumentValue(args, pointer)
			if !ok {
				return nil, fmt.Errorf("approval resource argument %s is missing", pointer)
			}
			summary.Resources[pointer], _ = json.Marshal(value)
		}
		for _, rule := range def.resourceRules {
			if value, ok := config.ArgumentValue(args, rule.Argument); ok {
				summary.Resources[rule.Argument], _ = json.Marshal(value)
			}
		}
		if policy.VersionArgument != "" {
			value, _ := config.ArgumentValue(args, policy.VersionArgument)
			summary.Version, _ = value.(string)
			if summary.Version == "" || summary.Version == "*" {
				return nil, fmt.Errorf("approval requires a specific resource version string at %s", policy.VersionArgument)
			}
		}
		summaries = append(summaries, summary)
	}
	return summaries, nil
}

func (v *view) approvalPreview(ctx context.Context, req *mcp.CallToolRequest, def toolDefinition, arguments json.RawMessage) (json.RawMessage, string, error) {
	var tool, versionArgument string
	for _, policy := range def.approvalRules {
		if policy.PreviewTool == "" {
			continue
		}
		if tool != "" && (tool != policy.PreviewTool || versionArgument != policy.VersionArgument) {
			return nil, "", fmt.Errorf("conflicting approval preview policies")
		}
		tool, versionArgument = policy.PreviewTool, policy.VersionArgument
	}
	if tool == "" {
		return nil, "", nil
	}
	result, preview, err := v.approvalRead(ctx, req, def, tool, arguments)
	if err != nil {
		return nil, "", err
	}
	data, err := json.Marshal(result.StructuredContent)
	if err != nil || len(data) > 32<<10 {
		return nil, "", fmt.Errorf("approval preview must contain at most 32 KiB of structured content")
	}
	var value struct {
		Version       string `json:"version"`
		Before, After json.RawMessage
	}
	if json.Unmarshal(data, &value) != nil || value.Version == "" || len(value.Before) == 0 || len(value.After) == 0 {
		return nil, "", fmt.Errorf("approval preview must return version, before and after")
	}
	var args map[string]any
	decoder := json.NewDecoder(bytes.NewReader(arguments))
	decoder.UseNumber()
	if err := decoder.Decode(&args); err != nil {
		return nil, "", err
	}
	expected, _ := config.ArgumentValue(args, versionArgument)
	if expected != value.Version {
		return nil, "", fmt.Errorf("resource version changed; obtain a fresh version before requesting approval")
	}
	// The upstream write must atomically enforce this same version. Rechecking a
	// read preview alone cannot close the interval before the write reaches it.
	return data, v.approvalPolicy(preview), nil
}

// The configured observation tool receives the same arguments, but cannot inherit
// continuation input that could turn an observation into a resumed operation.
func (v *view) approvalRead(ctx context.Context, req *mcp.CallToolRequest, def toolDefinition, tool string, arguments json.RawMessage) (*mcp.CallToolResult, toolDefinition, error) {
	name := def.backendID + "." + tool
	preview, ok := v.backendToolDefinitions(def.backendID)[name]
	if def.httpTool {
		preview, ok = v.hub.httpToolDefinitions(def.backendID)[name]
	}
	if !ok || preview.effect != "read" {
		return nil, toolDefinition{}, fmt.Errorf("approval read tool requires an explicitly published read-only tool")
	}
	params := *req.Params
	params.Name, params.Arguments, params.InputResponses, params.RequestState = name, arguments, nil, ""
	forwarded := &mcp.CallToolRequest{Session: req.Session, Extra: req.Extra, Params: &params}
	if !v.canCallTool(forwarded, preview) {
		return nil, toolDefinition{}, fmt.Errorf("current permissions do not allow the approval read tool")
	}
	checked, err := config.CheckToolResources(preview.resourceRules, arguments)
	if err != nil {
		return nil, toolDefinition{}, err
	}
	result, err := v.forwardTool(ctx, forwarded, preview, checked)
	if err != nil || result == nil || result.IsError || result.StructuredContent == nil {
		return nil, toolDefinition{}, fmt.Errorf("approval read tool failed; no write was attempted")
	}
	return result, preview, nil
}
