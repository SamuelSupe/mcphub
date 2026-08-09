package backend

import (
	"context"
	"fmt"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/SamuelSupe/mcphub/internal/config"
	"github.com/SamuelSupe/mcphub/internal/mcpcompat"
	"github.com/SamuelSupe/mcphub/internal/version"
)

type ProbeResult struct {
	LatencyMS       int64         `json:"latency_ms"`
	ProtocolVersion string        `json:"protocol_version"`
	Server          ProbeServer   `json:"server"`
	Capabilities    ProbeFeatures `json:"capabilities"`
	Counts          ProbeCounts   `json:"counts"`
}

type ProbeServer struct {
	Name    string `json:"name"`
	Title   string `json:"title,omitempty"`
	Version string `json:"version,omitempty"`
}

type ProbeFeatures struct {
	Tools       bool `json:"tools"`
	Prompts     bool `json:"prompts"`
	Resources   bool `json:"resources"`
	Completions bool `json:"completions"`
}

type ProbeCounts struct {
	Tools             int `json:"tools"`
	Prompts           int `json:"prompts"`
	Resources         int `json:"resources"`
	ResourceTemplates int `json:"resource_templates"`
}

func Probe(ctx context.Context, cfg config.BackendConfig, maximumRefresh time.Duration) (*ProbeResult, error) {
	started := time.Now()
	probeCtx, cancel := context.WithTimeout(ctx, cfg.RequestTimeout.Duration)
	defer cancel()
	httpClient, err := newHTTPClient(probeCtx, cfg)
	if err != nil {
		return nil, err
	}
	httpClient.Transport = &mcpcompat.RoundTripper{Base: httpClient.Transport}
	client := mcp.NewClient(
		&mcp.Implementation{Name: "mcphub", Version: version.Value},
		&mcp.ClientOptions{Capabilities: &mcp.ClientCapabilities{}, MultiRoundTrip: &mcp.MultiRoundTripOptions{Disabled: true}},
	)
	session, err := client.Connect(probeCtx, &mcp.StreamableClientTransport{
		Endpoint: cfg.URL, HTTPClient: httpClient, MaxRetries: -1,
	}, nil)
	if err != nil {
		return nil, fmt.Errorf("connect MCP session: %w", err)
	}
	defer session.Close()
	catalog, err := discoverCatalog(probeCtx, session, maximumRefresh)
	if err != nil {
		return nil, err
	}
	initialized := session.InitializeResult()
	result := &ProbeResult{LatencyMS: time.Since(started).Milliseconds()}
	if initialized != nil {
		result.ProtocolVersion = initialized.ProtocolVersion
		if initialized.ServerInfo != nil {
			result.Server = ProbeServer{
				Name: initialized.ServerInfo.Name, Title: initialized.ServerInfo.Title, Version: initialized.ServerInfo.Version,
			}
		}
		if initialized.Capabilities != nil {
			result.Capabilities = ProbeFeatures{
				Tools:       initialized.Capabilities.Tools != nil,
				Prompts:     initialized.Capabilities.Prompts != nil,
				Resources:   initialized.Capabilities.Resources != nil,
				Completions: initialized.Capabilities.Completions != nil,
			}
		}
	}
	result.Counts = ProbeCounts{
		Tools: len(catalog.Tools), Prompts: len(catalog.Prompts), Resources: len(catalog.Resources),
		ResourceTemplates: len(catalog.ResourceTemplates),
	}
	return result, nil
}
