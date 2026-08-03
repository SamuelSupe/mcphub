package app

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/SamuelSupe/mcphub/internal/config"
)

func TestReloadRetainsLastKnownGoodCatalogForUnavailableOptionalBackend(t *testing.T) {
	backendHTTP := startReloadBackend(t)
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	writeReloadConfig(t, configPath, backendHTTP.URL, false, "")
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("load initial config: %v", err)
	}
	application := newReloadTestApp(t, cfg, configPath)
	previous := application.currentRuntime()
	client, _ := previous.manager.Client("alpha")
	if client.Catalog() == nil || !client.Ready() {
		t.Fatal("initial optional backend is not ready")
	}

	backendHTTP.CloseClientConnections()
	backendHTTP.Close()
	if err := application.Reload(); err != nil {
		t.Fatalf("reload with unavailable optional backend: %v", err)
	}
	current := application.currentRuntime()
	if current == previous {
		t.Fatal("reload did not swap runtime generation")
	}
	client, _ = current.manager.Client("alpha")
	if client.Catalog() == nil {
		t.Fatal("optional backend lost its last-known-good catalog")
	}
	if client.Ready() {
		t.Fatal("unavailable optional backend reported ready")
	}
}

func TestReloadDoesNotReuseCatalogAcrossBackendCredentialChange(t *testing.T) {
	backendHTTP := startReloadBackend(t)
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	writeReloadConfig(t, configPath, backendHTTP.URL, false, "")
	setReloadBackendHeader(t, configPath, "tenant-a")
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("load initial config: %v", err)
	}
	application := newReloadTestApp(t, cfg, configPath)
	previousClient, _ := application.currentRuntime().manager.Client("alpha")
	if previousClient.Catalog() == nil || !previousClient.Ready() {
		t.Fatal("initial optional backend is not ready")
	}

	backendHTTP.CloseClientConnections()
	backendHTTP.Close()
	setReloadBackendHeader(t, configPath, "tenant-b")
	if err := application.Reload(); err != nil {
		t.Fatalf("reload with unavailable optional backend: %v", err)
	}
	currentClient, _ := application.currentRuntime().manager.Client("alpha")
	if currentClient.Catalog() != nil {
		t.Fatal("catalog from the previous backend credential identity was reused")
	}
	if currentClient.Ready() {
		t.Fatal("unavailable optional backend reported ready")
	}
}

func TestReloadRollsBackWhenNewRequiredBackendIsUnavailable(t *testing.T) {
	backendHTTP := startReloadBackend(t)
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	writeReloadConfig(t, configPath, backendHTTP.URL, true, "")
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("load initial config: %v", err)
	}
	application := newReloadTestApp(t, cfg, configPath)
	previous := application.currentRuntime()

	writeReloadConfig(t, configPath, backendHTTP.URL, true, `
  - id: unavailable
    url: http://127.0.0.1:1/mcp
    required: true
    allow_insecure_http: true
    request_timeout: 100ms
`)
	if err := application.Reload(); err == nil {
		t.Fatal("reload accepted an unavailable required backend")
	}
	if current := application.currentRuntime(); current != previous {
		t.Fatal("failed reload replaced the active runtime")
	}
	if !previous.manager.Ready() {
		t.Fatal("failed reload disrupted the previous required backend")
	}
}

func TestShutdownCancelsInProgressReloadWithoutReplacingRuntime(t *testing.T) {
	backendHTTP := startReloadBackend(t)
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	writeReloadConfig(t, configPath, backendHTTP.URL, true, "")
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("load initial config: %v", err)
	}
	application := newReloadTestApp(t, cfg, configPath)
	previous := application.currentRuntime()

	started := make(chan struct{}, 1)
	release := make(chan struct{})
	slowBackend := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, req *http.Request) {
		select {
		case started <- struct{}{}:
		default:
		}
		select {
		case <-req.Context().Done():
		case <-release:
		}
	}))
	t.Cleanup(func() {
		close(release)
		slowBackend.CloseClientConnections()
		slowBackend.Close()
	})
	writeReloadConfig(t, configPath, backendHTTP.URL, true, fmt.Sprintf(`
  - id: slow
    url: %s
    required: true
    allow_insecure_http: true
    request_timeout: 30s
`, slowBackend.URL))

	reloadDone := make(chan error, 1)
	go func() {
		reloadDone <- application.Reload()
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("reload did not start the slow backend connection")
	}

	application.stopping.Store(true)
	application.cancelReloadCandidate()
	select {
	case err := <-reloadDone:
		if err == nil {
			t.Fatal("reload succeeded after shutdown began")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("shutdown did not cancel the in-progress reload")
	}
	if application.currentRuntime() != previous {
		t.Fatal("canceled reload replaced the active runtime")
	}
}

func TestReloadRejectsRestartOnlyConfigurationChanges(t *testing.T) {
	backendHTTP := startReloadBackend(t)
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	writeReloadConfig(t, configPath, backendHTTP.URL, true, "")
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("load initial config: %v", err)
	}
	application := newReloadTestApp(t, cfg, configPath)
	previous := application.currentRuntime()

	content, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if !strings.Contains(string(content), `listen: ":8080"`) {
		t.Fatal("test config does not contain the listen address")
	}
	changed := []byte(strings.Replace(string(content), `listen: ":8080"`, `listen: ":9090"`, 1))
	if err := os.WriteFile(configPath, changed, 0o600); err != nil {
		t.Fatalf("write changed config: %v", err)
	}
	if err := application.Reload(); err == nil {
		t.Fatal("reload accepted a changed listen address")
	}
	if application.currentRuntime() != previous {
		t.Fatal("restart-only change replaced the active runtime")
	}
}

func newReloadTestApp(t *testing.T, cfg *config.Config, configPath string) *App {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx, cancel := context.WithCancel(context.Background())
	rt, err := newRuntime(ctx, cfg, logger, false)
	if err != nil {
		cancel()
		t.Fatalf("new runtime: %v", err)
	}
	application := &App{
		ctx:        ctx,
		cancel:     cancel,
		configPath: configPath,
		logger:     logger,
		auth:       staticVerifier{},
		runtime:    rt,
	}
	t.Cleanup(application.Close)
	return application
}

func startReloadBackend(t *testing.T) *httptest.Server {
	t.Helper()
	server := mcp.NewServer(&mcp.Implementation{Name: "reload-backend", Version: "1"}, nil)
	server.AddTool(&mcp.Tool{Name: "echo", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "ok"}}}, nil
	})
	httpServer := httptest.NewServer(mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return server },
		&mcp.StreamableHTTPOptions{Stateless: true},
	))
	t.Cleanup(func() {
		httpServer.CloseClientConnections()
		httpServer.Close()
	})
	return httpServer
}

func writeReloadConfig(t *testing.T, path, backendURL string, required bool, extraBackends string) {
	t.Helper()
	content := fmt.Sprintf(`
server:
  listen: ":8080"
  public_url: https://hub.example.com/mcp
  page_size: 100
  request_timeout: 500ms
  drain_timeout: 100ms
  refresh_interval: 1h
  catalog_ttl: 30s
auth:
  issuer: https://idp.example.com
backends:
  - id: alpha
    url: %s
    required: %t
    allow_insecure_http: true
    request_timeout: 500ms
%s`, backendURL, required, extraBackends)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

func setReloadBackendHeader(t *testing.T, path, tenant string) {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read reload config: %v", err)
	}
	if strings.Contains(string(content), "    headers:\n") {
		updated := strings.Replace(string(content), "      X-Tenant: tenant-a", "      X-Tenant: "+tenant, 1)
		if updated == string(content) {
			t.Fatal("reload config does not contain the expected tenant header")
		}
		content = []byte(updated)
	} else {
		needle := "    allow_insecure_http: true\n"
		insert := needle + "    headers:\n      X-Tenant: " + tenant + "\n"
		updated := strings.Replace(string(content), needle, insert, 1)
		if updated == string(content) {
			t.Fatal("reload config does not contain the backend insertion point")
		}
		content = []byte(updated)
	}
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("write reload config header: %v", err)
	}
}
