package app

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/SamuelSupe/mcphub/internal/config"
	"github.com/SamuelSupe/mcphub/internal/configstore"
	"github.com/SamuelSupe/mcphub/internal/httptool"
)

func TestAdminToolGroupAndHTTPToolETagsPreserveSecretsAndRejectStaleWrites(t *testing.T) {
	application := newAdminTestApp(t)
	createGroup := []byte(`{"id":"payments","base_url":"https://api.example.com","enabled":false,"request_timeout":"1s","headers":[{"name":"X-API-Key","value":"group-secret"}]}`)
	response := serveAdminJSON(t, application, http.MethodPost, "/api/v1/tool-groups", createGroup, "")
	if response.Code != http.StatusCreated || response.Header().Get("ETag") != `"1"` {
		t.Fatalf("tool group create status/ETag = %d/%q; body=%s", response.Code, response.Header().Get("ETag"), response.Body.String())
	}
	if strings.Contains(response.Body.String(), "group-secret") {
		t.Fatalf("tool group create leaked configured secret: %s", response.Body.String())
	}
	var groupView toolGroupView
	if err := json.Unmarshal(response.Body.Bytes(), &groupView); err != nil {
		t.Fatalf("decode tool group view: %v", err)
	}
	if groupView.ID != "payments" || groupView.Revision != 1 || len(groupView.Headers) != 1 || !groupView.Headers[0].Configured {
		t.Fatalf("created tool group view = %#v", groupView)
	}
	response = serveAdminJSON(t, application, http.MethodGet, "/api/v1/events", nil, "")
	if response.Code != http.StatusOK {
		t.Fatalf("events after tool group create status = %d; body=%s", response.Code, response.Body.String())
	}
	var eventsPayload struct {
		Events []struct {
			BackendID  string `json:"backend_id"`
			SourceKind string `json:"source_kind"`
			SourceID   string `json:"source_id"`
			Action     string `json:"action"`
		} `json:"events"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &eventsPayload); err != nil || len(eventsPayload.Events) != 1 {
		t.Fatalf("events after tool group create = %#v, err=%v", eventsPayload, err)
	}
	event := eventsPayload.Events[0]
	if event.BackendID != "" || event.SourceKind != "tool_group" || event.SourceID != "payments" || event.Action != "create" {
		t.Fatalf("tool group event = %#v", event)
	}

	response = serveAdminJSON(t, application, http.MethodGet, "/api/v1/tool-groups/payments", nil, "")
	if response.Code != http.StatusOK || response.Header().Get("ETag") != `"1"` || strings.Contains(response.Body.String(), "group-secret") {
		t.Fatalf("tool group GET status/ETag/body = %d/%q/%s", response.Code, response.Header().Get("ETag"), response.Body.String())
	}

	preserveSecret := []byte(`{"id":"payments","base_url":"https://api.example.com","enabled":false,"request_timeout":"2s","headers":[{"name":"X-API-Key"}]}`)
	response = serveAdminJSON(t, application, http.MethodPut, "/api/v1/tool-groups/payments", preserveSecret, `"1"`)
	if response.Code != http.StatusOK || response.Header().Get("ETag") != `"2"` {
		t.Fatalf("tool group update status/ETag = %d/%q; body=%s", response.Code, response.Header().Get("ETag"), response.Body.String())
	}
	stored, err := application.store.GetToolGroup(context.Background(), "payments")
	if err != nil {
		t.Fatalf("GetToolGroup() after update: %v", err)
	}
	if stored.Revision != 2 || stored.Config.Headers["X-API-Key"] != "group-secret" || stored.Config.RequestTimeout.String() != "2s" {
		t.Fatalf("stored tool group after secret-preserving update = %#v", stored)
	}
	response = serveAdminJSON(t, application, http.MethodPut, "/api/v1/tool-groups/payments", preserveSecret, `"1"`)
	if response.Code != http.StatusConflict {
		t.Fatalf("stale tool group update status = %d, want 409; body=%s", response.Code, response.Body.String())
	}

	createTool := []byte(`{"name":"lookup","description":"Find an item","enabled":false,"method":"GET","path":"/items/{id}","parameters":[{"name":"id","argument":"id","in":"path","required":true,"schema":{"type":"string"}}]}`)
	response = serveAdminJSON(t, application, http.MethodPost, "/api/v1/tool-groups/payments/tools", createTool, "")
	if response.Code != http.StatusCreated || response.Header().Get("ETag") != `"1"` {
		t.Fatalf("HTTP tool create status/ETag = %d/%q; body=%s", response.Code, response.Header().Get("ETag"), response.Body.String())
	}
	var toolView httpToolView
	if err := json.Unmarshal(response.Body.Bytes(), &toolView); err != nil {
		t.Fatalf("decode HTTP tool view: %v", err)
	}
	if toolView.Name != "lookup" || toolView.Origin != "manual" || toolView.Revision != 1 {
		t.Fatalf("created HTTP tool view = %#v", toolView)
	}

	response = serveAdminJSON(t, application, http.MethodPut, "/api/v1/tool-groups/payments/tools/lookup", []byte(`{"name":"lookup","description":"stale","enabled":false,"method":"GET","path":"/stale"}`), `"2"`)
	if response.Code != http.StatusConflict {
		t.Fatalf("stale HTTP tool update status = %d, want 409; body=%s", response.Code, response.Body.String())
	}
	response = serveAdminJSON(t, application, http.MethodPut, "/api/v1/tool-groups/payments/tools/lookup", []byte(`{"name":"lookup","description":"updated","enabled":false,"method":"GET","path":"/items/{id}","parameters":[{"name":"id","argument":"id","in":"path","required":true,"schema":{"type":"string"}}]}`), `"1"`)
	if response.Code != http.StatusOK || response.Header().Get("ETag") != `"2"` {
		t.Fatalf("HTTP tool update status/ETag = %d/%q; body=%s", response.Code, response.Header().Get("ETag"), response.Body.String())
	}
	storedTool, err := application.store.GetHTTPTool(context.Background(), "payments", "lookup")
	if err != nil {
		t.Fatalf("GetHTTPTool() after update: %v", err)
	}
	if storedTool.Revision != 2 || storedTool.Config.Description != "updated" || storedTool.Config.Origin != "manual" {
		t.Fatalf("stored HTTP tool after update = %#v", storedTool)
	}
}

func TestAdminHTTPToolSchemaPreservesLargeJSONNumberThroughStore(t *testing.T) {
	application := newAdminTestApp(t)
	groupBody := []byte(`{"id":"catalog","base_url":"https://api.example.com","enabled":true,"request_timeout":"1s"}`)
	response := serveAdminJSON(t, application, http.MethodPost, "/api/v1/tool-groups", groupBody, "")
	if response.Code != http.StatusCreated {
		t.Fatalf("tool group create status = %d; body=%s", response.Code, response.Body.String())
	}
	toolBody := []byte(`{"name":"lookup","enabled":true,"method":"GET","path":"/lookup","parameters":[{"name":"limit","argument":"limit","in":"query","schema":{"type":"integer","default":9007199254740993,"minimum":9007199254740993}}]}`)
	response = serveAdminJSON(t, application, http.MethodPost, "/api/v1/tool-groups/catalog/tools", toolBody, "")
	if response.Code != http.StatusCreated {
		t.Fatalf("HTTP tool create status = %d; body=%s", response.Code, response.Body.String())
	}
	stored, err := application.store.GetHTTPTool(context.Background(), "catalog", "lookup")
	if err != nil {
		t.Fatalf("GetHTTPTool() after schema create: %v", err)
	}
	if len(stored.Config.Parameters) != 1 {
		t.Fatalf("stored parameters = %#v", stored.Config.Parameters)
	}
	encoded, err := json.Marshal(stored.Config.Parameters[0].Schema)
	if err != nil || string(encoded) != `{"default":9007199254740993,"minimum":9007199254740993,"type":"integer"}` {
		t.Fatalf("stored schema number encoding = %s, err=%v", encoded, err)
	}
}

func TestAdminOpenAPIUploadCreateConflictAndReplacementAreAtomic(t *testing.T) {
	application := newAdminTestApp(t)
	groupBody := []byte(`{"id":"catalog","base_url":"https://api.example.com","enabled":false,"request_timeout":"1s"}`)
	response := serveAdminJSON(t, application, http.MethodPost, "/api/v1/tool-groups", groupBody, "")
	if response.Code != http.StatusCreated {
		t.Fatalf("tool group create status = %d; body=%s", response.Code, response.Body.String())
	}
	queryURL := []byte(`{"origin_type":"url","spec_url":"https://api.example.com/openapi.yaml?sig=secret"}`)
	response = serveAdminJSON(t, application, http.MethodPost, "/api/v1/tool-groups/catalog/imports/inspect", queryURL, "")
	if response.Code != http.StatusUnprocessableEntity || !strings.Contains(response.Body.String(), "query") {
		t.Fatalf("OpenAPI URL with query status/body = %d/%s, want 422 query rejection", response.Code, response.Body.String())
	}
	const document = `openapi: 3.0.3
info:
  title: Catalog API
  version: "1"
paths:
  /items:
    get:
      operationId: listItems
      responses:
        '200':
          description: Items
`
	inspectBody := []byte(`{"origin_type":"upload","document":` + strconvQuote(document) + `}`)
	response = serveAdminJSON(t, application, http.MethodPost, "/api/v1/tool-groups/catalog/imports/inspect", inspectBody, "")
	if response.Code != http.StatusOK {
		t.Fatalf("OpenAPI inspect status = %d; body=%s", response.Code, response.Body.String())
	}
	var preview struct {
		SHA256     string `json:"sha256"`
		Operations []struct {
			Key string `json:"key"`
		} `json:"operations"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &preview); err != nil || preview.SHA256 == "" || len(preview.Operations) != 1 {
		t.Fatalf("inspect preview = %#v, err=%v", preview, err)
	}
	missingSelection := []byte(`{"id":"catalog-missing","origin_type":"upload","document":` + strconvQuote(document) + `,"sha256":"` + preview.SHA256 + `","selected":[{"operation_key":"GET /missing","tool_name":"missing","enabled":true}]}`)
	response = serveAdminJSON(t, application, http.MethodPost, "/api/v1/tool-groups/catalog/imports", missingSelection, "")
	if response.Code != http.StatusBadRequest {
		t.Fatalf("missing OpenAPI operation import status = %d, want 400; body=%s", response.Code, response.Body.String())
	}
	imports, err := application.store.ListOpenAPIImports(context.Background(), "catalog")
	if err != nil || len(imports) != 0 {
		t.Fatalf("imports after missing operation rejection = %#v, err=%v", imports, err)
	}
	selected := `[{"operation_key":"` + preview.Operations[0].Key + `","tool_name":"list_items","enabled":true}]`
	createImport := []byte(`{"id":"catalog-v1","origin_type":"upload","document":` + strconvQuote(document) + `,"sha256":"` + preview.SHA256 + `","selected":` + selected + `}`)
	response = serveAdminJSON(t, application, http.MethodPost, "/api/v1/tool-groups/catalog/imports", createImport, "")
	if response.Code != http.StatusCreated || response.Header().Get("ETag") != `"1"` {
		t.Fatalf("OpenAPI import create status/ETag = %d/%q; body=%s", response.Code, response.Header().Get("ETag"), response.Body.String())
	}
	imports, err = application.store.ListOpenAPIImports(context.Background(), "catalog")
	if err != nil || len(imports) != 1 || imports[0].Revision != 1 {
		t.Fatalf("stored imports after create = %#v, err=%v", imports, err)
	}
	tools, err := application.store.ListHTTPTools(context.Background(), "catalog")
	if err != nil || len(tools) != 1 || tools[0].Config.Name != "list_items" || tools[0].Config.ImportID != "catalog-v1" {
		t.Fatalf("stored imported tools = %#v, err=%v", tools, err)
	}

	conflictImport := []byte(`{"id":"catalog-v2","origin_type":"upload","document":` + strconvQuote(document) + `,"sha256":"` + preview.SHA256 + `","selected":` + selected + `}`)
	response = serveAdminJSON(t, application, http.MethodPost, "/api/v1/tool-groups/catalog/imports", conflictImport, "")
	if response.Code != http.StatusConflict {
		t.Fatalf("conflicting OpenAPI import status = %d, want 409; body=%s", response.Code, response.Body.String())
	}
	imports, err = application.store.ListOpenAPIImports(context.Background(), "catalog")
	if err != nil || len(imports) != 1 {
		t.Fatalf("imports after conflict = %#v, err=%v", imports, err)
	}

	const documentV2 = `openapi: 3.0.3
info:
  title: Catalog API
  version: "2"
paths:
  /items:
    get:
      operationId: listItems
      summary: Updated items
      responses:
        '200':
          description: Items
`
	selectedReplacement := `[{"operation_key":"` + preview.Operations[0].Key + `","tool_name":"list_items","enabled":true}]`
	replacement := []byte(`{"id":"catalog-v1","origin_type":"upload","document":` + strconvQuote(documentV2) + `,"selected":` + selectedReplacement + `}`)
	response = serveAdminJSON(t, application, http.MethodPut, "/api/v1/tool-groups/catalog/imports/catalog-v1", replacement, `"2"`)
	if response.Code != http.StatusConflict {
		t.Fatalf("stale OpenAPI replacement status = %d, want 409; body=%s", response.Code, response.Body.String())
	}
	response = serveAdminJSON(t, application, http.MethodPut, "/api/v1/tool-groups/catalog/imports/catalog-v1", replacement, `"1"`)
	if response.Code != http.StatusOK || response.Header().Get("ETag") != `"2"` {
		t.Fatalf("OpenAPI replacement status/ETag = %d/%q; body=%s", response.Code, response.Header().Get("ETag"), response.Body.String())
	}
	tools, err = application.store.ListHTTPTools(context.Background(), "catalog")
	if err != nil || len(tools) != 1 || tools[0].Config.Name != "list_items" || tools[0].Config.Description != "Updated items" {
		t.Fatalf("tools after OpenAPI replacement = %#v, err=%v", tools, err)
	}
	invalid := []byte(`{"id":"catalog-v1","origin_type":"upload","document":"not an OpenAPI document","selected":[]}`)
	response = serveAdminJSON(t, application, http.MethodPut, "/api/v1/tool-groups/catalog/imports/catalog-v1", invalid, `"2"`)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("invalid OpenAPI replacement status = %d, want 400; body=%s", response.Code, response.Body.String())
	}
	current, err := application.store.GetOpenAPIImport(context.Background(), "catalog", "catalog-v1")
	if err != nil || current.Revision != 2 || string(current.Document) != documentV2 {
		t.Fatalf("LKG import after invalid replacement = %#v, err=%v", current, err)
	}
	caseRename := []byte(`{"id":"catalog-v1","origin_type":"upload","document":` + strconvQuote(documentV2) + `,"selected":[{"operation_key":"` + preview.Operations[0].Key + `","tool_name":"LIST_ITEMS","enabled":true}]}`)
	response = serveAdminJSON(t, application, http.MethodPut, "/api/v1/tool-groups/catalog/imports/catalog-v1", caseRename, `"2"`)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("case-only imported tool rename status = %d, want 400; body=%s", response.Code, response.Body.String())
	}
	current, err = application.store.GetOpenAPIImport(context.Background(), "catalog", "catalog-v1")
	if err != nil || current.Revision != 2 || len(current.Config.Selected) != 1 || current.Config.Selected[0].ToolName != "list_items" {
		t.Fatalf("import after rejected case-only rename = %#v, err=%v", current, err)
	}
}

func TestOpenAPIRefreshRemovesDeletedSelectedOperationFromRuntimeAndLKG(t *testing.T) {
	const documentV1 = `openapi: 3.0.3
info:
  title: Catalog API
  version: "1"
paths:
  /items:
    get:
      operationId: listItems
      responses:
        '200':
          description: Items
`
	const documentV2 = `openapi: 3.0.3
info:
  title: Catalog API
  version: "2"
paths: {}
`
	var document atomic.Value
	document.Store(documentV1)
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/openapi.yaml" {
			http.NotFound(w, req)
			return
		}
		w.Header().Set("Content-Type", "text/yaml")
		_, _ = io.WriteString(w, document.Load().(string))
	}))
	t.Cleanup(func() {
		upstream.CloseClientConnections()
		upstream.Close()
	})
	previousTransport := http.DefaultTransport
	http.DefaultTransport = upstream.Client().Transport
	t.Cleanup(func() { http.DefaultTransport = previousTransport })

	application := newAdminTestApp(t)
	groupBody := []byte(`{"id":"catalog","base_url":"` + upstream.URL + `","enabled":true,"request_timeout":"2s"}`)
	response := serveAdminJSON(t, application, http.MethodPost, "/api/v1/tool-groups", groupBody, "")
	if response.Code != http.StatusCreated {
		t.Fatalf("tool group create status = %d; body=%s", response.Code, response.Body.String())
	}
	inspectBody := []byte(`{"origin_type":"url","spec_url":"` + upstream.URL + `/openapi.yaml"}`)
	response = serveAdminJSON(t, application, http.MethodPost, "/api/v1/tool-groups/catalog/imports/inspect", inspectBody, "")
	if response.Code != http.StatusOK {
		t.Fatalf("OpenAPI inspect status = %d; body=%s", response.Code, response.Body.String())
	}
	var preview struct {
		SHA256     string `json:"sha256"`
		Operations []struct {
			Key string `json:"key"`
		} `json:"operations"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &preview); err != nil || preview.SHA256 == "" || len(preview.Operations) != 1 {
		t.Fatalf("OpenAPI inspect preview = %#v, err=%v", preview, err)
	}
	createBody := []byte(`{"id":"catalog-v1","origin_type":"url","spec_url":"` + upstream.URL + `/openapi.yaml","sha256":"` + preview.SHA256 + `","selected":[{"operation_key":"` + preview.Operations[0].Key + `","tool_name":"list_items","enabled":true}]}`)
	response = serveAdminJSON(t, application, http.MethodPost, "/api/v1/tool-groups/catalog/imports", createBody, "")
	if response.Code != http.StatusCreated {
		t.Fatalf("OpenAPI import create status = %d; body=%s", response.Code, response.Body.String())
	}
	if names := runtimeToolNames(t, application.currentRuntime()); len(names) != 1 || names[0] != "catalog.list_items" {
		t.Fatalf("runtime tools before refresh = %v, want catalog.list_items", names)
	}
	record, err := application.store.GetOpenAPIImport(context.Background(), "catalog", "catalog-v1")
	if err != nil {
		t.Fatalf("GetOpenAPIImport() before refresh: %v", err)
	}
	document.Store(documentV2)
	if err := application.refreshOpenAPIImport(record); err != nil {
		t.Fatalf("refresh after selected operation deletion: %v", err)
	}
	refreshed, err := application.store.GetOpenAPIImport(context.Background(), "catalog", "catalog-v1")
	if err != nil {
		t.Fatalf("GetOpenAPIImport() after refresh: %v", err)
	}
	if refreshed.Revision != record.Revision+1 || len(refreshed.Config.Selected) != 0 || string(refreshed.Document) != documentV2 {
		t.Fatalf("refreshed import = %#v, want revision increment with removed selection", refreshed)
	}
	tools, err := application.store.ListHTTPTools(context.Background(), "catalog")
	if err != nil || len(tools) != 0 {
		t.Fatalf("stored tools after deleted operation refresh = %#v, err=%v", tools, err)
	}
	if names := runtimeToolNames(t, application.currentRuntime()); len(names) != 0 {
		t.Fatalf("runtime tools after deleted operation refresh = %v, want empty", names)
	}
}

func TestAdminToolGroupDeleteAndOtherUpdateKeepRuntimeAndStoreConsistent(t *testing.T) {
	application := newAdminTestApp(t)
	for _, id := range []string{"alpha", "beta"} {
		body := []byte(`{"id":"` + id + `","base_url":"https://` + id + `.example.com","enabled":true,"request_timeout":"1s"}`)
		response := serveAdminJSON(t, application, http.MethodPost, "/api/v1/tool-groups", body, "")
		if response.Code != http.StatusCreated || response.Header().Get("ETag") != `"1"` {
			t.Fatalf("%s group create status/ETag = %d/%q; body=%s", id, response.Code, response.Header().Get("ETag"), response.Body.String())
		}
	}
	for _, group := range []struct {
		id   string
		name string
	}{
		{id: "alpha", name: "echo"},
		{id: "beta", name: "lookup"},
	} {
		body := []byte(`{"name":"` + group.name + `","description":"` + group.name + `","enabled":true,"method":"GET","path":"/` + group.name + `"}`)
		response := serveAdminJSON(t, application, http.MethodPost, "/api/v1/tool-groups/"+group.id+"/tools", body, "")
		if response.Code != http.StatusCreated || response.Header().Get("ETag") != `"1"` {
			t.Fatalf("%s HTTP tool create status/ETag = %d/%q; body=%s", group.id, response.Code, response.Header().Get("ETag"), response.Body.String())
		}
	}
	if names := runtimeToolNames(t, application.currentRuntime()); len(names) != 2 || names[0] != "alpha.echo" || names[1] != "beta.lookup" {
		t.Fatalf("runtime tools before serialized changes = %v, want alpha.echo and beta.lookup", names)
	}

	updateBeta := []byte(`{"id":"beta","base_url":"https://beta.example.com","enabled":true,"request_timeout":"2s"}`)
	response := serveAdminJSON(t, application, http.MethodPut, "/api/v1/tool-groups/beta", updateBeta, `"1"`)
	if response.Code != http.StatusOK || response.Header().Get("ETag") != `"2"` {
		t.Fatalf("beta update status/ETag = %d/%q; body=%s", response.Code, response.Header().Get("ETag"), response.Body.String())
	}
	response = serveAdminJSON(t, application, http.MethodDelete, "/api/v1/tool-groups/alpha", nil, `"1"`)
	if response.Code != http.StatusNoContent {
		t.Fatalf("alpha delete status = %d; body=%s", response.Code, response.Body.String())
	}

	groups, err := application.store.ListToolGroups(context.Background())
	if err != nil {
		t.Fatalf("ListToolGroups() after serialized changes: %v", err)
	}
	if len(groups) != 1 || groups[0].Config.ID != "beta" || groups[0].Revision != 2 || groups[0].Config.RequestTimeout.String() != "2s" {
		t.Fatalf("stored groups after serialized changes = %#v, want beta revision 2", groups)
	}
	if names := runtimeToolNames(t, application.currentRuntime()); len(names) != 1 || names[0] != "beta.lookup" {
		t.Fatalf("runtime tools after serialized changes = %v, want beta.lookup", names)
	}
}

func TestAdminToolGroupUpdateKeepsChildCreatedWhileCandidateBuildWaits(t *testing.T) {
	discoveryStarted := make(chan struct{})
	releaseDiscovery := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseDiscovery) }) }
	var firstDiscovery sync.Once
	var oauth *httptest.Server
	oauth = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/.well-known/oauth-authorization-server":
			firstDiscovery.Do(func() {
				close(discoveryStarted)
				<-releaseDiscovery
			})
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"issuer":"`+oauth.URL+`","token_endpoint":"`+oauth.URL+`/token"}`)
		case "/token":
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"access_token":"test-token","token_type":"Bearer","expires_in":3600}`)
		default:
			http.NotFound(w, req)
		}
	}))
	t.Cleanup(func() {
		release()
		oauth.CloseClientConnections()
		oauth.Close()
	})
	previousTransport := http.DefaultTransport
	http.DefaultTransport = oauth.Client().Transport
	t.Cleanup(func() { http.DefaultTransport = previousTransport })

	application := newAdminTestApp(t)
	group := httptool.GroupConfig{
		ID: "payments", BaseURL: "https://api.example.com", Enabled: true,
		RequestTimeout: time.Second, MaxResponseBodyBytes: httptool.DefaultMaxResponseBytes,
	}
	created, err := application.store.CreateToolGroup(context.Background(), configstore.ToolGroupRecord{Config: group})
	if err != nil {
		t.Fatalf("CreateToolGroup() error: %v", err)
	}
	candidate, previous, err := application.buildCandidate(application.currentConfig())
	if err != nil {
		t.Fatalf("build initial HTTP tool candidate: %v", err)
	}
	if err := application.activateCandidate(candidate, previous); err != nil {
		t.Fatalf("activate initial HTTP tool candidate: %v", err)
	}
	group.OAuth = &config.OAuthConfig{Type: "client_credentials", Issuer: oauth.URL, ClientID: "client", ClientSecret: "secret"}
	if _, err := application.store.UpdateToolGroup(context.Background(), configstore.ToolGroupRecord{Config: group}, created.Revision); err != nil {
		t.Fatalf("seed OAuth group update: %v", err)
	}

	childDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		childDone <- serveAdminJSON(t, application, http.MethodPost, "/api/v1/tool-groups/payments/tools", []byte(`{"name":"lookup","enabled":true,"method":"GET","path":"/lookup"}`), "")
	}()
	select {
	case <-discoveryStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for child candidate build to start OAuth discovery")
	}

	groupUpdateDone := make(chan *httptest.ResponseRecorder, 1)
	updateBody := []byte(`{"id":"payments","base_url":"https://api.example.com","enabled":true,"request_timeout":"1s","oauth":{"type":"client_credentials","issuer":"` + oauth.URL + `","client_id":"client","client_secret":"secret"}}`)
	go func() {
		groupUpdateDone <- serveAdminJSON(t, application, http.MethodPut, "/api/v1/tool-groups/payments", updateBody, `"2"`)
	}()
	release()
	childResponse := <-childDone
	groupResponse := <-groupUpdateDone
	if childResponse.Code != http.StatusCreated {
		t.Fatalf("child HTTP tool create status = %d; body=%s", childResponse.Code, childResponse.Body.String())
	}
	if groupResponse.Code != http.StatusOK || groupResponse.Header().Get("ETag") != `"3"` {
		t.Fatalf("group update status/ETag = %d/%q; body=%s", groupResponse.Code, groupResponse.Header().Get("ETag"), groupResponse.Body.String())
	}
	storedGroup, err := application.store.GetToolGroup(context.Background(), "payments")
	if err != nil {
		t.Fatalf("GetToolGroup() after serialized child/update: %v", err)
	}
	if storedGroup.Revision != 3 || len(storedGroup.Config.Tools) != 1 || storedGroup.Config.Tools[0].Name != "lookup" {
		t.Fatalf("stored group after child/update = %#v, want revision 3 with lookup", storedGroup)
	}
	if names := runtimeToolNames(t, application.currentRuntime()); len(names) != 1 || names[0] != "payments.lookup" {
		t.Fatalf("runtime tools after child/update = %v, want payments.lookup", names)
	}
}

func TestAdminHTTPToolCatalogHotUpdatesAndCallsUpstreamWithoutReconnect(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/items/42" {
			http.NotFound(w, req)
			return
		}
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	t.Cleanup(func() {
		upstream.CloseClientConnections()
		upstream.Close()
	})
	originalTransport := http.DefaultTransport
	http.DefaultTransport = upstream.Client().Transport
	t.Cleanup(func() { http.DefaultTransport = originalTransport })

	application := newAdminTestApp(t)
	hubHTTP := httptest.NewServer(application)
	t.Cleanup(func() {
		hubHTTP.CloseClientConnections()
		hubHTTP.Close()
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	listChanged := make(chan struct{}, 4)
	session := connectAppClientWithOptions(t, ctx, hubHTTP.URL+"/mcp", "allowed", &mcp.ClientOptions{
		ToolListChangedHandler: func(context.Context, *mcp.ToolListChangedRequest) {
			select {
			case listChanged <- struct{}{}:
			default:
			}
		},
	})
	t.Cleanup(func() { _ = session.Close() })
	initial, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("initial ListTools() error: %v", err)
	}
	if len(initial.Tools) != 0 {
		t.Fatalf("initial HTTP tool catalog = %#v, want empty", initial.Tools)
	}

	groupBody := []byte(`{"id":"payments","base_url":"` + upstream.URL + `","enabled":true,"request_timeout":"2s"}`)
	response := serveAdminJSON(t, application, http.MethodPost, "/api/v1/tool-groups", groupBody, "")
	if response.Code != http.StatusCreated {
		t.Fatalf("enabled tool group create status = %d; body=%s", response.Code, response.Body.String())
	}
	groups, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools() after HTTP tool group create: %v", err)
	}
	if len(groups.Tools) != 0 {
		t.Fatalf("catalog after empty HTTP tool group create = %#v, want empty", groups.Tools)
	}

	createTool := []byte(`{"name":"lookup","description":"Look up an item","enabled":true,"method":"GET","path":"/items/{id}","parameters":[{"name":"id","argument":"id","in":"path","required":true,"schema":{"type":"string"}}]}`)
	response = serveAdminJSON(t, application, http.MethodPost, "/api/v1/tool-groups/payments/tools", createTool, "")
	if response.Code != http.StatusCreated || response.Header().Get("ETag") != `"1"` {
		t.Fatalf("HTTP tool create status/ETag = %d/%q; body=%s", response.Code, response.Header().Get("ETag"), response.Body.String())
	}
	waitHTTPToolListChanged(t, ctx, listChanged)
	updated, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools() after HTTP tool create: %v", err)
	}
	if len(updated.Tools) != 1 || updated.Tools[0].Name != "payments.lookup" {
		t.Fatalf("catalog after HTTP tool create = %#v", updated.Tools)
	}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "payments.lookup", Arguments: map[string]any{"id": "42"}})
	if err != nil {
		t.Fatalf("CallTool() error: %v", err)
	}
	if result.IsError || len(result.Content) != 1 || result.Content[0].(*mcp.TextContent).Text != `{"ok":true}` || calls.Load() != 1 {
		t.Fatalf("HTTP tool call result/calls = %#v/%d", result, calls.Load())
	}

	drainHTTPToolListChanges(listChanged)
	disableTool := []byte(`{"name":"lookup","description":"Look up an item","enabled":false,"method":"GET","path":"/items/{id}","parameters":[{"name":"id","argument":"id","in":"path","required":true,"schema":{"type":"string"}}]}`)
	response = serveAdminJSON(t, application, http.MethodPut, "/api/v1/tool-groups/payments/tools/lookup", disableTool, `"1"`)
	if response.Code != http.StatusOK || response.Header().Get("ETag") != `"2"` {
		t.Fatalf("HTTP tool disable status/ETag = %d/%q; body=%s", response.Code, response.Header().Get("ETag"), response.Body.String())
	}
	waitHTTPToolListChanged(t, ctx, listChanged)
	final, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools() after HTTP tool disable: %v", err)
	}
	if len(final.Tools) != 0 {
		t.Fatalf("catalog after HTTP tool disable = %#v, want empty", final.Tools)
	}
	if _, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "payments.lookup", Arguments: map[string]any{"id": "42"}}); err == nil {
		t.Fatal("CallTool() for disabled HTTP tool unexpectedly succeeded")
	}
	if calls.Load() != 1 {
		t.Fatalf("upstream calls after disabled tool call = %d, want 1", calls.Load())
	}
}

func TestAdminHTTPToolRuleScopeHotUpdatesExistingSessions(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.URL.Path != "/items" {
			http.NotFound(w, req)
			return
		}
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	t.Cleanup(func() {
		upstream.CloseClientConnections()
		upstream.Close()
	})
	originalTransport := http.DefaultTransport
	http.DefaultTransport = upstream.Client().Transport
	t.Cleanup(func() { http.DefaultTransport = originalTransport })

	application := newAdminTestApp(t)
	hubHTTP := httptest.NewServer(application)
	t.Cleanup(func() {
		hubHTTP.CloseClientConnections()
		hubHTTP.Close()
	})
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	allowedChanged := make(chan struct{}, 4)
	privilegedChanged := make(chan struct{}, 4)
	allowed := connectAppClientWithOptions(t, ctx, hubHTTP.URL+"/mcp", "allowed", &mcp.ClientOptions{
		ToolListChangedHandler: func(context.Context, *mcp.ToolListChangedRequest) {
			select {
			case allowedChanged <- struct{}{}:
			default:
			}
		},
	})
	privileged := connectAppClientWithOptions(t, ctx, hubHTTP.URL+"/mcp", "privileged", &mcp.ClientOptions{
		ToolListChangedHandler: func(context.Context, *mcp.ToolListChangedRequest) {
			select {
			case privilegedChanged <- struct{}{}:
			default:
			}
		},
	})
	t.Cleanup(func() { _ = allowed.Close(); _ = privileged.Close() })
	for _, session := range []*mcp.ClientSession{allowed, privileged} {
		tools, err := session.ListTools(ctx, nil)
		if err != nil {
			t.Fatalf("initial ListTools() error: %v", err)
		}
		if len(tools.Tools) != 0 {
			t.Fatalf("initial HTTP tool catalog = %#v, want empty", tools.Tools)
		}
	}

	groupBody := []byte(`{"id":"payments","base_url":"` + upstream.URL + `","enabled":true,"required_scopes":["mcp:alpha"],"request_timeout":"2s"}`)
	response := serveAdminJSON(t, application, http.MethodPost, "/api/v1/tool-groups", groupBody, "")
	if response.Code != http.StatusCreated {
		t.Fatalf("scoped tool group create status = %d; body=%s", response.Code, response.Body.String())
	}
	createTool := []byte(`{"name":"lookup","description":"Look up an item","enabled":true,"method":"GET","path":"/items"}`)
	response = serveAdminJSON(t, application, http.MethodPost, "/api/v1/tool-groups/payments/tools", createTool, "")
	if response.Code != http.StatusCreated || response.Header().Get("ETag") != `"1"` {
		t.Fatalf("scoped HTTP tool create status/ETag = %d/%q; body=%s", response.Code, response.Header().Get("ETag"), response.Body.String())
	}
	waitHTTPToolListChanged(t, ctx, allowedChanged)
	waitHTTPToolListChanged(t, ctx, privilegedChanged)
	for _, session := range []*mcp.ClientSession{allowed, privileged} {
		tools, err := session.ListTools(ctx, nil)
		if err != nil || len(tools.Tools) != 1 || tools.Tools[0].Name != "payments.lookup" {
			t.Fatalf("catalog before rule update = %#v, err=%v", tools.Tools, err)
		}
	}

	updateGroup := []byte(`{"id":"payments","base_url":"` + upstream.URL + `","enabled":true,"required_scopes":["mcp:alpha"],"tool_rules":[{"match":"lookup","required_scopes":["mcp:alpha:audit"]}],"request_timeout":"2s"}`)
	response = serveAdminJSON(t, application, http.MethodPut, "/api/v1/tool-groups/payments", updateGroup, `"1"`)
	if response.Code != http.StatusOK || response.Header().Get("ETag") != `"2"` {
		t.Fatalf("scoped tool group update status/ETag = %d/%q; body=%s", response.Code, response.Header().Get("ETag"), response.Body.String())
	}
	waitHTTPToolListChanged(t, ctx, allowedChanged)
	waitHTTPToolListChanged(t, ctx, privilegedChanged)
	allowedTools, err := allowed.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("allowed ListTools() after scope update: %v", err)
	}
	if len(allowedTools.Tools) != 0 {
		t.Fatalf("catalog for session without new scope = %#v, want empty", allowedTools.Tools)
	}
	if _, err := allowed.CallTool(ctx, &mcp.CallToolParams{Name: "payments.lookup"}); err == nil {
		t.Fatal("session without required tool scope unexpectedly called HTTP tool")
	}
	privilegedTools, err := privileged.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("privileged ListTools() after scope update: %v", err)
	}
	if len(privilegedTools.Tools) != 1 || privilegedTools.Tools[0].Name != "payments.lookup" {
		t.Fatalf("catalog for session with new scope = %#v, want payments.lookup", privilegedTools.Tools)
	}
	result, err := privileged.CallTool(ctx, &mcp.CallToolParams{Name: "payments.lookup"})
	if err != nil {
		t.Fatalf("privileged CallTool() after scope update: %v", err)
	}
	if result.IsError || len(result.Content) != 1 || result.Content[0].(*mcp.TextContent).Text != `{"ok":true}` || calls.Load() != 1 {
		t.Fatalf("privileged HTTP call result/calls = %#v/%d", result, calls.Load())
	}
}

func waitHTTPToolListChanged(t *testing.T, ctx context.Context, events <-chan struct{}) {
	t.Helper()
	select {
	case <-events:
	case <-ctx.Done():
		t.Fatal("timed out waiting for HTTP tool catalog update")
	}
}

func drainHTTPToolListChanges(events <-chan struct{}) {
	for {
		select {
		case <-events:
		default:
			return
		}
	}
}

func serveAdminJSON(t *testing.T, application *App, method, path string, body []byte, ifMatch string) *httptest.ResponseRecorder {
	t.Helper()
	req := newAdminRequest(method, path, body)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if ifMatch != "" {
		req.Header.Set("If-Match", ifMatch)
	}
	recorder := httptest.NewRecorder()
	application.adminHandler().ServeHTTP(recorder, req)
	return recorder
}

func strconvQuote(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}
