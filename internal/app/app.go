package app

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	mcpauth "github.com/modelcontextprotocol/go-sdk/auth"

	"github.com/SamuelSupe/mcphub/v2/internal/authn"
	"github.com/SamuelSupe/mcphub/v2/internal/config"
	"github.com/SamuelSupe/mcphub/v2/internal/configstore"
	"github.com/SamuelSupe/mcphub/v2/internal/diagnostics"
	"github.com/SamuelSupe/mcphub/v2/internal/httptool"
	"github.com/SamuelSupe/mcphub/v2/internal/ratelimit"
	"github.com/SamuelSupe/mcphub/v2/internal/sso"
)

type App struct {
	requests   diagnostics.Recorder
	limits     ratelimit.Registry
	ctx        context.Context
	cancel     context.CancelFunc
	shutdown   <-chan struct{}
	configPath string
	logger     *slog.Logger
	auth       tokenVerifier
	sso        *sso.Server
	stopping   atomic.Bool

	runtimeMu       sync.RWMutex
	runtime         *runtime
	reloadMu        sync.Mutex
	candidateMu     sync.Mutex
	candidateCancel context.CancelFunc
	refreshMu       sync.Mutex
	refreshFailures map[string]int

	server      *http.Server
	adminServer *http.Server
	adminAuth   *adminAuthorization
	userAuth    *adminAuthorization
	store       *configstore.Store
	closeOnce   sync.Once
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
	managedCfg, store, err := prepareManagedConfig(ctx, cfg, configPath)
	if err != nil {
		cancel()
		return nil, err
	}
	cfg = managedCfg
	groups, err := loadToolGroups(ctx, store)
	if err != nil {
		cancel()
		if store != nil {
			_ = store.Close()
		}
		return nil, err
	}
	var authManager tokenVerifier
	var ssoServer *sso.Server
	if cfg.Auth.SSO != nil {
		ssoServer, err = sso.New(ctx, cfg, store)
		if err != nil {
			cancel()
			if store != nil {
				_ = store.Close()
			}
			return nil, err
		}
		authManager = ssoServer.Verifier(cfg.Server.PublicURL)
	} else {
		manager := authn.NewManager(cfg.Auth.Issuer, cfg.Server.PublicURL, logger)
		go manager.Run(ctx)
		authManager = manager
	}
	rt, err := newRuntimeWithGroups(ctx, cfg, groups, logger, false)
	if err != nil {
		cancel()
		if store != nil {
			_ = store.Close()
		}
		return nil, err
	}
	app := &App{
		ctx:             ctx,
		cancel:          cancel,
		shutdown:        parent.Done(),
		configPath:      configPath,
		logger:          logger,
		auth:            authManager,
		sso:             ssoServer,
		runtime:         rt,
		store:           store,
		refreshFailures: make(map[string]int),
	}
	app.limits.Configure(rt.rateLimitPolicies())
	if store != nil {
		store.ConfigureApprovalDelivery(cfg.Admin.Approvals, cfg.Admin.PublicURL)
		if err := store.RecoverApprovals(ctx); err != nil {
			app.Close()
			return nil, fmt.Errorf("recover tool approvals: %w", err)
		}
	}
	rt.hub.ConfigureApprovals(store, &app.limits)
	rt.hub.ConfigureClientAuthorization(store)
	if cfg.ClientAuthorization.Enabled {
		u, _ := url.Parse(cfg.Server.PublicURL)
		portal := config.AdminConfig{PublicURL: u.Scheme + "://" + u.Host, ClientID: cfg.ClientAuthorization.ClientID, ClientSecretEnv: cfg.ClientAuthorization.ClientSecretEnv, RequiredScopes: rt.allScopes()}
		app.userAuth = newAdminAuthorization(portal, cfg.Auth.Issuer, authManager)
		app.userAuth.userPortal, app.userAuth.resource = true, cfg.Server.PublicURL
	}
	app.server = &http.Server{
		Addr:              cfg.Server.Listen,
		Handler:           app,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelError),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    1 << 20,
	}
	if cfg.Admin.Enabled {
		if cfg.Admin.Remote() {
			var adminManager tokenVerifier
			if ssoServer != nil {
				adminManager = ssoServer.Verifier(cfg.Admin.PublicURL)
			} else {
				manager := authn.NewManager(cfg.Auth.Issuer, cfg.Admin.PublicURL, logger)
				go manager.Run(ctx)
				adminManager = manager
			}
			app.adminAuth = newAdminAuthorization(cfg.Admin, cfg.Auth.Issuer, adminManager)
		}
		app.adminServer = &http.Server{
			Addr: cfg.Admin.Listen, Handler: app.adminHandler(),
			ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelError),
			ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 2 * time.Minute, IdleTimeout: 2 * time.Minute, MaxHeaderBytes: 1 << 20,
		}
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
	if a.store != nil {
		go a.runOpenAPIRefreshLoop()
		go a.runApprovalMaintenance()
		go a.runApprovalDelivery()
	}

	errCh := make(chan error, 2)
	go func() {
		errCh <- a.server.ListenAndServe()
	}()
	if a.adminServer != nil {
		go func() {
			errCh <- a.adminServer.ListenAndServe()
		}()
	}
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
		if a.adminServer != nil {
			if err := a.adminServer.Shutdown(shutdownCtx); err != nil {
				_ = a.adminServer.Close()
			}
		}
		a.Close()
		return nil
	case <-a.ctx.Done():
		_ = a.server.Close()
		if a.adminServer != nil {
			_ = a.adminServer.Close()
		}
		return nil
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		_ = a.server.Close()
		if a.adminServer != nil {
			_ = a.adminServer.Close()
		}
		a.Close()
		return err
	}
}

func (a *App) Close() {
	a.closeOnce.Do(func() {
		a.stopping.Store(true)
		a.cancel()
		a.runtimeMu.Lock()
		rt := a.runtime
		a.runtime = nil
		a.runtimeMu.Unlock()
		if rt != nil {
			rt.close()
		}
		if a.store != nil {
			_ = a.store.Close()
		}
	})
}

func (a *App) Reload() error {
	a.reloadMu.Lock()
	defer a.reloadMu.Unlock()
	if a.stopping.Load() {
		return fmt.Errorf("service is shutting down")
	}

	cfg, err := config.LoadStatic(a.configPath)
	if err != nil {
		return err
	}
	current := a.currentConfig()
	if err := current.ImmutableEqual(cfg); err != nil {
		return err
	}
	if cfg.Admin.Enabled {
		records, err := a.store.List(a.ctx)
		if err != nil {
			return err
		}
		cfg = configWithRecords(cfg, records)
		if err := cfg.Validate(); err != nil {
			return err
		}
	}
	return a.replaceRuntimeLocked(cfg)
}

func (a *App) replaceRuntimeLocked(cfg *config.Config) error {
	candidate, previous, err := a.buildCandidate(cfg)
	if err != nil {
		return err
	}
	return a.activateCandidate(candidate, previous)
}

func (a *App) buildCandidate(cfg *config.Config) (*runtime, *runtime, error) {
	groups, err := loadToolGroups(a.ctx, a.store)
	if err != nil {
		return nil, nil, err
	}
	return a.buildCandidateWithGroups(cfg, groups)
}

func (a *App) buildCandidateWithGroups(cfg *config.Config, groups []httptool.GroupConfig) (*runtime, *runtime, error) {
	candidateParent, cancelCandidate := context.WithCancel(a.ctx)
	a.candidateMu.Lock()
	if a.stopping.Load() {
		a.candidateMu.Unlock()
		cancelCandidate()
		return nil, nil, fmt.Errorf("service is shutting down")
	}
	a.candidateCancel = cancelCandidate
	a.candidateMu.Unlock()

	candidate, err := newRuntimeWithGroups(candidateParent, cfg, groups, a.logger, true)
	if err != nil {
		a.clearReloadCandidate()
		cancelCandidate()
		return nil, nil, err
	}
	candidate.parentCancel = cancelCandidate
	a.clearReloadCandidate()
	if a.stopping.Load() {
		candidate.close()
		return nil, nil, fmt.Errorf("service is shutting down")
	}
	previous := a.currentRuntime()
	if previous == nil {
		candidate.close()
		return nil, nil, fmt.Errorf("service is shutting down")
	}
	for _, id := range candidate.manager.SeedUnavailableCatalogs(previous.manager) {
		candidate.hub.ReconcileBackend(id)
	}
	return candidate, previous, nil
}

func loadToolGroups(ctx context.Context, store *configstore.Store) ([]httptool.GroupConfig, error) {
	if store == nil {
		return nil, nil
	}
	records, err := store.ListToolGroups(ctx)
	if err != nil {
		return nil, err
	}
	groups := make([]httptool.GroupConfig, len(records))
	for index, record := range records {
		groups[index] = record.Config
	}
	return groups, nil
}

func (a *App) activateCandidate(candidate, previous *runtime) error {
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
	candidate.hub.ConfigureApprovals(a.store, &a.limits)
	candidate.hub.ConfigureClientAuthorization(a.store)
	previous.hub.CloseGrantViews()
	previous.hub.RetireApprovals()
	a.limits.Configure(candidate.rateLimitPolicies())
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
	if req.URL.Path == rt.cfg.MCPPath() && req.Method == http.MethodPost {
		req = req.WithContext(diagnostics.Begin(req.Context(), requestID))
		writer := &diagnosticWriter{ResponseWriter: w}
		w = writer
		defer func() {
			status := writer.status
			if status == 0 {
				status = 200
			}
			if reason := writer.Header().Get("MCPHub-Authorization-Error"); reason != "" {
				diagnostics.Outcome(req.Context(), "grant_denied", reason)
			}
			a.requests.Finish(req.Context(), status)
		}()
	}

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
	if a.sso != nil && a.sso.Handles(req.URL.Path) {
		a.sso.ServeHTTP(w, req)
		return
	}

	if a.userAuth != nil && (strings.HasPrefix(req.URL.Path, "/client-auth/") || strings.HasPrefix(req.URL.Path, "/api/v1/client-") || strings.HasPrefix(req.URL.Path, "/api/v1/broker-sessions/")) {
		a.serveClientAuthorization(w, req, rt)
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
