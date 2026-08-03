package hub

import (
	"reflect"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestRewriteToolResultChangesOnlyBackendResourceURIs(t *testing.T) {
	h := &Hub{
		views:           make(map[string]*view),
		issuedResources: make(map[string]*issuedResourceSet),
	}
	annotations := &mcp.Annotations{Audience: []mcp.Role{mcp.Role("user")}, Priority: 0.8}
	meta := mcp.Meta{"opaque": map[string]any{"keep": true}}
	link := &mcp.ResourceLink{
		URI:         "memory://linked",
		Name:        "linked",
		Meta:        meta,
		Annotations: annotations,
		Icons:       []mcp.Icon{{Source: "https://example.com/icon.png"}},
	}
	embedded := &mcp.EmbeddedResource{
		Resource:    &mcp.ResourceContents{URI: "memory://embedded", Text: "body", Meta: meta},
		Meta:        meta,
		Annotations: annotations,
	}
	structured := map[string]any{"answer": 42}
	result := &mcp.CallToolResult{
		Meta: mcp.Meta{"result": "meta"},
		Content: []mcp.Content{
			link,
			&mcp.ToolResultContent{ToolUseID: "nested", Content: []mcp.Content{embedded}, StructuredContent: structured, Meta: meta},
		},
		StructuredContent: structured,
	}

	rewritten := h.rewriteToolResult("alpha", result)
	if rewritten == result {
		t.Fatal("rewrite returned the original result pointer")
	}
	if link.URI != "memory://linked" || embedded.Resource.URI != "memory://embedded" {
		t.Fatal("rewrite mutated the downstream result")
	}
	rewrittenLink, ok := rewritten.Content[0].(*mcp.ResourceLink)
	if !ok {
		t.Fatalf("rewritten link type = %T", rewritten.Content[0])
	}
	if backendID, original, ok := decodeResource(rewrittenLink.URI); !ok || backendID != "alpha" || original != link.URI {
		t.Fatalf("rewritten link URI = %q", rewrittenLink.URI)
	}
	if rewrittenLink.Meta["opaque"] == nil || rewrittenLink.Annotations != annotations || len(rewrittenLink.Icons) != 1 {
		t.Fatalf("rewritten link lost protocol fields: %#v", rewrittenLink)
	}
	nested, ok := rewritten.Content[1].(*mcp.ToolResultContent)
	if !ok {
		t.Fatalf("nested result type = %T", rewritten.Content[1])
	}
	rewrittenEmbedded, ok := nested.Content[0].(*mcp.EmbeddedResource)
	if !ok || rewrittenEmbedded.Resource == nil {
		t.Fatalf("nested content type = %T", nested.Content[0])
	}
	if backendID, original, ok := decodeResource(rewrittenEmbedded.Resource.URI); !ok || backendID != "alpha" || original != embedded.Resource.URI {
		t.Fatalf("rewritten embedded URI = %q", rewrittenEmbedded.Resource.URI)
	}
	if !reflect.DeepEqual(rewritten.StructuredContent, structured) || !reflect.DeepEqual(nested.StructuredContent, structured) {
		t.Fatal("rewrite changed structured content")
	}
}
