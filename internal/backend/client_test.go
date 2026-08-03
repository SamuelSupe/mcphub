package backend

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/SamuelSupe/mcphub/internal/config"
)

func TestCatalogPublicationWarnsOncePerUnmatchedRuleAndGeneration(t *testing.T) {
	var logs bytes.Buffer
	client := NewClient(
		config.BackendConfig{
			ID: "alpha",
			ToolRules: []config.ToolRule{
				{Match: "echo", RequiredScopes: []string{"mcp:echo"}},
				{Match: "delete_*", RequiredScopes: []string{"mcp:dangerous"}},
			},
		},
		time.Minute,
		slog.New(slog.NewTextHandler(&logs, nil)),
		nil,
		nil,
	)
	t.Cleanup(client.Close)

	client.publishCatalog(&Catalog{Tools: []*mcp.Tool{{Name: "echo"}}})
	if got := strings.Count(logs.String(), "tool rule matches no catalog tool"); got != 1 {
		t.Fatalf("first catalog unmatched warnings = %d, want 1; logs=%q", got, logs.String())
	}
	if !strings.Contains(logs.String(), "match=delete_*") || strings.Contains(logs.String(), "match=echo") {
		t.Fatalf("first catalog warning fields = %q", logs.String())
	}

	client.publishCatalog(&Catalog{Tools: []*mcp.Tool{{Name: "echo"}}})
	if got := strings.Count(logs.String(), "tool rule matches no catalog tool"); got != 2 {
		t.Fatalf("two catalog generations unmatched warnings = %d, want 2; logs=%q", got, logs.String())
	}
}

func TestDisconnectSignalImmediatelyInvalidatesOnlyCurrentSession(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "backend-test", Version: "1"}, nil)
	httpServer := httptest.NewServer(mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return server },
		&mcp.StreamableHTTPOptions{Stateless: true},
	))
	defer httpServer.Close()

	protocolClient := mcp.NewClient(&mcp.Implementation{Name: "client-test", Version: "1"}, nil)
	session, err := protocolClient.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint:   httpServer.URL,
		MaxRetries: -1,
	}, nil)
	if err != nil {
		t.Fatalf("connect test session: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })

	client := NewClient(
		config.BackendConfig{ID: "alpha"},
		time.Minute,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		nil,
		nil,
	)
	client.mu.Lock()
	client.session = session
	client.mu.Unlock()
	client.ready.Store(true)

	client.signalDisconnected(&mcp.ClientSession{})
	if !client.Ready() {
		t.Fatal("stale session signal invalidated the current session")
	}
	client.signalDisconnected(session)
	if client.Ready() {
		t.Fatal("current session signal did not immediately invalidate readiness")
	}
	select {
	case <-client.changed:
	default:
		t.Fatal("current session disconnect did not wake the reconnect loop")
	}
}

func TestConnectRemainsUnavailableUntilSubscriptionsAreRestored(t *testing.T) {
	var subscribeCalls atomic.Int32
	server := mcp.NewServer(&mcp.Implementation{Name: "backend-test", Version: "1"}, &mcp.ServerOptions{
		SubscribeHandler: func(context.Context, *mcp.SubscribeRequest) error {
			if subscribeCalls.Add(1) == 1 {
				return errors.New("temporary restore failure")
			}
			return nil
		},
		UnsubscribeHandler: func(context.Context, *mcp.UnsubscribeRequest) error { return nil },
	})
	server.AddResource(&mcp.Resource{Name: "resource", URI: "memory://resource"}, func(_ context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: req.Params.URI}}}, nil
	})
	httpServer := httptest.NewServer(mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return server },
		&mcp.StreamableHTTPOptions{Stateless: true},
	))
	t.Cleanup(httpServer.Close)

	client := NewClient(
		config.BackendConfig{
			ID:                "alpha",
			URL:               httpServer.URL,
			AllowInsecureHTTP: true,
			RequestTimeout:    config.Duration{Duration: 500 * time.Millisecond},
		},
		time.Hour,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		nil,
		nil,
	)
	client.subscriptionMu.Lock()
	client.subscriptions["memory://resource"] = 1
	client.subscriptionMu.Unlock()
	t.Cleanup(client.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	if err := client.ConnectOnce(ctx); err == nil {
		t.Fatal("connection reported ready after subscription restoration failed")
	}
	if client.Ready() {
		t.Fatal("client is ready after subscription restoration failed")
	}
	if err := client.ConnectOnce(ctx); err != nil {
		t.Fatalf("reconnect after temporary restoration failure: %v", err)
	}
	if !client.Ready() || subscribeCalls.Load() != 2 {
		t.Fatalf("restored client ready=%v subscribe calls=%d", client.Ready(), subscribeCalls.Load())
	}
}

func TestManagerMissingScopesResolvesLowercaseAuthorityAndAllowedIDsCanonical(t *testing.T) {
	cfg := &config.Config{
		Server: config.ServerConfig{RefreshInterval: config.Duration{Duration: time.Minute}},
		Backends: []config.BackendConfig{{
			ID:             "Alpha",
			RequiredScopes: []string{"mcp:alpha"},
			ToolRules: []config.ToolRule{{
				Match:          "delete_*",
				RequiredScopes: []string{"mcp:alpha:dangerous"},
			}},
		}},
	}
	manager := NewManager(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil)
	t.Cleanup(manager.Close)
	if _, ok := manager.Client("alpha"); ok {
		t.Fatal("Manager.Client accepted lowercase alias; canonical lookup must remain exact")
	}
	if client, ok := manager.Client("Alpha"); !ok || client.ID() != "Alpha" {
		t.Fatalf("Manager.Client canonical lookup = (%v, %v)", client, ok)
	}
	missing, known := manager.MissingScopes("alpha", nil)
	if !known || len(missing) != 1 || missing[0] != "mcp:alpha" {
		t.Fatalf("MissingScopes lowercase authority = (%v, %v), want ([mcp:alpha], true)", missing, known)
	}
	allowed := manager.AllowedIDs([]string{"mcp:alpha"})
	if len(allowed) != 1 || allowed[0] != "Alpha" {
		t.Fatalf("AllowedIDs = %v, want [Alpha]", allowed)
	}
	profileIDs, profileScopes := manager.AllowedProfile([]string{"attacker:controlled", "mcp:alpha", "mcp:alpha:dangerous"})
	if len(profileIDs) != 1 || profileIDs[0] != "Alpha" {
		t.Fatalf("AllowedProfile IDs = %v, want [Alpha]", profileIDs)
	}
	if len(profileScopes) != 1 || profileScopes[0] != "mcp:alpha:dangerous" {
		t.Fatalf("AllowedProfile tool scopes = %v, want only configured granted scope", profileScopes)
	}
}

func TestResourceUpdatedSubscriptionRoutesStaySessionBoundAndForgettable(t *testing.T) {
	const (
		parentURI = "memory://parent"
		childURI  = "memory://parent/child"
	)
	var updates []string
	client := NewClient(
		config.BackendConfig{ID: "alpha"},
		time.Minute,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		nil,
		func(_ string, uri string) { updates = append(updates, uri) },
	)
	session := &mcp.ClientSession{}
	otherSession := &mcp.ClientSession{}

	acknowledge := func(session *mcp.ClientSession, id string, uris ...string) {
		client.acknowledgeSubscriptions(session, &mcp.SubscriptionsAcknowledgedParams{
			Meta: mcp.Meta{mcp.MetaKeySubscriptionID: id},
			Notifications: mcp.NotificationSubscriptions{
				ResourceSubscriptions: uris,
			},
		})
	}
	armWaiter := func(session *mcp.ClientSession, uri string) (subscriptionWaiterKey, chan struct{}) {
		key := subscriptionWaiterKey{session: session, uri: uri}
		waiter := make(chan struct{})
		client.subscriptionAckMu.Lock()
		client.subscriptionWaiters[key] = waiter
		client.subscriptionAckMu.Unlock()
		return key, waiter
	}
	expectUpdate := func(t *testing.T, session *mcp.ClientSession, id, uri, want string) {
		t.Helper()
		before := len(updates)
		client.handleResourceUpdated(context.Background(), &mcp.ResourceUpdatedNotificationRequest{
			Session: session,
			Params: &mcp.ResourceUpdatedNotificationParams{
				Meta: mcp.Meta{mcp.MetaKeySubscriptionID: id},
				URI:  uri,
			},
		})
		if len(updates) != before+1 || updates[len(updates)-1] != want {
			t.Fatalf("resource update (%p, %q, %q) appended %v, want one %q", session, id, uri, updates[before:], want)
		}
	}

	// An acknowledgement that arrives without a live waiter must not create a
	// route that a later update can borrow.
	acknowledge(session, "unsolicited", parentURI)
	expectUpdate(t, session, "unsolicited", childURI, childURI)

	// A timed-out waiter is removed before a late acknowledgement arrives, so
	// that acknowledgement must be ignored for routing purposes as well.
	lateKey, lateWaiter := armWaiter(session, parentURI)
	client.removeSubscriptionWaiter(lateKey, lateWaiter)
	acknowledge(session, "late", parentURI)
	expectUpdate(t, session, "late", childURI, childURI)

	// A live waiter binds the acknowledged subscription ID to its parent URI.
	_, _ = armWaiter(session, parentURI)
	acknowledge(session, "bound", parentURI)
	expectUpdate(t, session, "bound", childURI, parentURI)
	// Exact updates remain exact and are emitted only once.
	expectUpdate(t, session, "bound", parentURI, parentURI)
	// The same ID on another session cannot borrow this route.
	expectUpdate(t, otherSession, "bound", childURI, childURI)
	// An unknown ID on the owning session also falls back to the event URI.
	expectUpdate(t, session, "unknown", childURI, childURI)

	client.forgetSubscriptionRoute(session, parentURI)
	expectUpdate(t, session, "bound", childURI, childURI)

	_, _ = armWaiter(session, parentURI)
	acknowledge(session, "session-bound", parentURI)
	expectUpdate(t, session, "session-bound", childURI, parentURI)
	client.forgetSubscriptionSession(session)
	expectUpdate(t, session, "session-bound", childURI, childURI)
}
