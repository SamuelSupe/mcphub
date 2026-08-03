package backend

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type progressRoute struct {
	session *mcp.ServerSession
	token   any
	ctx     context.Context
}

func (c *Client) handleProgress(params *mcp.ProgressNotificationParams) {
	if params == nil {
		return
	}
	key, ok := params.ProgressToken.(string)
	if !ok {
		return
	}
	c.progressMu.Lock()
	route, ok := c.progressRoutes[key]
	c.progressMu.Unlock()
	if !ok || route.session == nil || route.ctx == nil {
		return
	}
	forwarded := *params
	forwarded.ProgressToken = route.token
	if err := route.session.NotifyProgress(route.ctx, &forwarded); err != nil {
		c.logger.Debug("forward progress notification", "error_type", fmt.Sprintf("%T", err))
	}
}

func (c *Client) prepareProgress(ctx context.Context, meta mcp.Meta, upstream *mcp.ServerSession) (mcp.Meta, func()) {
	cloned := downstreamMeta(meta)
	if cloned == nil {
		cloned = make(mcp.Meta)
	}
	original, ok := cloned["progressToken"]
	if !ok || upstream == nil {
		return cloned, func() {}
	}
	key := fmt.Sprintf("mcphub:%s:%d", c.cfg.ID, c.progressSequence.Add(1))
	c.progressMu.Lock()
	c.progressRoutes[key] = progressRoute{session: upstream, token: original, ctx: ctx}
	c.progressMu.Unlock()
	cloned["progressToken"] = key
	return cloned, func() {
		c.progressMu.Lock()
		delete(c.progressRoutes, key)
		c.progressMu.Unlock()
	}
}

func downstreamMeta(meta mcp.Meta) mcp.Meta {
	cloned := maps.Clone(meta)
	delete(cloned, mcp.MetaKeyProtocolVersion)
	delete(cloned, mcp.MetaKeyClientInfo)
	delete(cloned, mcp.MetaKeyClientCapabilities)
	return cloned
}

type withoutValuesContext struct {
	context.Context
}

func (withoutValuesContext) Value(any) any {
	return nil
}

func downstreamContext(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	// The MCP SDK stores the inbound protocol version in context. Reusing that
	// value would override the independently negotiated downstream version.
	return context.WithTimeout(withoutValuesContext{Context: parent}, timeout)
}

func (c *Client) CallTool(ctx context.Context, upstream *mcp.ServerSession, params *mcp.CallToolParams) (*mcp.CallToolResult, error) {
	session, err := c.currentSession()
	if err != nil {
		return nil, err
	}
	p := *params
	meta, cleanup := c.prepareProgress(ctx, params.Meta, upstream)
	p.Meta = meta
	defer cleanup()
	callCtx, cancel := downstreamContext(ctx, c.cfg.RequestTimeout.Duration)
	defer cancel()
	result, err := session.CallTool(callCtx, &p)
	c.observeCallError(session, err)
	return result, err
}

func (c *Client) GetPrompt(ctx context.Context, upstream *mcp.ServerSession, params *mcp.GetPromptParams) (*mcp.GetPromptResult, error) {
	session, err := c.currentSession()
	if err != nil {
		return nil, err
	}
	p := *params
	meta, cleanup := c.prepareProgress(ctx, params.Meta, upstream)
	p.Meta = meta
	defer cleanup()
	callCtx, cancel := downstreamContext(ctx, c.cfg.RequestTimeout.Duration)
	defer cancel()
	result, err := session.GetPrompt(callCtx, &p)
	c.observeCallError(session, err)
	return result, err
}

func (c *Client) ReadResource(ctx context.Context, upstream *mcp.ServerSession, params *mcp.ReadResourceParams) (*mcp.ReadResourceResult, error) {
	session, err := c.currentSession()
	if err != nil {
		return nil, err
	}
	p := *params
	meta, cleanup := c.prepareProgress(ctx, params.Meta, upstream)
	p.Meta = meta
	defer cleanup()
	callCtx, cancel := downstreamContext(ctx, c.cfg.RequestTimeout.Duration)
	defer cancel()
	result, err := session.ReadResource(callCtx, &p)
	c.observeCallError(session, err)
	return result, err
}

func (c *Client) Complete(ctx context.Context, upstream *mcp.ServerSession, params *mcp.CompleteParams) (*mcp.CompleteResult, error) {
	session, err := c.currentSession()
	if err != nil {
		return nil, err
	}
	p := *params
	meta, cleanup := c.prepareProgress(ctx, params.Meta, upstream)
	p.Meta = meta
	defer cleanup()
	if params.Ref != nil {
		ref := *params.Ref
		p.Ref = &ref
	}
	callCtx, cancel := downstreamContext(ctx, c.cfg.RequestTimeout.Duration)
	defer cancel()
	result, err := session.Complete(callCtx, &p)
	c.observeCallError(session, err)
	return result, err
}

func (c *Client) observeCallError(session *mcp.ClientSession, err error) {
	if err != nil && errors.Is(err, mcp.ErrConnectionClosed) {
		c.signalDisconnected(session)
	}
}
