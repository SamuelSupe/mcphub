package backend

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/SamuelSupe/mcphub/internal/config"
)

func TestClientCredentialsDiscoveryWithoutPKCEAndHeaderIsolation(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/.well-known/oauth-authorization-server":
			if got := req.Header.Get("X-API-Key"); got != "" {
				t.Errorf("metadata request leaked backend header %q", got)
			}
			writeBackendTestJSON(t, w, map[string]any{
				"issuer":                                server.URL,
				"authorization_endpoint":                server.URL + "/authorize",
				"token_endpoint":                        server.URL + "/token",
				"jwks_uri":                              server.URL + "/jwks",
				"response_types_supported":              []string{"token"},
				"grant_types_supported":                 []string{"client_credentials"},
				"token_endpoint_auth_methods_supported": []string{"client_secret_basic"},
			})
		case "/token":
			if got := req.Header.Get("X-API-Key"); got != "" {
				t.Errorf("token request leaked backend header %q", got)
			}
			if username, password, ok := req.BasicAuth(); !ok || username != "client" || password != "secret" {
				t.Errorf("token endpoint credentials = (%q, %q, %v)", username, password, ok)
			}
			writeBackendTestJSON(t, w, map[string]any{
				"access_token": "backend-token",
				"token_type":   "Bearer",
				"expires_in":   3600,
			})
		case "/backend":
			if got, want := req.Header.Get("X-API-Key"), "fixed-secret"; got != want {
				t.Errorf("backend API key = %q, want %q", got, want)
			}
			if got, want := req.Header.Get("Authorization"), "Bearer backend-token"; got != want {
				t.Errorf("backend Authorization = %q, want %q", got, want)
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, req)
		}
	}))
	defer server.Close()

	client, err := newHTTPClient(context.Background(), config.BackendConfig{
		ID:             "alpha",
		RequestTimeout: config.Duration{Duration: 5 * time.Second},
		Headers:        map[string]string{"X-API-Key": "fixed-secret"},
		OAuth: &config.OAuthConfig{
			Type:         "client_credentials",
			Issuer:       server.URL,
			ClientID:     "client",
			ClientSecret: "secret",
			Scopes:       []string{"mcp.read", "mcp.write"},
		},
	})
	if err != nil {
		t.Fatalf("newHTTPClient: %v", err)
	}
	resp, err := client.Get(server.URL + "/backend")
	if err != nil {
		t.Fatalf("backend request: %v", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("backend status = %d", resp.StatusCode)
	}
}

func TestOAuthDiscoveryRejectsInsecureTokenEndpointForHTTPSIssuer(t *testing.T) {
	var issuer *httptest.Server
	issuer = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/.well-known/oauth-authorization-server" {
			http.NotFound(w, req)
			return
		}
		writeBackendTestJSON(t, w, map[string]any{
			"issuer":         issuer.URL,
			"token_endpoint": "http://127.0.0.1:8081/token",
		})
	}))
	defer issuer.Close()

	_, err := discoverOAuthTokenEndpoint(context.Background(), issuer.URL, issuer.Client())
	if err == nil || !strings.Contains(err.Error(), "must use HTTPS") {
		t.Fatalf("discovery error = %v, want insecure token endpoint rejection", err)
	}
}

func TestBackendHTTPClientDoesNotFollowRedirects(t *testing.T) {
	var destinationCalled atomic.Bool
	destination := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		destinationCalled.Store(true)
	}))
	defer destination.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", destination.URL)
		w.WriteHeader(http.StatusFound)
	}))
	defer source.Close()

	client, err := newHTTPClient(context.Background(), config.BackendConfig{
		ID:             "alpha",
		RequestTimeout: config.Duration{Duration: 5 * time.Second},
		Headers:        map[string]string{"Authorization": "Bearer fixed"},
	})
	if err != nil {
		t.Fatalf("newHTTPClient: %v", err)
	}
	resp, err := client.Get(source.URL)
	if err != nil {
		t.Fatalf("redirect request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusFound || destinationCalled.Load() {
		t.Fatalf("redirect status = %d, destination called = %v", resp.StatusCode, destinationCalled.Load())
	}
}

func TestProgressBodyBoundsOversizedEventAndForwardsFollowingProgress(t *testing.T) {
	progressEvent := func(token string, progress float64) []byte {
		data, err := json.Marshal(map[string]any{
			"jsonrpc": "2.0",
			"method":  "notifications/progress",
			"params": map[string]any{
				"progressToken": token,
				"progress":      progress,
			},
		})
		if err != nil {
			t.Fatalf("marshal progress event: %v", err)
		}
		event := append([]byte("data: "), data...)
		return append(event, '\n', '\n')
	}

	oversized := progressEvent("oversized", 99)
	// Keep the event semantically valid if it were inspected: SSE comments are
	// ignored by sseData, but still count toward the bounded inspection limit.
	prefix := oversized[:len(oversized)-1]
	minimumFiller := maximumInspectedSSEEventBytes - len(prefix) + 64
	prefix = append(prefix, bytes.Repeat([]byte(":\n"), minimumFiller/2+1)...)
	oversized = append(prefix, '\n')
	if len(oversized) <= maximumInspectedSSEEventBytes {
		t.Fatalf("oversized event length = %d, want > %d", len(oversized), maximumInspectedSSEEventBytes)
	}
	normal := progressEvent("normal", 1)
	firstSplit := maximumInspectedSSEEventBytes / 2
	secondSplit := maximumInspectedSSEEventBytes + 16
	chunks := [][]byte{
		oversized[:firstSplit],
		oversized[firstSplit:secondSplit],
		oversized[secondSplit:],
		normal[:len(normal)/2],
		normal[len(normal)/2:],
	}

	var (
		callbackCount       int
		callbackInFinalRead bool
		inFinalRead         bool
		callbackParams      *mcp.ProgressNotificationParams
	)
	body := &progressBody{
		body: &progressChunkReader{chunks: chunks},
		handle: func(params *mcp.ProgressNotificationParams) {
			callbackCount++
			callbackInFinalRead = inFinalRead
			copyParams := *params
			callbackParams = &copyParams
		},
	}

	var output bytes.Buffer
	sawOversized := false
	for i, chunk := range chunks {
		readBuffer := make([]byte, len(chunk))
		inFinalRead = i == len(chunks)-1
		n, err := body.Read(readBuffer)
		inFinalRead = false
		if err != nil {
			t.Fatalf("Read chunk %d: %v", i, err)
		}
		if n != len(chunk) {
			t.Fatalf("Read chunk %d returned %d bytes, want %d", i, n, len(chunk))
		}
		output.Write(readBuffer[:n])
		if body.oversized {
			sawOversized = true
		}
		if len(body.event) > maximumInspectedSSEEventBytes {
			t.Fatalf("event inspection buffer length = %d, want <= %d", len(body.event), maximumInspectedSSEEventBytes)
		}
		if body.tailLen > 4 {
			t.Fatalf("SSE terminator tail length = %d, want <= 4", body.tailLen)
		}
	}
	if _, err := body.Read(make([]byte, 1)); err != io.EOF {
		t.Fatalf("final Read error = %v, want io.EOF", err)
	}
	if !bytes.Equal(output.Bytes(), bytes.Join(chunks, nil)) {
		t.Fatal("progressBody output differs from input")
	}
	if !sawOversized {
		t.Fatal("test input never entered oversized-event state")
	}
	if callbackCount != 1 {
		t.Fatalf("progress callback count = %d, want 1", callbackCount)
	}
	if !callbackInFinalRead {
		t.Fatal("progress callback did not run before the terminating Read returned")
	}
	if callbackParams == nil || callbackParams.ProgressToken != "normal" || callbackParams.Progress != 1 {
		t.Fatalf("progress callback params = %#v, want normal token and progress 1", callbackParams)
	}
}

type progressChunkReader struct {
	chunks [][]byte
	index  int
}

func (r *progressChunkReader) Read(p []byte) (int, error) {
	if r.index == len(r.chunks) {
		return 0, io.EOF
	}
	chunk := r.chunks[r.index]
	if len(p) < len(chunk) {
		return 0, io.ErrShortBuffer
	}
	r.index++
	return copy(p, chunk), nil
}

func (r *progressChunkReader) Close() error { return nil }

func writeBackendTestJSON(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Errorf("encode response: %v", err)
	}
}
