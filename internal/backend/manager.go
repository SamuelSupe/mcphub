package backend

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"
	"sync"

	"github.com/SamuelSupe/mcphub/internal/config"
)

type Manager struct {
	logger  *slog.Logger
	clients map[string]*Client
	aliases map[string]*Client
	ids     []string
	cancel  context.CancelFunc
}

func NewManager(
	cfg *config.Config,
	logger *slog.Logger,
	onCatalogChanged func(string),
	onResourceUpdated func(string, string),
) *Manager {
	manager := &Manager{
		logger:  logger,
		clients: make(map[string]*Client, len(cfg.Backends)),
		aliases: make(map[string]*Client, len(cfg.Backends)),
		ids:     make([]string, 0, len(cfg.Backends)),
	}
	for _, backendConfig := range cfg.Backends {
		client := NewClient(
			backendConfig,
			cfg.Server.RefreshInterval.Duration,
			logger,
			onCatalogChanged,
			onResourceUpdated,
		)
		manager.clients[backendConfig.ID] = client
		manager.aliases[strings.ToLower(backendConfig.ID)] = client
		manager.ids = append(manager.ids, backendConfig.ID)
	}
	slices.Sort(manager.ids)
	return manager
}

func (m *Manager) Start(parent context.Context, requireReady bool) error {
	ctx, cancel := context.WithCancel(parent)
	m.cancel = cancel

	type result struct {
		client *Client
		err    error
	}
	results := make(chan result, len(m.clients))
	var wg sync.WaitGroup
	for _, client := range m.clients {
		wg.Add(1)
		go func(client *Client) {
			defer wg.Done()
			results <- result{client: client, err: client.ConnectOnce(ctx)}
		}(client)
	}
	wg.Wait()
	close(results)

	var requiredErrors []error
	for result := range results {
		if result.err == nil {
			continue
		}
		m.logger.Warn("initial backend connection failed", "backend", result.client.ID(), "error_type", fmt.Sprintf("%T", result.err))
		if requireReady && result.client.Required() {
			requiredErrors = append(requiredErrors, fmt.Errorf("backend %s: %w", result.client.ID(), result.err))
		}
	}
	if len(requiredErrors) > 0 {
		cancel()
		m.Close()
		return fmt.Errorf("required backends are unavailable: %w", requiredErrors[0])
	}
	for _, client := range m.clients {
		go client.Run(ctx)
	}
	return nil
}

func (m *Manager) Close() {
	if m.cancel != nil {
		m.cancel()
	}
	for _, client := range m.clients {
		client.Close()
	}
}

func (m *Manager) Ready() bool {
	for _, client := range m.clients {
		if client.Required() && !client.Ready() {
			return false
		}
	}
	return true
}

func (m *Manager) Status() (ready, total, requiredReady, requiredTotal int) {
	for _, client := range m.clients {
		total++
		if client.Ready() {
			ready++
		}
		if client.Required() {
			requiredTotal++
			if client.Ready() {
				requiredReady++
			}
		}
	}
	return ready, total, requiredReady, requiredTotal
}

func (m *Manager) Client(id string) (*Client, bool) {
	client, ok := m.clients[id]
	return client, ok
}

func (m *Manager) IDs() []string {
	return slices.Clone(m.ids)
}

// SeedUnavailableCatalogs carries forward discovery data only for the same
// backend identity and endpoint. It never marks the new connection ready.
func (m *Manager) SeedUnavailableCatalogs(previous *Manager) []string {
	if previous == nil {
		return nil
	}
	var seeded []string
	for _, id := range m.ids {
		client := m.clients[id]
		if client.Required() || client.Ready() || client.Catalog() != nil {
			continue
		}
		oldClient, ok := previous.Client(id)
		if !ok || !sameCatalogSource(oldClient.Config(), client.Config()) {
			continue
		}
		if client.seedCatalog(oldClient.Catalog()) {
			seeded = append(seeded, id)
		}
	}
	return seeded
}

func sameCatalogSource(previous, current config.BackendConfig) bool {
	if previous.URL != current.URL ||
		previous.AllowInsecureHTTP != current.AllowInsecureHTTP ||
		!maps.Equal(previous.Headers, current.Headers) {
		return false
	}
	if previous.OAuth == nil || current.OAuth == nil {
		return previous.OAuth == nil && current.OAuth == nil
	}
	return previous.OAuth.Type == current.OAuth.Type &&
		previous.OAuth.Issuer == current.OAuth.Issuer &&
		previous.OAuth.ClientID == current.OAuth.ClientID &&
		previous.OAuth.ClientSecret == current.OAuth.ClientSecret &&
		slices.Equal(previous.OAuth.Scopes, current.OAuth.Scopes)
}

func (m *Manager) AllowedIDs(scopes []string) []string {
	allowed, _ := m.AllowedProfile(scopes)
	return allowed
}

func (m *Manager) AllowedProfile(scopes []string) ([]string, []string) {
	granted := scopeSet(scopes)
	relevantToolScopes := make(map[string]struct{})
	allowed := make([]string, 0, len(m.clients))
	for _, id := range m.ids {
		client := m.clients[id]
		backendConfig := client.Config()
		if !hasAllScopes(granted, backendConfig.RequiredScopes) {
			continue
		}
		allowed = append(allowed, id)
		for _, rule := range backendConfig.ToolRules {
			for _, scope := range rule.RequiredScopes {
				if _, ok := granted[scope]; ok {
					relevantToolScopes[scope] = struct{}{}
				}
			}
		}
	}
	profileScopes := make([]string, 0, len(relevantToolScopes))
	for scope := range relevantToolScopes {
		profileScopes = append(profileScopes, scope)
	}
	slices.Sort(profileScopes)
	return allowed, profileScopes
}

func (m *Manager) MissingScopes(id string, scopes []string) ([]string, bool) {
	client, ok := m.clients[id]
	if !ok {
		client, ok = m.aliases[strings.ToLower(id)]
	}
	if !ok {
		return nil, false
	}
	granted := scopeSet(scopes)
	var missing []string
	for _, scope := range client.Config().RequiredScopes {
		if _, ok := granted[scope]; !ok {
			missing = append(missing, scope)
		}
	}
	slices.Sort(missing)
	return missing, true
}

func scopeSet(scopes []string) map[string]struct{} {
	granted := make(map[string]struct{}, len(scopes))
	for _, scope := range scopes {
		granted[scope] = struct{}{}
	}
	return granted
}

func hasAllScopes(granted map[string]struct{}, required []string) bool {
	for _, scope := range required {
		if _, ok := granted[scope]; !ok {
			return false
		}
	}
	return true
}
