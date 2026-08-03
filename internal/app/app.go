package app

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	mcpauth "github.com/modelcontextprotocol/go-sdk/auth"

	"github.com/SamuelSupe/mcphub/internal/authn"
	"github.com/SamuelSupe/mcphub/internal/config"
)

type App struct {
	ctx        context.Context
	cancel     context.CancelFunc
	shutdown   <-chan struct{}
	configPath string
	logger     *slog.Logger
	auth       tokenVerifier
	stopping   atomic.Bool

	runtimeMu       sync.RWMutex
	runtime         *runtime
	reloadMu        sync.Mutex
	candidateMu     sync.Mutex
	candidateCancel context.CancelFunc

	server *http.Server
}

type tokenVerifier interface {
	Ready() bool
	Verify(context.Context, string, *http.Request) (*mcpauth.TokenInfo, error)
}

func New(parent context.Context, cfg *config.Config, configPath string, logger *slog.Logger) (*App, error) {
	// The parent signals that graceful shutdown should begin. Runtime work uses
	// an independently cancelled context so in-flight requests keep their
	// backend connections until the HTTP drain completes.
	ctx, cancel := context.WithCancel(context.WithoutCancel(parent))
	authManager := authn.NewManager(cfg.Auth.Issuer, cfg.Server.PublicURL, logger)
	go authManager.Run(ctx)
	rt, err := newRuntime(ctx, cfg, logger, false)
	if err != nil {
		cancel()
		return nil, err
	}
	app := &App{
		ctx:        ctx,
		cancel:     cancel,
		shutdown:   parent.Done(),
		configPath: configPath,
		logger:     logger,
		auth:       authManager,
		runtime:    rt,
	}
	app.server = &http.Server{
		Addr:              cfg.Server.Listen,
		Handler:           app,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelError),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    1 << 20,
	}
	return app, nil
}

func (a *App) Run() error {
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	defer signal.Stop(hup)
	go func() {
		for {
			select {
			case <-a.ctx.Done():
				return
			case <-a.shutdown:
				return
			case <-hup:
				if err := a.Reload(); err != nil {
					a.logger.Error("configuration reload rejected", "error_type", fmt.Sprintf("%T", err))
				} else {
					a.logger.Info("configuration reloaded")
				}
			}
		}
	}()

	errCh := make(chan error, 1)
	go func() {
		errCh <- a.server.ListenAndServe()
	}()
	select {
	case <-a.shutdown:
		a.stopping.Store(true)
		a.cancelReloadCandidate()
		// Let an already-running reload finish, while preventing a new one from
		// starting, before choosing the generation to drain.
		a.reloadMu.Lock()
		a.reloadMu.Unlock()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), a.currentConfig().Server.DrainTimeout.Duration)
		defer cancel()
		if err := a.server.Shutdown(shutdownCtx); err != nil {
			a.logger.Warn("HTTP drain did not complete; forcing connection close", "error_type", fmt.Sprintf("%T", err))
			_ = a.server.Close()
		}
		a.Close()
		return nil
	case <-a.ctx.Done():
		_ = a.server.Close()
		return nil
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		a.Close()
		return err
	}
}

func (a *App) Close() {
	a.stopping.Store(true)
	a.cancel()
	a.runtimeMu.Lock()
	rt := a.runtime
	a.runtime = nil
	a.runtimeMu.Unlock()
	if rt != nil {
		rt.close()
	}
}

func (a *App) Reload() error {
	a.reloadMu.Lock()
	defer a.reloadMu.Unlock()
	if a.stopping.Load() {
		return fmt.Errorf("service is shutting down")
	}

	cfg, err := config.Load(a.configPath)
	if err != nil {
		return err
	}
	current := a.currentConfig()
	if err := current.ImmutableEqual(cfg); err != nil {
		return err
	}
	candidateParent, cancelCandidate := context.WithCancel(a.ctx)
	a.candidateMu.Lock()
	if a.stopping.Load() {
		a.candidateMu.Unlock()
		cancelCandidate()
		return fmt.Errorf("service is shutting down")
	}
	a.candidateCancel = cancelCandidate
	a.candidateMu.Unlock()

	candidate, err := newRuntime(candidateParent, cfg, a.logger, true)
	if err != nil {
		a.clearReloadCandidate()
		cancelCandidate()
		return err
	}
	candidate.parentCancel = cancelCandidate
	a.clearReloadCandidate()
	if a.stopping.Load() {
		candidate.close()
		return fmt.Errorf("service is shutting down")
	}
	previous := a.currentRuntime()
	if previous == nil {
		candidate.close()
		return fmt.Errorf("service is shutting down")
	}
	for _, id := range candidate.manager.SeedUnavailableCatalogs(previous.manager) {
		candidate.hub.ReconcileBackend(id)
	}

	a.runtimeMu.Lock()
	if a.stopping.Load() {
		a.runtimeMu.Unlock()
		candidate.close()
		return fmt.Errorf("service is shutting down")
	}
	if a.runtime != previous {
		a.runtimeMu.Unlock()
		candidate.close()
		return fmt.Errorf("runtime changed while reloading configuration")
	}
	a.runtime = candidate
	a.runtimeMu.Unlock()
	go previous.drain(previous.cfg.Server.DrainTimeout.Duration)
	return nil
}

func (a *App) cancelReloadCandidate() {
	a.candidateMu.Lock()
	cancel := a.candidateCancel
	a.candidateMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (a *App) clearReloadCandidate() {
	a.candidateMu.Lock()
	a.candidateCancel = nil
	a.candidateMu.Unlock()
}

func (a *App) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	requestID := req.Header.Get("X-Request-Id")
	if !validRequestID(requestID) {
		requestID = rand.Text()
		req.Header.Set("X-Request-Id", requestID)
	}
	w.Header().Set("X-Request-Id", requestID)

	a.runtimeMu.RLock()
	rt := a.runtime
	if rt == nil {
		a.runtimeMu.RUnlock()
		http.Error(w, "service unavailable", http.StatusServiceUnavailable)
		return
	}
	rt.active.Add(1)
	a.runtimeMu.RUnlock()
	defer rt.active.Done()

	requestCtx, cancelRequest := context.WithCancel(req.Context())
	stopRuntimeCancellation := context.AfterFunc(rt.ctx, cancelRequest)
	if rt.ctx.Err() != nil {
		cancelRequest()
	}
	defer func() {
		stopRuntimeCancellation()
		cancelRequest()
	}()
	req = req.WithContext(requestCtx)

	controller := http.NewResponseController(w)
	_ = controller.SetReadDeadline(time.Now().Add(rt.cfg.Server.RequestTimeout.Duration))
	defer func() {
		// A route can reject a request before consuming its body. Close it while
		// the deadline is active so a slow body cannot retain a server goroutine
		// indefinitely. authorizeMCPRequest clears the deadline after it has read
		// an accepted long-lived subscriptions/listen request.
		_ = req.Body.Close()
		_ = controller.SetReadDeadline(time.Time{})
	}()

	if !a.allowOrigin(w, req, rt) {
		return
	}

	switch req.URL.Path {
	case "/healthz":
		writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
	case "/readyz":
		a.serveReady(w, rt)
	case "/.well-known/oauth-protected-resource", rt.cfg.ResourceMetadataPath():
		a.serveResourceMetadata(w, req, rt)
	case rt.cfg.MCPPath():
		a.serveMCP(w, req, rt)
	default:
		http.NotFound(w, req)
	}
}

func (a *App) currentRuntime() *runtime {
	a.runtimeMu.RLock()
	defer a.runtimeMu.RUnlock()
	return a.runtime
}

func (a *App) currentConfig() *config.Config {
	rt := a.currentRuntime()
	if rt == nil {
		return &config.Config{}
	}
	return rt.cfg
}
