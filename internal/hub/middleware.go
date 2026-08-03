package hub

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"
	"time"
	"unicode"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func (v *view) catalogBarrierMiddleware(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		switch method {
		case "server/discover", "tools/list", "prompts/list", "resources/list", "resources/templates/list":
			v.reconcileMu.RLock()
			defer v.reconcileMu.RUnlock()
		}
		return next(ctx, method, req)
	}
}

func (v *view) timeoutMiddleware(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		if method == "subscriptions/listen" {
			return next(ctx, method, req)
		}
		callCtx, cancel := context.WithTimeout(ctx, v.hub.cfg.Server.RequestTimeout.Duration)
		defer cancel()
		return next(callCtx, method, req)
	}
}

func (v *view) privateCacheMiddleware(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		result, err := next(ctx, method, req)
		if err != nil || result == nil {
			return result, err
		}
		ttl := int(v.hub.cfg.Server.CatalogTTL.Duration / time.Millisecond)
		switch value := result.(type) {
		case *mcp.ListToolsResult:
			value.TTLMs, value.CacheScope = ttl, "private"
		case *mcp.ListPromptsResult:
			value.TTLMs, value.CacheScope = ttl, "private"
		case *mcp.ListResourcesResult:
			value.TTLMs, value.CacheScope = ttl, "private"
		case *mcp.ListResourceTemplatesResult:
			filtered := make([]*mcp.ResourceTemplate, 0, len(value.ResourceTemplates))
			for _, template := range value.ResourceTemplates {
				if template == nil {
					continue
				}
				if _, internal := v.internalTemplates[template.URITemplate]; !internal {
					filtered = append(filtered, template)
				}
			}
			value.ResourceTemplates = filtered
			value.TTLMs, value.CacheScope = ttl, "private"
		case *mcp.ReadResourceResult:
			value.CacheScope = "private"
		case *mcp.DiscoverResult:
			value.TTLMs, value.CacheScope = ttl, "private"
		}
		return result, nil
	}
}

func (v *view) loggingMiddleware(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		started := time.Now()
		result, err := next(ctx, method, req)
		attributes := []any{
			"mcp_method", methodForLog(method),
			"backend", backendForLog(backendForParams(req.GetParams())),
			"capability_name", capabilityForParams(req.GetParams()),
			"duration_ms", time.Since(started).Milliseconds(),
		}
		if extra := req.GetExtra(); extra != nil {
			if extra.TokenInfo != nil {
				attributes = append(attributes, "subject", boundedLogValue(extra.TokenInfo.UserID, 256))
			}
			if extra.Header != nil {
				attributes = append(attributes,
					"request_id", boundedLogValue(extra.Header.Get("X-Request-Id"), 128),
					"traceparent", boundedLogValue(extra.Header.Get("Traceparent"), 256),
				)
			}
		}
		if err != nil {
			attributes = append(attributes, "status", "error", "error_type", fmt.Sprintf("%T", err))
			v.hub.logger.Warn("MCP request", attributes...)
		} else {
			attributes = append(attributes, "status", "ok")
			v.hub.logger.Info("MCP request", attributes...)
		}
		return result, err
	}
}

func capabilityForParams(params mcp.Params) string {
	switch value := params.(type) {
	case *mcp.CallToolParamsRaw:
		return namedCapabilityForLog(value.Name)
	case *mcp.GetPromptParams:
		return namedCapabilityForLog(value.Name)
	case *mcp.ReadResourceParams:
		return resourceCapabilityForLog(value.URI)
	case *mcp.SubscribeParams:
		return resourceCapabilityForLog(value.URI)
	case *mcp.UnsubscribeParams:
		return resourceCapabilityForLog(value.URI)
	case *mcp.CompleteParams:
		if value.Ref != nil {
			if value.Ref.Type == "ref/prompt" {
				return namedCapabilityForLog(value.Ref.Name)
			}
			return resourceCapabilityForLog(value.Ref.URI)
		}
	}
	return ""
}

func namedCapabilityForLog(value string) string {
	if featureNamePattern.MatchString(value) {
		return value
	}
	return ""
}

func methodForLog(value string) string {
	if value == "" || len(value) > 128 {
		return ""
	}
	for _, char := range value {
		if (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') && (char < '0' || char > '9') && char != '/' && char != '_' && char != '-' && char != '.' {
			return ""
		}
	}
	return value
}

func backendForLog(value string) string {
	if value == "" || len(value) > 32 {
		return ""
	}
	for _, char := range value {
		if (char < 'a' || char > 'z') && (char < 'A' || char > 'Z') && (char < '0' || char > '9') && char != '_' && char != '-' {
			return ""
		}
	}
	return value
}

func boundedLogValue(value string, maximum int) string {
	if len(value) > maximum || strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return "sha256:" + shortHash(value)
	}
	return value
}

func resourceCapabilityForLog(raw string) string {
	normalized := raw
	if start := strings.Index(raw, "{?"); start >= 0 {
		if !strings.HasSuffix(raw, "}") {
			return ""
		}
		normalized = raw[:start]
	}
	u, err := url.Parse(normalized)
	if err != nil || u.Scheme != "mcphub" || u.Host == "" || u.User != nil {
		return ""
	}
	if templateID, ok := strings.CutPrefix(u.Path, "/t/"); ok {
		if len(templateID) != sha256.Size*2 || strings.Contains(templateID, "/") {
			return ""
		}
		if _, err := hex.DecodeString(templateID); err != nil {
			return ""
		}
		return "mcphub://" + u.Host + "/t/" + templateID
	}
	if _, ok := strings.CutPrefix(u.Path, "/r/"); ok {
		return "mcphub://" + u.Host + "/r/" + shortHash(raw)
	}
	return ""
}

func backendForParams(params mcp.Params) string {
	switch value := params.(type) {
	case *mcp.CallToolParamsRaw:
		return backendFromName(value.Name)
	case *mcp.GetPromptParams:
		return backendFromName(value.Name)
	case *mcp.ReadResourceParams:
		return backendFromURI(value.URI)
	case *mcp.SubscribeParams:
		return backendFromURI(value.URI)
	case *mcp.UnsubscribeParams:
		return backendFromURI(value.URI)
	case *mcp.CompleteParams:
		if value.Ref != nil {
			if value.Ref.Type == "ref/prompt" {
				return backendFromName(value.Ref.Name)
			}
			return backendFromURI(value.Ref.URI)
		}
	}
	return ""
}
