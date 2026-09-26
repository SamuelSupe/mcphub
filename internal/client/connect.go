package client

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type ConnectOptions struct {
	clientID, grantID string
	Input             io.ReadCloser
	Output            io.WriteCloser
	HTTPClient        *http.Client
}

type pendingCall struct {
	cancel  context.CancelFunc
	method  string
	version string
}

type bridge struct {
	local, remote mcp.Connection
	mu            sync.Mutex
	protocol      string
	pending       map[jsonrpc.ID]pendingCall
	writeMu       sync.Mutex
}

func connectorHTTPClient(ctx context.Context, store *Store, name string, opts ConnectOptions) (*credentials, *http.Client, error) {
	credentials, err := bindCredentials(ctx, store, name, opts.HTTPClient)
	if err != nil {
		return nil, nil, err
	}
	if credentials.kind == "admin" {
		return nil, nil, errors.New("administrator profiles cannot be used for MCP connections; log in to the MCP endpoint with a separate profile")
	}
	if _, err := credentials.token(ctx, ""); err != nil {
		return nil, nil, err
	}
	httpConnection := httpClient(opts.HTTPClient, 0)
	base := httpConnection.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	transport := &authenticatedTransport{base: base, credentials: credentials}
	if opts.clientID != "" {
		transport.grant = func(ctx context.Context) (string, error) {
			var secret string
			err := store.locked(ctx, name, func() error {
				p, err := store.load(name)
				if err != nil {
					return err
				}
				if p.Session != credentials.session || p.Broker == nil {
					return ErrProfileChanged
				}
				entry, ok := p.Broker.Clients[opts.clientID]
				if !ok || entry.Grant.GrantID != opts.grantID {
					return ErrProfileChanged
				}
				secret = entry.Credential
				return nil
			})
			return secret, err
		}
	}
	httpConnection.Transport = transport
	return credentials, httpConnection, nil
}

func Connect(ctx context.Context, store *Store, name string, opts ConnectOptions) error {
	if name == "" {
		name = "default"
	}
	credentials, httpConnection, err := connectorHTTPClient(ctx, store, name, opts)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var localTransport mcp.Transport = &mcp.StdioTransport{}
	if opts.Input != nil || opts.Output != nil {
		if opts.Input == nil || opts.Output == nil {
			return errors.New("both connector input and output are required")
		}
		localTransport = &mcp.IOTransport{Reader: opts.Input, Writer: opts.Output}
	}
	local, err := localTransport.Connect(ctx)
	if err != nil {
		return err
	}
	defer local.Close()
	remote, err := (&mcp.StreamableClientTransport{Endpoint: credentials.endpoint, HTTPClient: httpConnection, DisableStandaloneSSE: true, MaxRetries: -1}).Connect(ctx)
	if err != nil {
		return err
	}
	defer remote.Close()
	b := &bridge{local: local, remote: remote, pending: make(map[jsonrpc.ID]pendingCall)}
	return b.run(ctx, cancel)
}

func (b *bridge) run(ctx context.Context, cancel context.CancelFunc) error {
	failures := make(chan error, 1)
	fail := func(err error) {
		select {
		case failures <- err:
		default:
		}
	}
	var workers sync.WaitGroup
	// Calls remain in pending until their response (including subscription streams).
	// The separate write bound also limits fire-and-forget notifications.
	writes := make(chan struct{}, 128)
	workers.Go(func() {
		for {
			msg, err := b.remote.Read(ctx)
			if err != nil {
				fail(err)
				return
			}
			if response, ok := msg.(*jsonrpc.Response); ok {
				b.mu.Lock()
				call, exists := b.pending[response.ID]
				if exists && call.method == "initialize" && response.Error == nil {
					var result struct {
						ProtocolVersion string `json:"protocolVersion"`
					}
					if json.Unmarshal(response.Result, &result) == nil {
						b.protocol = result.ProtocolVersion
					}
				}
				if exists && call.method == "server/discover" && response.Error == nil {
					b.protocol = call.version
				}
				delete(b.pending, response.ID)
				b.mu.Unlock()
				if exists {
					call.cancel()
				}
			}
			if err := b.output(ctx, msg); err != nil {
				fail(err)
				return
			}
		}
	})
	workers.Go(func() {
		for {
			msg, err := b.local.Read(ctx)
			if err != nil {
				fail(err)
				return
			}
			if ctx.Err() != nil {
				return
			}
			req, isRequest := msg.(*jsonrpc.Request)
			b.mu.Lock()
			version := b.protocol
			b.mu.Unlock()
			meta := requestMetadata{version: version}
			if isRequest {
				meta = metadataFor(req, version)
				if req.Method == "notifications/cancelled" {
					if version := b.cancelRequest(req); version != "" {
						meta.version = version
					}
					if meta.version >= "2026-07-28" {
						continue
					}
				}
			}
			if isRequest && !req.IsCall() && req.Method == "notifications/initialized" {
				// Legacy clients can immediately send calls after this notification;
				// finish the handshake before letting those calls reach the server.
				if err := b.remote.Write(context.WithValue(ctx, requestMetadataKey{}, meta), msg); err != nil {
					fail(err)
					return
				}
				continue
			}
			callCtx, callCancel := context.WithCancel(context.WithValue(ctx, requestMetadataKey{}, meta))
			if isRequest && req.IsCall() {
				b.mu.Lock()
				_, duplicate := b.pending[req.ID]
				full := len(b.pending) >= 128
				if !duplicate && !full {
					b.pending[req.ID] = pendingCall{cancel: callCancel, method: req.Method, version: meta.version}
				}
				b.mu.Unlock()
				if duplicate || full {
					callCancel()
					if duplicate {
						fail(errors.New("duplicate in-flight request ID"))
						return
					}
					if err := b.output(ctx, &jsonrpc.Response{ID: req.ID, Error: &jsonrpc.Error{Code: -32000, Message: "too many in-flight MCP requests"}}); err != nil {
						fail(err)
						return
					}
					continue
				}
			}
			select {
			case writes <- struct{}{}:
			default:
				callCancel()
				fail(errors.New("too many concurrent MCP writes"))
				return
			}
			workers.Go(func() {
				defer func() { <-writes }()
				if !isRequest || !req.IsCall() {
					defer callCancel()
				}
				err := b.remote.Write(callCtx, msg)
				if err == nil {
					return
				}
				if isRequest && req.IsCall() {
					b.mu.Lock()
					delete(b.pending, req.ID)
					b.mu.Unlock()
					if callCtx.Err() == nil {
						wire := &jsonrpc.Error{Code: -32000, Message: publicError(err).Error()}
						if outputErr := b.output(ctx, &jsonrpc.Response{ID: req.ID, Error: wire}); outputErr != nil {
							fail(outputErr)
						}
					}
					callCancel()
				}
				if credentialError(err) {
					fail(publicError(err))
				}
			})
		}
	})
	var result error
	select {
	case result = <-failures:
	case <-ctx.Done():
		result = ctx.Err()
	}
	cancel()
	b.mu.Lock()
	for _, call := range b.pending {
		call.cancel()
	}
	b.mu.Unlock()
	b.remote.Close()
	b.local.Close()
	workers.Wait()
	if errors.Is(result, io.EOF) || errors.Is(result, context.Canceled) {
		return nil
	}
	return publicError(result)
}

func (b *bridge) output(ctx context.Context, msg jsonrpc.Message) error {
	b.writeMu.Lock()
	defer b.writeMu.Unlock()
	return b.local.Write(ctx, msg)
}

func (b *bridge) cancelRequest(req *jsonrpc.Request) string {
	var params struct {
		RequestID any `json:"requestId"`
	}
	if json.Unmarshal(req.Params, &params) != nil {
		return ""
	}
	id, err := jsonrpc.MakeID(params.RequestID)
	if err != nil {
		return ""
	}
	b.mu.Lock()
	call, ok := b.pending[id]
	delete(b.pending, id)
	b.mu.Unlock()
	if ok {
		call.cancel()
	}
	return call.version
}

func metadataFor(req *jsonrpc.Request, fallback string) requestMetadata {
	var params struct {
		ProtocolVersion string `json:"protocolVersion"`
		Name            string `json:"name"`
		URI             string `json:"uri"`
		Meta            struct {
			Version string `json:"io.modelcontextprotocol/protocolVersion"`
		} `json:"_meta"`
	}
	_ = json.Unmarshal(req.Params, &params)
	meta := requestMetadata{version: fallback, method: req.Method}
	if params.Meta.Version != "" {
		meta.version = params.Meta.Version
	}
	if req.Method == "initialize" && params.ProtocolVersion != "" {
		meta.version = params.ProtocolVersion
	}
	switch req.Method {
	case "tools/call", "prompts/get":
		meta.name = params.Name
	case "resources/read":
		meta.name = params.URI
	}
	return meta
}
