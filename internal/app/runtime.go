package app

import (
	"context"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/SamuelSupe/mcphub/internal/backend"
	"github.com/SamuelSupe/mcphub/internal/config"
	"github.com/SamuelSupe/mcphub/internal/httptool"
	"github.com/SamuelSupe/mcphub/internal/hub"
)

type runtime struct {
	cfg          *config.Config
	manager      *backend.Manager
	hub          *hub.Hub
	mcpHandler   http.Handler
	origins      *http.CrossOriginProtection
	ctx          context.Context
	cancel       context.CancelFunc
	parentCancel context.CancelFunc
	active       sync.WaitGroup
	closeOnce    sync.Once
}

func newRuntime(parent context.Context, cfg *config.Config, logger *slog.Logger, requireReady bool) (*runtime, error) {
	return newRuntimeWithGroups(parent, cfg, nil, logger, requireReady)
}

func newRuntimeWithGroups(parent context.Context, cfg *config.Config, groups []httptool.GroupConfig, logger *slog.Logger, requireReady bool) (*runtime, error) {
	ctx, cancel := context.WithCancel(parent)
	if err := httptool.ValidateGroups(groups, backendIDs(cfg)); err != nil {
		cancel()
		return nil, err
	}
	httpTools, err := httptool.NewManager(ctx, groups, logger)
	if err != nil {
		cancel()
		return nil, err
	}
	var currentHub *hub.Hub
	manager := backend.NewManager(
		cfg,
		logger,
		func(id string) {
			if currentHub != nil {
				currentHub.ReconcileBackend(id)
			}
		},
		func(id, uri string) {
			if currentHub != nil {
				currentHub.ResourceUpdated(id, uri)
			}
		},
	)
	currentHub = hub.NewWithHTTPTools(cfg, manager, httpTools, logger)
	if err := manager.Start(ctx, requireReady); err != nil {
		currentHub.Close()
		cancel()
		return nil, err
	}

	origins := http.NewCrossOriginProtection()
	publicURL, _ := url.Parse(cfg.Server.PublicURL)
	_ = origins.AddTrustedOrigin(publicURL.Scheme + "://" + publicURL.Host)
	for _, origin := range cfg.Server.AllowedOrigins {
		_ = origins.AddTrustedOrigin(origin)
	}

	rt := &runtime{
		cfg: cfg, manager: manager, hub: currentHub,
		origins: origins, ctx: ctx, cancel: cancel,
	}
	rt.mcpHandler = mcp.NewStreamableHTTPHandler(
		currentHub.ServerForRequest,
		&mcp.StreamableHTTPOptions{
			Stateless:                    true,
			MaxRequestBodyBytes:          cfg.Server.MaxRequestBodyBytes,
			PropagateRequestCancellation: true,
			// Reverse proxies commonly preserve the public Host while dialing a
			// loopback listener; the app-level origin policy provides this guard.
			DisableLocalhostProtection: true,
		},
	)
	return rt, nil
}

func backendIDs(cfg *config.Config) []string {
	ids := make([]string, 0, len(cfg.Backends))
	for _, value := range cfg.Backends {
		ids = append(ids, value.ID)
	}
	return ids
}

func (r *runtime) ready() bool {
	return r.manager.Ready()
}

func (r *runtime) allScopes() []string {
	values := append(r.cfg.AllScopes(), r.hub.HTTPToolScopes()...)
	slices.Sort(values)
	return slices.Compact(values)
}

func (r *runtime) close() {
	r.closeOnce.Do(func() {
		// Stop requests that outlived the drain before enumerating sessions. An
		// in-flight request can otherwise register a session immediately after
		// Hub.Close's snapshot and escape generation shutdown.
		r.cancel()
		r.hub.Close()
		r.manager.Close()
		if r.parentCancel != nil {
			r.parentCancel()
		}
	})
}

func (r *runtime) drain(timeout time.Duration) {
	done := make(chan struct{})
	go func() {
		r.active.Wait()
		close(done)
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
	}
	r.close()
}
