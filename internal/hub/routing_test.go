package hub

import (
	"context"
	"encoding/base64"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/SamuelSupe/mcphub/internal/backend"
	"github.com/SamuelSupe/mcphub/internal/config"
)

func TestExposeNameNamespacesAndTruncatesDeterministically(t *testing.T) {
	short, err := exposeName("github", "search")
	if err != nil {
		t.Fatalf("expose short name: %v", err)
	}
	if short != "github.search" {
		t.Fatalf("short exposed name = %q", short)
	}

	original := strings.Repeat("a", 128)
	first, err := exposeName("github", original)
	if err != nil {
		t.Fatalf("expose long name: %v", err)
	}
	second, err := exposeName("github", original)
	if err != nil {
		t.Fatalf("expose long name again: %v", err)
	}
	if first != second {
		t.Fatalf("long name mapping is not deterministic: %q != %q", first, second)
	}
	if len(first) != 128 || !strings.HasPrefix(first, "github.") {
		t.Fatalf("long exposed name = %q (length %d)", first, len(first))
	}
	if !strings.HasSuffix(first, ".6836cf13bac4") {
		t.Fatalf("long exposed name has unexpected hash suffix: %q", first)
	}
}

func TestResourceRouteRoundTrip(t *testing.T) {
	original := "https://api.example.com/files/a b?version=1#section"
	exposed := encodeResource("alpha", original)
	backendID, decoded, ok := decodeResource(exposed)
	if !ok || backendID != "alpha" || decoded != original {
		t.Fatalf("decodeResource(%q) = (%q, %q, %v)", exposed, backendID, decoded, ok)
	}

	for _, invalid := range []string{
		"https://alpha/r/value",
		"mcphub://alpha/r/invalid!",
		"mcphub://alpha/r/YQ/extra",
		"mcphub://alpha/r/YQ?variant=secret",
		"mcphub://alpha/r/YQ#fragment",
	} {
		if _, _, ok := decodeResource(invalid); ok {
			t.Fatalf("decodeResource accepted invalid route %q", invalid)
		}
	}
}

func TestUppercaseBackendIDsUseLowercaseResourceAuthorities(t *testing.T) {
	const backendID = "Alpha_1"
	original := "HTTPS://Api.Example.com/files/a b?version=1#Section"
	exposed := encodeResource(backendID, original)
	if !strings.HasPrefix(exposed, "mcphub://alpha_1/r/") {
		t.Fatalf("resource authority = %q, want lowercase", exposed)
	}
	decodedBackend, decodedOriginal, ok := decodeResource(exposed)
	if !ok || decodedBackend != "alpha_1" || decodedOriginal != original {
		t.Fatalf("decodeResource(%q) = (%q, %q, %v)", exposed, decodedBackend, decodedOriginal, ok)
	}

	if got, want := issuedResourceTemplate(backendID), "mcphub://alpha_1/r/{resource}"; got != want {
		t.Fatalf("issued resource template = %q, want %q", got, want)
	}
	route, err := newTemplateRoute(backendID, "https://api.example.com/files/{id}")
	if err != nil {
		t.Fatalf("newTemplateRoute: %v", err)
	}
	if !strings.HasPrefix(route.exposed, "mcphub://alpha_1/t/") || route.backendID != backendID {
		t.Fatalf("template route = %#v, want lowercase authority and canonical backend ID", route)
	}
}

func TestResourceSubscriptionOnlyResolvesPublishedConcreteResources(t *testing.T) {
	listed := encodeResource("alpha", "memory://listed")
	unlisted := encodeResource("alpha", "memory://unlisted")
	v := &view{
		hub:            &Hub{issuedResources: make(map[string]*issuedResourceSet)},
		allowed:        map[string]struct{}{"alpha": {}},
		byHost:         map[string]string{"alpha": "alpha"},
		resources:      map[string]string{listed: "fingerprint"},
		templateRoutes: make(map[string]*templateRoute),
	}

	backendID, original, ok := v.resolveResource(listed, true)
	if !ok || backendID != "alpha" || original != "memory://listed" {
		t.Fatalf("listed resource resolved as (%q, %q, %v)", backendID, original, ok)
	}
	if _, _, ok := v.resolveResource(unlisted, true); ok {
		t.Fatal("unlisted concrete resource was accepted for subscription")
	}
	if backendID, original, ok := v.resolveResource(unlisted, false); !ok || backendID != "alpha" || original != "memory://unlisted" {
		t.Fatalf("unsubscribe cleanup route = (%q, %q, %v)", backendID, original, ok)
	}
}

func TestListedUppercaseBackendResourceResolvesFromLowercaseAuthority(t *testing.T) {
	const backendID = "Alpha_1"
	const original = "memory://listed"
	listed := encodeResource(backendID, original)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := &config.Config{
		Server:   config.ServerConfig{PageSize: 10, RequestTimeout: config.Duration{Duration: time.Second}},
		Backends: []config.BackendConfig{{ID: backendID}},
	}
	manager := backend.NewManager(cfg, logger, nil, nil)
	h := New(cfg, manager, logger)
	v := newView(h, []string{backendID}, nil)
	t.Cleanup(func() {
		v.close()
		manager.Close()
	})
	v.resources[listed] = "fingerprint"
	resolvedBackend, resolvedOriginal, ok := v.resolveResource(listed, true)
	if !ok || resolvedBackend != backendID || resolvedOriginal != original {
		t.Fatalf("listed uppercase backend resource resolved as (%q, %q, %v), want (%q, %q, true)", resolvedBackend, resolvedOriginal, ok, backendID, original)
	}
}

func TestUnpairedUnsubscribeDoesNotTouchSharedBackendReference(t *testing.T) {
	v := &view{
		allowed:         map[string]struct{}{"alpha": {}},
		templateRoutes:  make(map[string]*templateRoute),
		subscriptions:   make(map[backendSubscription]map[string]int),
		bySession:       make(map[*mcp.ServerSession]map[sessionSubscription]struct{}),
		watchedSessions: make(map[*mcp.ServerSession]struct{}),
	}
	err := v.unsubscribe(context.Background(), &mcp.UnsubscribeRequest{
		Session: &mcp.ServerSession{},
		Params:  &mcp.UnsubscribeParams{URI: encodeResource("alpha", "memory://shared")},
	})
	if err != nil {
		t.Fatalf("unpaired unsubscribe: %v", err)
	}
}

func TestResourceCapabilityLogRedactsURIPayloads(t *testing.T) {
	original := "https://api.example.com/report?access_token=super-secret"
	concrete := encodeResource("alpha", original)
	logged := resourceCapabilityForLog(concrete)
	if !strings.HasPrefix(logged, "mcphub://alpha/r/") || logged == concrete {
		t.Fatalf("logged concrete resource = %q", logged)
	}
	if strings.Contains(logged, original) || strings.Contains(logged, base64.RawURLEncoding.EncodeToString([]byte(original))) {
		t.Fatalf("logged concrete resource exposes payload: %q", logged)
	}

	route, err := newTemplateRoute("crm", "https://crm.example.com/customers/{customer}{?access_token}")
	if err != nil {
		t.Fatalf("new template route: %v", err)
	}
	base := strings.Split(route.exposed, "{?")[0]
	if got := resourceCapabilityForLog(base + "?customer=c-1&access_token=super-secret"); got != base {
		t.Fatalf("logged expanded template = %q, want %q", got, base)
	}
	if got := resourceCapabilityForLog(route.exposed); got != base {
		t.Fatalf("logged template reference = %q, want %q", got, base)
	}
}

func TestCapabilityLogRejectsParameterShapedNames(t *testing.T) {
	if got := capabilityForParams(&mcp.CallToolParamsRaw{Name: "alpha.lookup?access_token=super-secret"}); got != "" {
		t.Fatalf("logged invalid tool name = %q", got)
	}
	if got := capabilityForParams(&mcp.GetPromptParams{Name: strings.Repeat("x", 129)}); got != "" {
		t.Fatalf("logged overlong prompt name = %q", got)
	}
	if got := capabilityForParams(&mcp.CallToolParamsRaw{Name: "alpha.lookup"}); got != "alpha.lookup" {
		t.Fatalf("logged valid tool name = %q", got)
	}
}

func TestTemplateRouteRestoresOriginalRFC6570Expansion(t *testing.T) {
	route, err := newTemplateRoute("crm", "https://crm.example.com/customers/{customer}/orders/{order}{?view}")
	if err != nil {
		t.Fatalf("newTemplateRoute: %v", err)
	}
	if !strings.HasPrefix(route.exposed, "mcphub://crm/t/") || !strings.HasSuffix(route.exposed, "{?customer,order,view}") {
		t.Fatalf("unexpected exposed template %q", route.exposed)
	}

	exposedURI := strings.TrimSuffix(route.exposed, "{?customer,order,view}") + "?customer=c-1&order=o%2F2&view=full"
	original, err := route.expand(exposedURI)
	if err != nil {
		t.Fatalf("expand exposed URI: %v", err)
	}
	if want := "https://crm.example.com/customers/c-1/orders/o%2F2?view=full"; original != want {
		t.Fatalf("expanded original = %q, want %q", original, want)
	}
}
