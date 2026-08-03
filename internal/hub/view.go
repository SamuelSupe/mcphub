package hub

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/SamuelSupe/mcphub/internal/version"
)

type view struct {
	hub     *Hub
	server  *mcp.Server
	allowed map[string]struct{}
	byHost  map[string]string
	ids     []string

	reconcileMu sync.RWMutex
	tools       map[string]string
	prompts     map[string]string
	resources   map[string]string
	templates   map[string]string

	routesMu          sync.RWMutex
	promptRoutes      map[string]namedRoute
	templateRoutes    map[string]*templateRoute
	internalTemplates map[string]struct{}

	subscriptionMu  sync.Mutex
	subscriptions   map[backendSubscription]map[string]int
	bySession       map[*mcp.ServerSession]map[sessionSubscription]struct{}
	watchedSessions map[*mcp.ServerSession]struct{}
}

type namedRoute struct {
	backendID string
	original  string
}

type backendSubscription struct {
	backendID string
	original  string
}

type sessionSubscription struct {
	backend backendSubscription
	exposed string
}

type toolDefinition struct {
	backendID   string
	original    string
	tool        *mcp.Tool
	fingerprint string
}

type promptDefinition struct {
	backendID   string
	original    string
	prompt      *mcp.Prompt
	fingerprint string
}

type resourceDefinition struct {
	backendID   string
	original    string
	resource    *mcp.Resource
	fingerprint string
}

type templateDefinition struct {
	route       *templateRoute
	template    *mcp.ResourceTemplate
	fingerprint string
}

func newView(h *Hub, ids []string) *view {
	v := &view{
		hub:               h,
		allowed:           make(map[string]struct{}, len(ids)),
		byHost:            make(map[string]string, len(ids)),
		ids:               slices.Clone(ids),
		tools:             make(map[string]string),
		prompts:           make(map[string]string),
		resources:         make(map[string]string),
		templates:         make(map[string]string),
		promptRoutes:      make(map[string]namedRoute),
		templateRoutes:    make(map[string]*templateRoute),
		internalTemplates: make(map[string]struct{}, len(ids)),
		subscriptions:     make(map[backendSubscription]map[string]int),
		bySession:         make(map[*mcp.ServerSession]map[sessionSubscription]struct{}),
		watchedSessions:   make(map[*mcp.ServerSession]struct{}),
	}
	for _, id := range ids {
		v.allowed[id] = struct{}{}
		v.byHost[strings.ToLower(id)] = id
	}
	v.server = mcp.NewServer(
		&mcp.Implementation{Name: "mcphub", Version: version.Value},
		&mcp.ServerOptions{
			PageSize: h.cfg.Server.PageSize,
			Capabilities: &mcp.ServerCapabilities{
				Tools:       &mcp.ToolCapabilities{ListChanged: true},
				Prompts:     &mcp.PromptCapabilities{ListChanged: true},
				Resources:   &mcp.ResourceCapabilities{ListChanged: true, Subscribe: true},
				Completions: &mcp.CompletionCapabilities{},
			},
			CompletionHandler:  v.complete,
			SubscribeHandler:   v.subscribe,
			UnsubscribeHandler: v.unsubscribe,
		},
	)
	for _, id := range ids {
		uriTemplate := issuedResourceTemplate(id)
		v.internalTemplates[uriTemplate] = struct{}{}
		v.server.AddResourceTemplate(&mcp.ResourceTemplate{
			Name:        "mcphub-issued-" + id,
			URITemplate: uriTemplate,
		}, v.issuedResourceHandler(id))
	}
	v.server.AddReceivingMiddleware(v.timeoutMiddleware, v.loggingMiddleware, v.catalogBarrierMiddleware, v.privateCacheMiddleware)
	return v
}

func (v *view) allows(id string) bool {
	_, ok := v.allowed[id]
	return ok
}

func (v *view) backendForHost(host string) (string, bool) {
	id, ok := v.byHost[strings.ToLower(host)]
	return id, ok
}

func (v *view) reconcile() {
	toolDefs := make(map[string]toolDefinition)
	promptDefs := make(map[string]promptDefinition)
	resourceDefs := make(map[string]resourceDefinition)
	templateDefs := make(map[string]templateDefinition)

	for _, id := range v.ids {
		client, ok := v.hub.manager.Client(id)
		if !ok {
			continue
		}
		catalog := client.Catalog()
		if catalog == nil {
			continue
		}
		for _, tool := range catalog.Tools {
			if tool == nil {
				continue
			}
			exposed, err := exposeName(id, tool.Name)
			if err != nil {
				v.hub.logger.Warn("omit invalid backend tool", "backend", id, "capability_id", shortHash(tool.Name), "error_type", fmt.Sprintf("%T", err))
				continue
			}
			copyTool := *tool
			copyTool.Name = exposed
			fingerprint, err := fingerprint(&copyTool)
			if err != nil {
				v.hub.logger.Warn("omit backend tool with invalid metadata", "backend", id, "name", tool.Name, "error_type", fmt.Sprintf("%T", err))
				continue
			}
			if _, collision := toolDefs[exposed]; collision {
				v.hub.logger.Warn("omit colliding backend tool", "backend", id, "name", tool.Name, "exposed_name", exposed)
				continue
			}
			toolDefs[exposed] = toolDefinition{id, tool.Name, &copyTool, fingerprint}
		}
		for _, prompt := range catalog.Prompts {
			if prompt == nil {
				continue
			}
			exposed, err := exposeName(id, prompt.Name)
			if err != nil {
				v.hub.logger.Warn("omit invalid backend prompt", "backend", id, "capability_id", shortHash(prompt.Name), "error_type", fmt.Sprintf("%T", err))
				continue
			}
			copyPrompt := *prompt
			copyPrompt.Name = exposed
			fingerprint, err := fingerprint(&copyPrompt)
			if err != nil {
				continue
			}
			if _, collision := promptDefs[exposed]; collision {
				continue
			}
			promptDefs[exposed] = promptDefinition{id, prompt.Name, &copyPrompt, fingerprint}
		}
		for _, resource := range catalog.Resources {
			if resource == nil || resource.URI == "" {
				continue
			}
			exposed := encodeResource(id, resource.URI)
			copyResource := *resource
			copyResource.URI = exposed
			fingerprint, err := fingerprint(&copyResource)
			if err != nil {
				continue
			}
			resourceDefs[exposed] = resourceDefinition{id, resource.URI, &copyResource, fingerprint}
		}
		for _, resourceTemplate := range catalog.ResourceTemplates {
			if resourceTemplate == nil || resourceTemplate.URITemplate == "" {
				continue
			}
			route, err := newTemplateRoute(id, resourceTemplate.URITemplate)
			if err != nil {
				v.hub.logger.Warn("omit invalid backend resource template", "backend", id, "template_id", shortHash(resourceTemplate.URITemplate), "error_type", fmt.Sprintf("%T", err))
				continue
			}
			copyTemplate := *resourceTemplate
			copyTemplate.URITemplate = route.exposed
			fingerprint, err := fingerprint(&copyTemplate)
			if err != nil {
				continue
			}
			templateDefs[route.exposed] = templateDefinition{route, &copyTemplate, fingerprint}
		}
	}

	v.reconcileMu.Lock()
	defer v.reconcileMu.Unlock()
	v.applyTools(toolDefs)
	v.applyPrompts(promptDefs)
	v.applyResources(resourceDefs)
	v.applyTemplates(templateDefs)

	promptRoutes := make(map[string]namedRoute, len(promptDefs))
	for name, definition := range promptDefs {
		if v.prompts[name] == definition.fingerprint {
			promptRoutes[name] = namedRoute{definition.backendID, definition.original}
		}
	}
	templateRoutes := make(map[string]*templateRoute, len(templateDefs))
	for name, definition := range templateDefs {
		if v.templates[name] == definition.fingerprint {
			templateRoutes[name] = definition.route
		}
	}
	v.routesMu.Lock()
	v.promptRoutes = promptRoutes
	v.templateRoutes = templateRoutes
	v.routesMu.Unlock()
}

func (v *view) applyTools(desired map[string]toolDefinition) {
	removeMissing(v.tools, desiredKeys(desired), v.server.RemoveTools)
	for _, name := range sortedKeys(desired) {
		definition := desired[name]
		if v.tools[name] == definition.fingerprint {
			continue
		}
		if _, exists := v.tools[name]; exists {
			// AddTool validates before replacing. Remove the previous definition
			// first so a backend update with a newly invalid schema cannot leave a
			// stale public schema wired to the backend's changed implementation.
			v.server.RemoveTools(name)
			delete(v.tools, name)
		}
		if v.safeAddTool(definition) {
			v.tools[name] = definition.fingerprint
		}
	}
}

func (v *view) applyPrompts(desired map[string]promptDefinition) {
	removeMissing(v.prompts, desiredKeys(desired), v.server.RemovePrompts)
	for _, name := range sortedKeys(desired) {
		definition := desired[name]
		if v.prompts[name] == definition.fingerprint {
			continue
		}
		v.server.AddPrompt(definition.prompt, v.promptHandler(definition))
		v.prompts[name] = definition.fingerprint
	}
}

func (v *view) applyResources(desired map[string]resourceDefinition) {
	removeMissing(v.resources, desiredKeys(desired), v.server.RemoveResources)
	for _, uri := range sortedKeys(desired) {
		definition := desired[uri]
		if v.resources[uri] == definition.fingerprint {
			continue
		}
		if v.safeAddResource(definition) {
			v.resources[uri] = definition.fingerprint
		}
	}
}

func (v *view) applyTemplates(desired map[string]templateDefinition) {
	removeMissing(v.templates, desiredKeys(desired), v.server.RemoveResourceTemplates)
	for _, uri := range sortedKeys(desired) {
		definition := desired[uri]
		if v.templates[uri] == definition.fingerprint {
			continue
		}
		if v.safeAddTemplate(definition) {
			v.templates[uri] = definition.fingerprint
		}
	}
}

func (v *view) safeAddTool(definition toolDefinition) (added bool) {
	defer func() {
		if value := recover(); value != nil {
			added = false
			v.hub.logger.Warn("omit backend tool rejected by MCP SDK", "backend", definition.backendID, "name", definition.original, "error_type", fmt.Sprintf("%T", value))
		}
	}()
	v.server.AddTool(definition.tool, v.toolHandler(definition))
	return true
}

func (v *view) safeAddResource(definition resourceDefinition) (added bool) {
	defer func() {
		if value := recover(); value != nil {
			added = false
			v.hub.logger.Warn("omit backend resource rejected by MCP SDK", "backend", definition.backendID, "resource_id", shortHash(definition.original), "error_type", fmt.Sprintf("%T", value))
		}
	}()
	v.server.AddResource(definition.resource, v.resourceHandler(definition.backendID, definition.original))
	return true
}

func (v *view) safeAddTemplate(definition templateDefinition) (added bool) {
	defer func() {
		if value := recover(); value != nil {
			added = false
			v.hub.logger.Warn("omit backend resource template rejected by MCP SDK", "backend", definition.route.backendID, "template_id", shortHash(definition.route.original), "error_type", fmt.Sprintf("%T", value))
		}
	}()
	v.server.AddResourceTemplate(definition.template, v.templateHandler(definition.route))
	return true
}

func shortHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:6])
}

func (v *view) close() {
	for session := range v.server.Sessions() {
		_ = session.Close()
	}
}

func fingerprint(value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func sortedKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

func desiredKeys[V any](values map[string]V) map[string]struct{} {
	result := make(map[string]struct{}, len(values))
	for key := range values {
		result[key] = struct{}{}
	}
	return result
}

func removeMissing(current map[string]string, desired map[string]struct{}, remove func(...string)) {
	var missing []string
	for key := range current {
		if _, ok := desired[key]; !ok {
			missing = append(missing, key)
			delete(current, key)
		}
	}
	if len(missing) > 0 {
		slices.Sort(missing)
		remove(missing...)
	}
}
