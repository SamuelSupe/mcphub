package backend

import (
	"context"
	"fmt"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"golang.org/x/sync/errgroup"
)

type Catalog struct {
	Tools             []*mcp.Tool
	Prompts           []*mcp.Prompt
	Resources         []*mcp.Resource
	ResourceTemplates []*mcp.ResourceTemplate
	Capabilities      *mcp.ServerCapabilities
	RefreshAfter      time.Duration
}

func discoverCatalog(ctx context.Context, session *mcp.ClientSession, maximumRefresh time.Duration) (*Catalog, error) {
	result := &Catalog{
		Capabilities: session.InitializeResult().Capabilities,
		RefreshAfter: maximumRefresh,
	}
	if result.Capabilities == nil {
		return nil, fmt.Errorf("backend returned no capabilities")
	}

	group, discoveryCtx := errgroup.WithContext(ctx)
	var toolsRefresh, promptsRefresh, resourcesRefresh, templatesRefresh time.Duration
	if result.Capabilities.Tools != nil {
		group.Go(func() error {
			var err error
			result.Tools, toolsRefresh, err = discoverTools(discoveryCtx, session, maximumRefresh)
			return err
		})
	}
	if result.Capabilities.Prompts != nil {
		group.Go(func() error {
			var err error
			result.Prompts, promptsRefresh, err = discoverPrompts(discoveryCtx, session, maximumRefresh)
			return err
		})
	}
	if result.Capabilities.Resources != nil {
		group.Go(func() error {
			var err error
			result.Resources, resourcesRefresh, err = discoverResources(discoveryCtx, session, maximumRefresh)
			return err
		})
		group.Go(func() error {
			var err error
			result.ResourceTemplates, templatesRefresh, err = discoverResourceTemplates(discoveryCtx, session, maximumRefresh)
			return err
		})
	}
	if err := group.Wait(); err != nil {
		return nil, err
	}
	for _, delay := range []time.Duration{toolsRefresh, promptsRefresh, resourcesRefresh, templatesRefresh} {
		if delay > 0 && delay < result.RefreshAfter {
			result.RefreshAfter = delay
		}
	}

	if result.RefreshAfter < 5*time.Second {
		result.RefreshAfter = 5 * time.Second
	}
	return result, nil
}

func discoverTools(ctx context.Context, session *mcp.ClientSession, maximumRefresh time.Duration) ([]*mcp.Tool, time.Duration, error) {
	var tools []*mcp.Tool
	refreshAfter := maximumRefresh
	seen := make(map[string]struct{})
	for cursor := ""; ; {
		page, err := session.ListTools(ctx, &mcp.ListToolsParams{Cursor: cursor})
		if err != nil {
			return nil, 0, fmt.Errorf("list tools: %w", err)
		}
		tools = append(tools, page.Tools...)
		refreshAfter = refreshDelay(refreshAfter, page.TTLMs)
		next, done, err := nextPageCursor("tools", page.NextCursor, seen)
		if err != nil {
			return nil, 0, err
		}
		if done {
			return tools, refreshAfter, nil
		}
		cursor = next
	}
}

func discoverPrompts(ctx context.Context, session *mcp.ClientSession, maximumRefresh time.Duration) ([]*mcp.Prompt, time.Duration, error) {
	var prompts []*mcp.Prompt
	refreshAfter := maximumRefresh
	seen := make(map[string]struct{})
	for cursor := ""; ; {
		page, err := session.ListPrompts(ctx, &mcp.ListPromptsParams{Cursor: cursor})
		if err != nil {
			return nil, 0, fmt.Errorf("list prompts: %w", err)
		}
		prompts = append(prompts, page.Prompts...)
		refreshAfter = refreshDelay(refreshAfter, page.TTLMs)
		next, done, err := nextPageCursor("prompts", page.NextCursor, seen)
		if err != nil {
			return nil, 0, err
		}
		if done {
			return prompts, refreshAfter, nil
		}
		cursor = next
	}
}

func discoverResources(ctx context.Context, session *mcp.ClientSession, maximumRefresh time.Duration) ([]*mcp.Resource, time.Duration, error) {
	var resources []*mcp.Resource
	refreshAfter := maximumRefresh
	seen := make(map[string]struct{})
	for cursor := ""; ; {
		page, err := session.ListResources(ctx, &mcp.ListResourcesParams{Cursor: cursor})
		if err != nil {
			return nil, 0, fmt.Errorf("list resources: %w", err)
		}
		resources = append(resources, page.Resources...)
		refreshAfter = refreshDelay(refreshAfter, page.TTLMs)
		next, done, err := nextPageCursor("resources", page.NextCursor, seen)
		if err != nil {
			return nil, 0, err
		}
		if done {
			return resources, refreshAfter, nil
		}
		cursor = next
	}
}

func discoverResourceTemplates(ctx context.Context, session *mcp.ClientSession, maximumRefresh time.Duration) ([]*mcp.ResourceTemplate, time.Duration, error) {
	var templates []*mcp.ResourceTemplate
	refreshAfter := maximumRefresh
	seen := make(map[string]struct{})
	for cursor := ""; ; {
		page, err := session.ListResourceTemplates(ctx, &mcp.ListResourceTemplatesParams{Cursor: cursor})
		if err != nil {
			return nil, 0, fmt.Errorf("list resource templates: %w", err)
		}
		templates = append(templates, page.ResourceTemplates...)
		refreshAfter = refreshDelay(refreshAfter, page.TTLMs)
		next, done, err := nextPageCursor("resource templates", page.NextCursor, seen)
		if err != nil {
			return nil, 0, err
		}
		if done {
			return templates, refreshAfter, nil
		}
		cursor = next
	}
}

func nextPageCursor(feature, next string, seen map[string]struct{}) (string, bool, error) {
	if next == "" {
		return "", true, nil
	}
	if _, exists := seen[next]; exists {
		return "", false, fmt.Errorf("list %s: backend repeated pagination cursor", feature)
	}
	seen[next] = struct{}{}
	return next, false, nil
}

func refreshDelay(current time.Duration, ttlMilliseconds int) time.Duration {
	if ttlMilliseconds <= 0 {
		return current
	}
	ttl := time.Duration(ttlMilliseconds) * time.Millisecond
	if ttl < current {
		return ttl
	}
	return current
}
