package config

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"
)

type ApprovalSettings struct {
	PolicyChanges   PolicyChangeSettings `yaml:"policy_changes"`
	Notifications   ApprovalWebhook      `yaml:"notifications"`
	AuditArchive    ApprovalArchive      `yaml:"audit_archive"`
	RequiredScopes  []string             `yaml:"required_scopes"`
	PendingTTL      Duration             `yaml:"pending_ttl"`
	ExecutionTTL    Duration             `yaml:"execution_ttl"`
	Retention       Duration             `yaml:"retention"`
	StepUpACRValues []string             `yaml:"step_up_acr_values"`
}

func (s ApprovalSettings) Scopes() []string {
	if len(s.RequiredScopes) == 0 {
		return []string{"mcphub:approve"}
	}
	return s.RequiredScopes
}

func (s ApprovalSettings) WaitDuration() time.Duration {
	if s.PendingTTL.Duration == 0 {
		return 30 * time.Minute
	}
	return s.PendingTTL.Duration
}

func (s ApprovalSettings) ExecuteDuration() time.Duration {
	if s.ExecutionTTL.Duration == 0 {
		return 5 * time.Minute
	}
	return s.ExecutionTTL.Duration
}

func (s ApprovalSettings) RetainDuration() time.Duration {
	if s.Retention.Duration == 0 {
		return 30 * 24 * time.Hour
	}
	return s.Retention.Duration
}

func (s ApprovalSettings) Validate() error {
	if err := s.validateGovernance(); err != nil {
		return err
	}
	if err := validateScopes("admin.approvals.required_scopes", s.Scopes()); err != nil {
		return err
	}
	if s.WaitDuration() < time.Minute || s.WaitDuration() > 24*time.Hour {
		return fmt.Errorf("admin.approvals.pending_ttl must be between 1m and 24h")
	}
	if s.ExecuteDuration() < time.Minute || s.ExecuteDuration() > 30*time.Minute {
		return fmt.Errorf("admin.approvals.execution_ttl must be between 1m and 30m")
	}
	if s.RetainDuration() < 24*time.Hour || s.RetainDuration() > 365*24*time.Hour {
		return fmt.Errorf("admin.approvals.retention must be between 24h and 8760h")
	}
	return validateScopes("admin.approvals.step_up_acr_values", s.StepUpACRValues)
}

type ApprovalGrant struct {
	Subjects  []string       `yaml:"subjects" json:"subjects"`
	Resources []ResourceRule `yaml:"resources" json:"resources,omitempty"`
}

type ToolApprovalPolicy struct {
	RequiredApprovals        int                `yaml:"required_approvals" json:"required_approvals,omitempty"`
	RiskRules                []ApprovalRiskRule `yaml:"risk_rules" json:"risk_rules,omitempty"`
	OperationIDArgument      string             `yaml:"operation_id_argument" json:"operation_id_argument,omitempty"`
	StatusTool               string             `yaml:"status_tool" json:"status_tool,omitempty"`
	Approvers                []ApprovalGrant    `yaml:"approvers" json:"approvers,omitempty"`
	RequireDifferentReviewer bool               `yaml:"require_different_reviewer" json:"require_different_reviewer,omitempty"`
	RequireStepUp            bool               `yaml:"require_step_up" json:"require_step_up,omitempty"`
	Action                   string             `yaml:"action" json:"action,omitempty"`
	Environment              string             `yaml:"environment" json:"environment,omitempty"`
	ResourceArguments        []string           `yaml:"resource_arguments" json:"resource_arguments,omitempty"`
	PreviewTool              string             `yaml:"preview_tool" json:"preview_tool,omitempty"`
	VersionArgument          string             `yaml:"version_argument" json:"version_argument,omitempty"`
}

func (p ToolApprovalPolicy) Validate() error {
	if p.RequiredApprovals < 0 || p.RequiredApprovals > 2 {
		return fmt.Errorf("approval required_approvals must be 1 or 2")
	}
	for _, risk := range p.RiskRules {
		if len(risk.Resources) == 0 || risk.RequiredApprovals < 1 || risk.RequiredApprovals > 2 {
			return fmt.Errorf("approval risk_rules require resource conditions and 1 or 2 approvals")
		}
		if err := validateResourceRules(risk.Resources); err != nil {
			return err
		}
	}
	if p.StatusTool != "" && (!publishedToolName.MatchString(p.StatusTool) || p.OperationIDArgument == "") {
		return fmt.Errorf("approval status_tool requires an original tool name and operation_id_argument")
	}
	if len(p.Action) > 256 || len(p.Environment) > 128 {
		return fmt.Errorf("approval action/environment is too long")
	}
	for _, grant := range p.Approvers {
		if len(grant.Subjects) == 0 {
			return fmt.Errorf("approval approvers require explicit subjects")
		}
		for _, subject := range grant.Subjects {
			if strings.TrimSpace(subject) == "" || len(subject) > 256 {
				return fmt.Errorf("invalid approval subject")
			}
		}
		if err := validateResourceRules(grant.Resources); err != nil {
			return err
		}
	}
	pointers := slices.Clone(p.ResourceArguments)
	if p.OperationIDArgument != "" {
		pointers = append(pointers, p.OperationIDArgument)
	}
	if p.VersionArgument != "" {
		pointers = append(pointers, p.VersionArgument)
	}
	for _, pointer := range pointers {
		if err := validateResourceRules([]ResourceRule{{Argument: pointer, AllowedValues: []string{"validate"}}}); err != nil {
			return err
		}
	}
	if p.PreviewTool != "" && (!publishedToolName.MatchString(p.PreviewTool) || p.VersionArgument == "") {
		return fmt.Errorf("approval preview_tool requires an original tool name and version_argument")
	}
	return nil
}

type ApprovalRiskRule struct {
	Resources         []ResourceRule `yaml:"resources" json:"resources"`
	RequiredApprovals int            `yaml:"required_approvals" json:"required_approvals"`
	RequireStepUp     bool           `yaml:"require_step_up" json:"require_step_up,omitempty"`
}

// Resolve risk from the server's policies and the normalized execution arguments.
// A matching condition can only strengthen the base policy.
func ResolveApprovalPolicies(policies []ToolApprovalPolicy, arguments json.RawMessage) ([]ToolApprovalPolicy, error) {
	resolved := slices.Clone(policies)
	var args map[string]any
	if json.Unmarshal(arguments, &args) != nil {
		return nil, fmt.Errorf("invalid approval arguments")
	}
	for i := range resolved {
		for _, risk := range resolved[i].RiskRules {
			matches := true
			for _, condition := range risk.Resources {
				value, ok := ArgumentValue(args, condition.Argument)
				valid := ok
				match := false
				switch v := value.(type) {
				case string:
					valid = valid && v != ""
					match = slices.Contains(condition.AllowedValues, v)
				case []any:
					valid = valid && len(v) > 0
					for _, item := range v {
						text, ok := item.(string)
						valid = valid && ok && text != ""
						match = match || slices.Contains(condition.AllowedValues, text)
					}
				default:
					valid = false
				}
				if !valid {
					return nil, fmt.Errorf("approval risk argument %s must contain strings", condition.Argument)
				}
				matches = matches && match
			}
			if matches {
				resolved[i].RequiredApprovals = max(resolved[i].RequiredApprovals, risk.RequiredApprovals)
				resolved[i].RequireStepUp = resolved[i].RequireStepUp || risk.RequireStepUp
			}
		}
		if resolved[i].RequiredApprovals == 2 {
			resolved[i].RequireDifferentReviewer = true
		}
	}
	return resolved, nil
}

func ApprovalQuorum(policies []ToolApprovalPolicy) int {
	count := 1
	for _, policy := range policies {
		count = max(count, policy.RequiredApprovals)
	}
	return count
}

func ToolApprovalPolicies(rules []ToolRule, name string) []ToolApprovalPolicy {
	var policies []ToolApprovalPolicy
	for _, rule := range rules {
		if rule.Matches(name) && rule.Approval != nil {
			policies = append(policies, *rule.Approval)
		}
	}
	return policies
}

// Every matching policy must allow this reviewer; grants within a policy are
// alternatives. Subjects come from the same verified issuer as the requester.
func CanReviewApproval(policies []ToolApprovalPolicy, reviewer, requester string, arguments json.RawMessage, decision bool) bool {
	if reviewer == "" {
		return false
	}
	for _, policy := range policies {
		if decision && policy.RequireDifferentReviewer && reviewer == requester {
			return false
		}
		allowed := len(policy.Approvers) == 0
		for _, grant := range policy.Approvers {
			if !slices.Contains(grant.Subjects, reviewer) {
				continue
			}
			if _, err := CheckToolResources(grant.Resources, arguments); err == nil {
				allowed = true
				break
			}
		}
		if !allowed {
			return false
		}
	}
	return true
}

func ApprovalNeedsStepUp(policies []ToolApprovalPolicy) bool {
	return slices.ContainsFunc(policies, func(p ToolApprovalPolicy) bool { return p.RequireStepUp })
}

// ArgumentValue follows object-key JSON Pointers, matching resource policies.
func ArgumentValue(object any, pointer string) (any, bool) {
	for _, key := range strings.Split(strings.TrimPrefix(pointer, "/"), "/") {
		key = strings.ReplaceAll(strings.ReplaceAll(key, "~1", "/"), "~0", "~")
		values, ok := object.(map[string]any)
		if !ok {
			return nil, false
		}
		object, ok = values[key]
		if !ok {
			return nil, false
		}
	}
	return object, true
}
