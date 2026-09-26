package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"
)

type ResourceRule struct {
	Argument      string   `yaml:"argument" json:"argument"`
	AllowedValues []string `yaml:"allowed_values" json:"allowed_values"`
}

// Unclassified tools require approval. A write rule takes precedence over every
// read rule, so a broader rule cannot weaken an explicit write classification.
func ToolEffect(rules []ToolRule, name string) string {
	effect := "unknown"
	for _, rule := range rules {
		if !rule.Matches(name) {
			continue
		}
		if rule.Effect == "write" || rule.Approval != nil {
			return "write"
		}
		if rule.Effect == "read" {
			effect = "read"
		}
	}
	return effect
}

var publishedToolName = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,128}$`)

func validatePublishedTools(names []string) error {
	seen := make(map[string]bool, len(names))
	for _, name := range names {
		if !publishedToolName.MatchString(name) || seen[name] {
			return fmt.Errorf("published_tools must contain unique, exact tool names (no glob patterns)")
		}
		seen[name] = true
	}
	return nil
}

func validateResourceRules(rules []ResourceRule) error {
	for _, rule := range rules {
		if !strings.HasPrefix(rule.Argument, "/") {
			return fmt.Errorf("resource_rules argument must be a non-empty JSON pointer")
		}
		for i := 0; i < len(rule.Argument); i++ {
			if rule.Argument[i] == '~' {
				i++
				if i == len(rule.Argument) || (rule.Argument[i] != '0' && rule.Argument[i] != '1') {
					return fmt.Errorf("resource_rules argument contains an invalid JSON pointer escape")
				}
			}
		}
		if len(rule.AllowedValues) == 0 {
			return fmt.Errorf("resource_rules allowed_values must not be empty")
		}
		for _, value := range rule.AllowedValues {
			if value == "" {
				return fmt.Errorf("resource_rules allowed_values must contain non-empty strings")
			}
		}
	}
	return nil
}

func ToolResourceRules(rules []ToolRule, name string) []ResourceRule {
	var resources []ResourceRule
	for _, rule := range rules {
		if rule.Matches(name) {
			resources = append(resources, rule.ResourceRules...)
		}
	}
	return resources
}

// CheckToolResources requires every matching constraint. Object keys use JSON
// pointer escaping; a leaf may be a string or a non-empty array of strings.
// Return normalized JSON so the upstream cannot interpret duplicate keys
// differently from the values that passed authorization. Numbers retain precision.
func CheckToolResources(rules []ResourceRule, arguments json.RawMessage) (json.RawMessage, error) {
	if len(rules) == 0 {
		return arguments, nil
	}
	denied := fmt.Errorf("tool resource is not allowed")
	decoder := json.NewDecoder(bytes.NewReader(arguments))
	decoder.UseNumber()
	var args map[string]any
	if err := decoder.Decode(&args); err != nil || args == nil {
		return nil, denied
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, denied
	}
	for _, rule := range rules {
		var value any = args
		for _, key := range strings.Split(strings.TrimPrefix(rule.Argument, "/"), "/") {
			key = strings.ReplaceAll(strings.ReplaceAll(key, "~1", "/"), "~0", "~")
			object, ok := value.(map[string]any)
			if !ok {
				return nil, denied
			}
			value = object[key]
		}
		values, ok := value.([]any)
		if !ok {
			values = []any{value}
		}
		if len(values) == 0 {
			return nil, denied
		}
		for _, value := range values {
			resource, ok := value.(string)
			if !ok || !slices.Contains(rule.AllowedValues, resource) {
				return nil, denied
			}
		}
	}
	return json.Marshal(args)
}

func CloneToolRules(rules []ToolRule) []ToolRule {
	result := slices.Clone(rules)
	for i := range result {
		if rules[i].Approval != nil {
			// Policies contain nested grant slices and must not share mutable state
			// with the configuration editor or a previous runtime generation.
			data, _ := json.Marshal(rules[i].Approval)
			result[i].Approval = new(ToolApprovalPolicy)
			_ = json.Unmarshal(data, result[i].Approval)
		}
		result[i].RequiredScopes = slices.Clone(rules[i].RequiredScopes)
		result[i].ResourceRules = slices.Clone(rules[i].ResourceRules)
		for j := range result[i].ResourceRules {
			result[i].ResourceRules[j].AllowedValues = slices.Clone(rules[i].ResourceRules[j].AllowedValues)
		}
	}
	return result
}
