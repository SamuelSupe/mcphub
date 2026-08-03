package backend

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/SamuelSupe/mcphub/internal/config"
	"github.com/SamuelSupe/mcphub/internal/mcpcompat"
	"github.com/SamuelSupe/mcphub/internal/version"
)

type Client struct {
	cfg               config.BackendConfig
	refreshMaximum    time.Duration
	logger            *slog.Logger
	onCatalogChanged  func(string)
	onResourceUpdated func(string, string)

	mu            sync.RWMutex
	session       *mcp.ClientSession
	sessionCancel context.CancelFunc
	closed        bool

	catalog atomic.Pointer[Catalog]
	ready   atomic.Bool

	changed chan struct{}

	progressSequence atomic.Uint64
	progressMu       sync.Mutex
	progressRoutes   map[string]progressRoute

	subscriptionMu sync.Mutex
	subscriptions  map[string]int

	subscriptionAckMu   sync.Mutex
	subscriptionWaiters map[subscriptionWaiterKey]chan struct{}
	subscriptionRoutes  map[subscriptionRouteKey][]string
}

func NewClient(
	cfg config.BackendConfig,
	refreshMaximum time.Duration,
	logger *slog.Logger,
	onCatalogChanged func(string),
	onResourceUpdated func(string, string),
) *Client {
	return &Client{
		cfg:                 cfg,
		refreshMaximum:      refreshMaximum,
		logger:              logger.With("backend", cfg.ID),
		onCatalogChanged:    onCatalogChanged,
		onResourceUpdated:   onResourceUpdated,
		changed:             make(chan struct{}, 1),
		progressRoutes:      make(map[string]progressRoute),
		subscriptions:       make(map[string]int),
		subscriptionWaiters: make(map[subscriptionWaiterKey]chan struct{}),
		subscriptionRoutes:  make(map[subscriptionRouteKey][]string),
	}
}

func (c *Client) ID() string {
	return c.cfg.ID
}

func (c *Client) Config() config.BackendConfig {
	return c.cfg
}

func (c *Client) Required() bool {
	return c.cfg.Required
}

func (c *Client) Ready() bool {
	return c.ready.Load()
}

func (c *Client) Catalog() *Catalog {
	return c.catalog.Load()
}

func (c *Client) seedCatalog(catalog *Catalog) bool {
	if catalog == nil || c.Catalog() != nil {
		return false
	}
	return c.catalog.CompareAndSwap(nil, catalog)
}

func (c *Client) ConnectOnce(ctx context.Context) error {
	c.mu.RLock()
	closed := c.closed
	c.mu.RUnlock()
	if closed {
		return fmt.Errorf("backend %s is closed", c.cfg.ID)
	}

	sessionCtx, cancelSession := context.WithCancel(ctx)
	handedOffSession := false
	defer func() {
		if !handedOffSession {
			cancelSession()
		}
	}()

	httpClient, err := newHTTPClient(sessionCtx, c.cfg)
	if err != nil {
		return err
	}
	httpClient.Transport = &progressRoundTripper{
		base:   &mcpcompat.RoundTripper{Base: httpClient.Transport},
		handle: c.handleProgress,
	}
	client := mcp.NewClient(
		&mcp.Implementation{Name: "mcphub", Version: version.Value},
		&mcp.ClientOptions{
			Capabilities: &mcp.ClientCapabilities{},
			ToolListChangedHandler: func(context.Context, *mcp.ToolListChangedRequest) {
				c.signalChanged()
			},
			PromptListChangedHandler: func(context.Context, *mcp.PromptListChangedRequest) {
				c.signalChanged()
			},
			ResourceListChangedHandler: func(context.Context, *mcp.ResourceListChangedRequest) {
				c.signalChanged()
			},
			ResourceUpdatedHandler: c.handleResourceUpdated,
			MultiRoundTrip:         &mcp.MultiRoundTripOptions{Disabled: true},
		},
	)
	client.AddReceivingMiddleware(c.subscriptionAcknowledgementMiddleware())
	transport := &mcp.StreamableClientTransport{
		Endpoint:   c.cfg.URL,
		HTTPClient: httpClient,
		MaxRetries: -1,
	}
	connectCtx, cancel := context.WithTimeout(ctx, c.cfg.RequestTimeout.Duration)
	session, err := client.Connect(connectCtx, transport, nil)
	cancel()
	if err != nil {
		return fmt.Errorf("connect MCP session: %w", err)
	}

	discoveryCtx, cancel := context.WithTimeout(ctx, c.cfg.RequestTimeout.Duration)
	catalog, err := discoverCatalog(discoveryCtx, session, c.refreshMaximum)
	cancel()
	if err != nil {
		cancelSession()
		_ = session.Close()
		return err
	}

	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		cancelSession()
		_ = session.Close()
		return fmt.Errorf("backend %s is closed", c.cfg.ID)
	}
	previous := c.session
	previousCancel := c.sessionCancel
	c.session = session
	c.sessionCancel = cancelSession
	handedOffSession = true
	c.mu.Unlock()
	if previousCancel != nil {
		previousCancel()
	}
	if previous != nil && previous != session {
		c.forgetSubscriptionSession(previous)
		_ = previous.Close()
	}
	if err := c.restoreSubscriptions(ctx, session); err != nil {
		c.markDisconnected(session)
		return err
	}
	c.mu.Lock()
	if c.closed || c.session != session {
		c.mu.Unlock()
		cancelSession()
		_ = session.Close()
		return fmt.Errorf("backend %s is closed", c.cfg.ID)
	}
	c.catalog.Store(catalog)
	c.ready.Store(true)
	c.mu.Unlock()
	c.logger.Info("backend ready",
		"tools", len(catalog.Tools),
		"prompts", len(catalog.Prompts),
		"resources", len(catalog.Resources),
		"resource_templates", len(catalog.ResourceTemplates),
	)
	if c.onCatalogChanged != nil {
		c.onCatalogChanged(c.cfg.ID)
	}
	go func() {
		_ = session.Wait()
		c.signalDisconnected(session)
	}()
	return nil
}

func (c *Client) Run(ctx context.Context) {
	backoff := time.Second
	for {
		if !c.Ready() {
			if err := c.ConnectOnce(ctx); err != nil {
				c.logger.Warn("backend connection failed", "error_type", fmt.Sprintf("%T", err), "retry_in", backoff)
				if !waitFor(ctx, backoff, c.changed) {
					c.Close()
					return
				}
				backoff = min(backoff*2, 30*time.Second)
				continue
			}
			backoff = time.Second
		}

		delay := c.refreshMaximum
		if catalog := c.Catalog(); catalog != nil {
			delay = catalog.RefreshAfter
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			c.Close()
			return
		case <-timer.C:
			c.refresh(ctx)
		case <-c.changed:
			timer.Stop()
			c.refresh(ctx)
		}
	}
}

func (c *Client) refresh(ctx context.Context) {
	session, err := c.currentSession()
	if err != nil {
		return
	}
	refreshCtx, cancel := context.WithTimeout(ctx, c.cfg.RequestTimeout.Duration)
	catalog, err := discoverCatalog(refreshCtx, session, c.refreshMaximum)
	cancel()
	if err != nil {
		c.logger.Warn("catalog refresh failed", "error_type", fmt.Sprintf("%T", err))
		c.markDisconnected(session)
		return
	}
	c.catalog.Store(catalog)
	if c.onCatalogChanged != nil {
		c.onCatalogChanged(c.cfg.ID)
	}
}

func (c *Client) Close() {
	c.mu.Lock()
	session := c.session
	cancelSession := c.sessionCancel
	c.session = nil
	c.sessionCancel = nil
	c.closed = true
	c.mu.Unlock()
	c.ready.Store(false)
	c.forgetSubscriptionSession(session)
	if cancelSession != nil {
		cancelSession()
	}
	if session != nil {
		_ = session.Close()
	}
}

func (c *Client) currentSession() (*mcp.ClientSession, error) {
	c.mu.RLock()
	session := c.session
	c.mu.RUnlock()
	if session == nil || !c.Ready() {
		return nil, fmt.Errorf("backend %s is unavailable", c.cfg.ID)
	}
	return session, nil
}

func (c *Client) markDisconnected(session *mcp.ClientSession) bool {
	c.mu.Lock()
	if c.session != session {
		c.mu.Unlock()
		return false
	}
	c.session = nil
	cancelSession := c.sessionCancel
	c.sessionCancel = nil
	c.mu.Unlock()
	c.ready.Store(false)
	c.forgetSubscriptionSession(session)
	if cancelSession != nil {
		cancelSession()
	}
	_ = session.Close()
	c.logger.Warn("backend disconnected; retaining last-known-good catalog")
	return true
}

func (c *Client) signalChanged() {
	select {
	case c.changed <- struct{}{}:
	default:
	}
}

func (c *Client) signalDisconnected(session *mcp.ClientSession) {
	if c.markDisconnected(session) {
		c.signalChanged()
	}
}

func (c *Client) handleResourceUpdated(_ context.Context, req *mcp.ResourceUpdatedNotificationRequest) {
	if req == nil || req.Params == nil || c.onResourceUpdated == nil {
		return
	}
	for _, uri := range c.subscriptionURIsForUpdate(req.Session, req.Params) {
		c.onResourceUpdated(c.cfg.ID, uri)
	}
}

func waitFor(ctx context.Context, delay time.Duration, wake <-chan struct{}) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	case <-wake:
		return true
	}
}
