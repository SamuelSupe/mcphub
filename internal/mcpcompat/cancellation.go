package mcpcompat

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
)

const (
	statelessProtocolVersion  = "2026-07-28"
	protocolVersionMetaKey    = "io.modelcontextprotocol/protocolVersion"
	clientCapabilitiesMetaKey = "io.modelcontextprotocol/clientCapabilities"
)

// NormalizeCancellation adds the request metadata required by the 2026-07-28
// protocol when an SDK v1.7.0 client sends notifications/cancelled without it.
// The same SDK server rejects that notification with HTTP 400, which otherwise
// makes the client's entire logical connection unusable after cancellation.
func NormalizeCancellation(body []byte, protocolVersion string) ([]byte, bool) {
	if protocolVersion < statelessProtocolVersion {
		return body, false
	}

	var message map[string]json.RawMessage
	if err := json.Unmarshal(body, &message); err != nil {
		return body, false
	}
	var method string
	if err := json.Unmarshal(message["method"], &method); err != nil || method != "notifications/cancelled" {
		return body, false
	}

	var params map[string]json.RawMessage
	if err := json.Unmarshal(message["params"], &params); err != nil || params == nil {
		return body, false
	}
	meta := make(map[string]json.RawMessage)
	if raw, exists := params["_meta"]; exists {
		if err := json.Unmarshal(raw, &meta); err != nil || meta == nil {
			return body, false
		}
	}
	changed := false
	if _, exists := meta[protocolVersionMetaKey]; !exists {
		version, _ := json.Marshal(protocolVersion)
		meta[protocolVersionMetaKey] = version
		changed = true
	}
	if _, exists := meta[clientCapabilitiesMetaKey]; !exists {
		meta[clientCapabilitiesMetaKey] = json.RawMessage(`{}`)
		changed = true
	}
	if !changed {
		return body, false
	}
	rawMeta, _ := json.Marshal(meta)
	params["_meta"] = rawMeta
	rawParams, _ := json.Marshal(params)
	message["params"] = rawParams
	normalized, err := json.Marshal(message)
	if err != nil {
		return body, false
	}
	return normalized, true
}

// RoundTripper applies NormalizeCancellation to outbound MCP requests.
type RoundTripper struct {
	Base http.RoundTripper
}

func (t *RoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	base := t.Base
	if base == nil {
		base = http.DefaultTransport
	}
	if req.Method != http.MethodPost || req.Body == nil || req.Header.Get("Mcp-Protocol-Version") < statelessProtocolVersion || req.Header.Get("Mcp-Method") != "notifications/cancelled" {
		return base.RoundTrip(req)
	}

	body, err := io.ReadAll(req.Body)
	_ = req.Body.Close()
	if err != nil {
		return nil, err
	}
	normalized, changed := NormalizeCancellation(body, req.Header.Get("Mcp-Protocol-Version"))
	if !changed {
		normalized = body
	}

	cloned := req.Clone(req.Context())
	cloned.Header = req.Header.Clone()
	cloned.Body = io.NopCloser(bytes.NewReader(normalized))
	cloned.ContentLength = int64(len(normalized))
	cloned.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(normalized)), nil
	}
	return base.RoundTrip(cloned)
}
