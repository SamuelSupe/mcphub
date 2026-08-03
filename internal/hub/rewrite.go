package hub

import "github.com/modelcontextprotocol/go-sdk/mcp"

func (h *Hub) rewriteToolResult(backendID string, result *mcp.CallToolResult) *mcp.CallToolResult {
	if result == nil {
		return nil
	}
	copyResult := *result
	copyResult.Content = h.rewriteContents(backendID, result.Content)
	return &copyResult
}

func (h *Hub) rewritePromptResult(backendID string, result *mcp.GetPromptResult) *mcp.GetPromptResult {
	if result == nil {
		return nil
	}
	copyResult := *result
	copyResult.Messages = make([]*mcp.PromptMessage, 0, len(result.Messages))
	for _, message := range result.Messages {
		if message == nil {
			copyResult.Messages = append(copyResult.Messages, nil)
			continue
		}
		copyMessage := *message
		copyMessage.Content = h.rewriteContent(backendID, message.Content)
		copyResult.Messages = append(copyResult.Messages, &copyMessage)
	}
	return &copyResult
}

func (h *Hub) rewriteResourceResult(backendID string, result *mcp.ReadResourceResult) *mcp.ReadResourceResult {
	if result == nil {
		return nil
	}
	copyResult := *result
	copyResult.Contents = make([]*mcp.ResourceContents, 0, len(result.Contents))
	for _, content := range result.Contents {
		if content == nil {
			copyResult.Contents = append(copyResult.Contents, nil)
			continue
		}
		copyContent := *content
		if copyContent.URI != "" {
			h.rememberResource(backendID, copyContent.URI)
			copyContent.URI = encodeResource(backendID, copyContent.URI)
		}
		copyResult.Contents = append(copyResult.Contents, &copyContent)
	}
	return &copyResult
}

func (h *Hub) rewriteContents(backendID string, contents []mcp.Content) []mcp.Content {
	if contents == nil {
		return nil
	}
	result := make([]mcp.Content, 0, len(contents))
	for _, content := range contents {
		result = append(result, h.rewriteContent(backendID, content))
	}
	return result
}

func (h *Hub) rewriteContent(backendID string, content mcp.Content) mcp.Content {
	switch value := content.(type) {
	case *mcp.ResourceLink:
		if value == nil {
			return value
		}
		copyValue := *value
		h.rememberResource(backendID, value.URI)
		copyValue.URI = encodeResource(backendID, value.URI)
		return &copyValue
	case *mcp.EmbeddedResource:
		if value == nil {
			return value
		}
		copyValue := *value
		if value.Resource != nil {
			resource := *value.Resource
			h.rememberResource(backendID, resource.URI)
			resource.URI = encodeResource(backendID, resource.URI)
			copyValue.Resource = &resource
		}
		return &copyValue
	case *mcp.ToolResultContent:
		if value == nil {
			return value
		}
		copyValue := *value
		copyValue.Content = h.rewriteContents(backendID, value.Content)
		return &copyValue
	default:
		return content
	}
}
