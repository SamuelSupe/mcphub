package backend

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const subscriptionsAcknowledgedMethod = "notifications/subscriptions/acknowledged"

type subscriptionWaiterKey struct {
	session *mcp.ClientSession
	uri     string
}

type subscriptionRouteKey struct {
	session *mcp.ClientSession
	id      string
}

func (c *Client) Subscribe(ctx context.Context, uri string) error {
	c.subscriptionMu.Lock()
	defer c.subscriptionMu.Unlock()
	if c.subscriptions[uri] > 0 {
		c.subscriptions[uri]++
		return nil
	}
	session, err := c.currentSession()
	if err != nil {
		return err
	}
	callCtx, cancel := downstreamContext(ctx, c.cfg.RequestTimeout.Duration)
	defer cancel()
	if err := c.subscribeSession(callCtx, session, uri); err != nil {
		c.observeCallError(session, err)
		return err
	}
	c.subscriptions[uri] = 1
	return nil
}

func (c *Client) Unsubscribe(ctx context.Context, uri string) error {
	c.subscriptionMu.Lock()
	defer c.subscriptionMu.Unlock()
	count := c.subscriptions[uri]
	if count == 0 {
		return nil
	}
	if count <= 1 {
		delete(c.subscriptions, uri)
	} else {
		c.subscriptions[uri] = count - 1
		return nil
	}
	session, err := c.currentSession()
	if err != nil {
		return nil
	}
	// A modern subscriptions/listen handler invokes unsubscribe while unwinding
	// a canceled stream. Detach that cancellation so a legacy backend still
	// receives resources/unsubscribe; the backend timeout and session lifecycle
	// keep this cleanup bounded.
	callCtx, cancel := downstreamContext(context.WithoutCancel(ctx), c.cfg.RequestTimeout.Duration)
	defer cancel()
	err = session.Unsubscribe(callCtx, &mcp.UnsubscribeParams{URI: uri})
	c.forgetSubscriptionRoute(session, uri)
	c.observeCallError(session, err)
	return err
}

func (c *Client) restoreSubscriptions(ctx context.Context, session *mcp.ClientSession) error {
	c.subscriptionMu.Lock()
	defer c.subscriptionMu.Unlock()
	for uri := range c.subscriptions {
		callCtx, cancel := downstreamContext(ctx, c.cfg.RequestTimeout.Duration)
		err := c.subscribeSession(callCtx, session, uri)
		cancel()
		if err != nil {
			return fmt.Errorf("restore resource subscription: %w", err)
		}
	}
	return nil
}

func (c *Client) subscribeSession(ctx context.Context, session *mcp.ClientSession, uri string) error {
	initialized := session.InitializeResult()
	if initialized == nil || initialized.ProtocolVersion < "2026-07-28" {
		return session.Subscribe(ctx, &mcp.SubscribeParams{URI: uri})
	}

	key := subscriptionWaiterKey{session: session, uri: uri}
	waiter := make(chan struct{})
	c.subscriptionAckMu.Lock()
	c.subscriptionWaiters[key] = waiter
	c.subscriptionAckMu.Unlock()
	defer c.removeSubscriptionWaiter(key, waiter)

	if err := session.Subscribe(ctx, &mcp.SubscribeParams{URI: uri}); err != nil {
		return err
	}
	select {
	case <-waiter:
		return nil
	case <-ctx.Done():
		// The SDK retains the background subscriptions/listen stream even when
		// the caller's context expires. Remove it so a later reconnect or retry
		// can create a fresh stream and receive a new acknowledgement.
		c.removeSubscriptionWaiter(key, waiter)
		_ = session.Unsubscribe(context.Background(), &mcp.UnsubscribeParams{URI: uri})
		c.forgetSubscriptionRoute(session, uri)
		return fmt.Errorf("await resource subscription acknowledgement: %w", ctx.Err())
	}
}

func (c *Client) subscriptionAcknowledgementMiddleware() mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if method == subscriptionsAcknowledgedMethod {
				params, paramsOK := req.GetParams().(*mcp.SubscriptionsAcknowledgedParams)
				session, sessionOK := req.GetSession().(*mcp.ClientSession)
				if paramsOK && params != nil && sessionOK {
					c.acknowledgeSubscriptions(session, params)
				}
			}
			return next(ctx, method, req)
		}
	}
}

func (c *Client) acknowledgeSubscriptions(session *mcp.ClientSession, params *mcp.SubscriptionsAcknowledgedParams) {
	c.subscriptionAckMu.Lock()
	defer c.subscriptionAckMu.Unlock()
	uris := params.Notifications.ResourceSubscriptions
	acknowledged := make([]string, 0, len(uris))
	for _, uri := range uris {
		key := subscriptionWaiterKey{session: session, uri: uri}
		if waiter := c.subscriptionWaiters[key]; waiter != nil {
			acknowledged = append(acknowledged, uri)
			delete(c.subscriptionWaiters, key)
			close(waiter)
		}
	}
	if id, ok := subscriptionIDKey(params.Meta); ok && len(acknowledged) > 0 {
		c.subscriptionRoutes[subscriptionRouteKey{session: session, id: id}] = slices.Clone(acknowledged)
	}
}

func (c *Client) subscriptionURIsForUpdate(session *mcp.ClientSession, params *mcp.ResourceUpdatedNotificationParams) []string {
	id, ok := subscriptionIDKey(params.Meta)
	if !ok || session == nil {
		return []string{params.URI}
	}
	c.subscriptionAckMu.Lock()
	routes := slices.Clone(c.subscriptionRoutes[subscriptionRouteKey{session: session, id: id}])
	c.subscriptionAckMu.Unlock()
	if len(routes) == 0 {
		return []string{params.URI}
	}
	if slices.Contains(routes, params.URI) {
		return []string{params.URI}
	}
	slices.Sort(routes)
	return slices.Compact(routes)
}

func (c *Client) forgetSubscriptionRoute(session *mcp.ClientSession, uri string) {
	c.subscriptionAckMu.Lock()
	defer c.subscriptionAckMu.Unlock()
	for key, routes := range c.subscriptionRoutes {
		if key.session != session || !slices.Contains(routes, uri) {
			continue
		}
		filtered := routes[:0]
		for _, route := range routes {
			if route != uri {
				filtered = append(filtered, route)
			}
		}
		if len(filtered) == 0 {
			delete(c.subscriptionRoutes, key)
		} else {
			c.subscriptionRoutes[key] = filtered
		}
	}
}

func (c *Client) forgetSubscriptionSession(session *mcp.ClientSession) {
	if session == nil {
		return
	}
	c.subscriptionAckMu.Lock()
	for key := range c.subscriptionRoutes {
		if key.session == session {
			delete(c.subscriptionRoutes, key)
		}
	}
	c.subscriptionAckMu.Unlock()
}

func subscriptionIDKey(meta mcp.Meta) (string, bool) {
	value, ok := meta[mcp.MetaKeySubscriptionID]
	if !ok || value == nil {
		return "", false
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", false
	}
	return string(encoded), true
}

func (c *Client) removeSubscriptionWaiter(key subscriptionWaiterKey, waiter chan struct{}) {
	c.subscriptionAckMu.Lock()
	defer c.subscriptionAckMu.Unlock()
	if c.subscriptionWaiters[key] == waiter {
		delete(c.subscriptionWaiters, key)
	}
}
