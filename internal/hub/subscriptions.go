package hub

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func (v *view) subscribe(ctx context.Context, req *mcp.SubscribeRequest) error {
	if req == nil || req.Params == nil || req.Session == nil {
		return fmt.Errorf("resource subscription requires a session and URI")
	}
	backendID, original, ok := v.resolveResource(req.Params.URI, true)
	if !ok {
		return fmt.Errorf("unknown resource %q", req.Params.URI)
	}
	backendKey := backendSubscription{backendID: backendID, original: original}
	sessionKey := sessionSubscription{backend: backendKey, exposed: req.Params.URI}

	v.subscriptionMu.Lock()
	if _, subscribed := v.bySession[req.Session][sessionKey]; subscribed {
		v.subscriptionMu.Unlock()
		return nil
	}
	client, _ := v.hub.manager.Client(backendID)
	if err := client.Subscribe(ctx, original); err != nil {
		v.subscriptionMu.Unlock()
		return publicBackendError(backendID, err)
	}
	byExposed := v.subscriptions[backendKey]
	if byExposed == nil {
		byExposed = make(map[string]int)
		v.subscriptions[backendKey] = byExposed
	}
	byExposed[req.Params.URI]++
	if v.bySession[req.Session] == nil {
		v.bySession[req.Session] = make(map[sessionSubscription]struct{})
	}
	v.bySession[req.Session][sessionKey] = struct{}{}
	_, watching := v.watchedSessions[req.Session]
	if !watching {
		v.watchedSessions[req.Session] = struct{}{}
	}
	v.subscriptionMu.Unlock()
	if !watching {
		go v.watchSession(req.Session)
	}
	return nil
}

func (v *view) unsubscribe(ctx context.Context, req *mcp.UnsubscribeRequest) error {
	if req == nil || req.Params == nil || req.Session == nil {
		return nil
	}
	v.subscriptionMu.Lock()
	bySession := v.bySession[req.Session]
	var sessionKey sessionSubscription
	found := false
	for subscription := range bySession {
		if subscription.exposed == req.Params.URI {
			sessionKey = subscription
			found = true
			break
		}
	}
	if !found {
		v.subscriptionMu.Unlock()
		return nil
	}
	delete(bySession, sessionKey)
	if len(bySession) == 0 {
		delete(v.bySession, req.Session)
	}
	if byExposed := v.subscriptions[sessionKey.backend]; byExposed != nil {
		if byExposed[req.Params.URI] <= 1 {
			delete(byExposed, req.Params.URI)
		} else {
			byExposed[req.Params.URI]--
		}
		if len(byExposed) == 0 {
			delete(v.subscriptions, sessionKey.backend)
		}
	}
	v.subscriptionMu.Unlock()
	client, _ := v.hub.manager.Client(sessionKey.backend.backendID)
	if err := client.Unsubscribe(ctx, sessionKey.backend.original); err != nil {
		return publicBackendError(sessionKey.backend.backendID, err)
	}
	return nil
}

func (v *view) watchSession(session *mcp.ServerSession) {
	_ = session.Wait()

	v.subscriptionMu.Lock()
	subscriptions := v.bySession[session]
	delete(v.bySession, session)
	delete(v.watchedSessions, session)
	for subscription := range subscriptions {
		byExposed := v.subscriptions[subscription.backend]
		if byExposed[subscription.exposed] <= 1 {
			delete(byExposed, subscription.exposed)
		} else {
			byExposed[subscription.exposed]--
		}
		if len(byExposed) == 0 {
			delete(v.subscriptions, subscription.backend)
		}
	}
	v.subscriptionMu.Unlock()

	for subscription := range subscriptions {
		if client, ok := v.hub.manager.Client(subscription.backend.backendID); ok {
			_ = client.Unsubscribe(context.Background(), subscription.backend.original)
		}
	}
}

func (v *view) resolveResource(exposed string, requireListedConcrete bool) (string, string, bool) {
	if host, original, ok := decodeResource(exposed); ok {
		backendID, allowed := v.backendForHost(host)
		if !allowed {
			return "", "", false
		}
		if requireListedConcrete {
			v.reconcileMu.RLock()
			_, listed := v.resources[exposed]
			v.reconcileMu.RUnlock()
			if !listed && !v.hub.wasResourceIssued(backendID, original) {
				return "", "", false
			}
		}
		return backendID, original, true
	}
	v.routesMu.RLock()
	defer v.routesMu.RUnlock()
	for _, route := range v.templateRoutes {
		if original, err := route.expand(exposed); err == nil {
			return route.backendID, original, true
		}
	}
	return "", "", false
}

func (v *view) resourceUpdated(backendID, original string) {
	key := backendSubscription{backendID: backendID, original: original}
	v.subscriptionMu.Lock()
	var exposed []string
	for value := range v.subscriptions[key] {
		exposed = append(exposed, value)
	}
	v.subscriptionMu.Unlock()
	for _, uri := range exposed {
		_ = v.server.ResourceUpdated(context.Background(), &mcp.ResourceUpdatedNotificationParams{URI: uri})
	}
}
