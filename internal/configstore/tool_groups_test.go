package configstore

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/SamuelSupe/mcphub/internal/config"
	"github.com/SamuelSupe/mcphub/internal/httptool"
	"github.com/SamuelSupe/mcphub/internal/openapiimport"
)

func TestToolGroupSecretsAreEncryptedAndWrongKeyIsRejected(t *testing.T) {
	ctx := context.Background()
	store, key, path := newTestStore(t)
	group := toolGroupFixture("payments")
	group.Headers = map[string]string{"X-API-Key": "group-header-secret"}
	group.OAuth = &config.OAuthConfig{Type: "client_credentials", Issuer: "https://idp.example.com", ClientID: "client", ClientSecret: "group-oauth-secret"}
	if _, err := store.CreateToolGroup(ctx, ToolGroupRecord{Config: group}); err != nil {
		t.Fatalf("CreateToolGroup() error: %v", err)
	}
	var public, sealed []byte
	if err := store.db.QueryRowContext(ctx, "SELECT config_json, secrets FROM tool_groups WHERE id='payments'").Scan(&public, &sealed); err != nil {
		t.Fatalf("read raw tool group row: %v", err)
	}
	for name, raw := range map[string][]byte{"public config": public, "sealed secrets": sealed} {
		if bytes.Contains(raw, []byte("group-header-secret")) || bytes.Contains(raw, []byte("group-oauth-secret")) {
			t.Fatalf("%s contains plaintext secret: %q", name, raw)
		}
	}
	loaded, err := store.GetToolGroup(ctx, "PAYMENTS")
	if err != nil {
		t.Fatalf("GetToolGroup() error: %v", err)
	}
	if loaded.Config.Headers["X-API-Key"] != "group-header-secret" || loaded.Config.OAuth == nil || loaded.Config.OAuth.ClientSecret != "group-oauth-secret" {
		t.Fatalf("decrypted tool group secrets = %#v", loaded.Config)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close() error: %v", err)
	}
	wrongKey := append([]byte(nil), key...)
	wrongKey[0]++
	wrong, err := Open(ctx, path, wrongKey)
	if err == nil {
		_ = wrong.Close()
		t.Fatal("Open() with wrong key succeeded")
	}
	if !bytes.Contains([]byte(err.Error()), []byte("incorrect")) {
		t.Fatalf("wrong-key error = %v", err)
	}
}

func TestToolGroupIDsConflictWithBackendsAndEachOtherCaseInsensitively(t *testing.T) {
	ctx := context.Background()
	store, _, _ := newTestStore(t)
	if _, err := store.Create(ctx, Record{Config: config.BackendConfig{ID: "alpha", URL: "https://alpha.example.com/mcp"}}); err != nil {
		t.Fatalf("Create backend error: %v", err)
	}
	if _, err := store.CreateToolGroup(ctx, ToolGroupRecord{Config: toolGroupFixture("ALPHA")}); !errors.Is(err, ErrConflict) {
		t.Fatalf("backend/tool group identity error = %v, want ErrConflict", err)
	}
	if _, err := store.CreateToolGroup(ctx, ToolGroupRecord{Config: toolGroupFixture("Reports")}); err != nil {
		t.Fatalf("CreateToolGroup() error: %v", err)
	}
	if _, err := store.CreateToolGroup(ctx, ToolGroupRecord{Config: toolGroupFixture("reports")}); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate tool group identity error = %v, want ErrConflict", err)
	}
}

func TestToolGroupRevisionConflictPreservesCurrentRecord(t *testing.T) {
	ctx := context.Background()
	store, _, _ := newTestStore(t)
	created, err := store.CreateToolGroup(ctx, ToolGroupRecord{Config: toolGroupFixture("reports")})
	if err != nil {
		t.Fatalf("CreateToolGroup() error: %v", err)
	}
	created.Config.BaseURL = "https://new.example.com/api"
	updated, err := store.UpdateToolGroup(ctx, created, created.Revision)
	if err != nil {
		t.Fatalf("UpdateToolGroup() error: %v", err)
	}
	if updated.Revision != 2 {
		t.Fatalf("updated revision = %d, want 2", updated.Revision)
	}
	stale := updated
	stale.Config.BaseURL = "https://stale.example.com/api"
	if _, err := store.UpdateToolGroup(ctx, stale, created.Revision); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale UpdateToolGroup() error = %v, want ErrConflict", err)
	}
	current, err := store.GetToolGroup(ctx, "reports")
	if err != nil {
		t.Fatalf("GetToolGroup() error: %v", err)
	}
	if current.Revision != 2 || current.Config.BaseURL != "https://new.example.com/api" {
		t.Fatalf("current record after stale update = %#v", current)
	}
}

func TestToolGroupRevisionDoesNotReuseETagAfterDeleteAndRecreate(t *testing.T) {
	ctx := context.Background()
	store, _, _ := newTestStore(t)
	created, err := store.CreateToolGroup(ctx, ToolGroupRecord{Config: toolGroupFixture("reports")})
	if err != nil {
		t.Fatalf("CreateToolGroup() error: %v", err)
	}
	if err := store.DeleteToolGroup(ctx, created.Config.ID, created.Revision); err != nil {
		t.Fatalf("DeleteToolGroup() error: %v", err)
	}
	recreated, err := store.CreateToolGroup(ctx, ToolGroupRecord{Config: toolGroupFixture("reports")})
	if err != nil {
		t.Fatalf("recreate tool group: %v", err)
	}
	if recreated.Revision <= created.Revision {
		t.Fatalf("recreated group revision = %d, want greater than deleted revision %d", recreated.Revision, created.Revision)
	}
	stale := recreated
	stale.Config.BaseURL = "https://stale.example.com/api"
	if _, err := store.UpdateToolGroup(ctx, stale, created.Revision); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale group update with old ETag error = %v, want ErrConflict", err)
	}
	if err := store.DeleteToolGroup(ctx, recreated.Config.ID, created.Revision); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale group delete with old ETag error = %v, want ErrConflict", err)
	}
	current, err := store.GetToolGroup(ctx, recreated.Config.ID)
	if err != nil {
		t.Fatalf("GetToolGroup() after stale writes: %v", err)
	}
	if current.Revision != recreated.Revision || current.Config.BaseURL != recreated.Config.BaseURL {
		t.Fatalf("tool group after stale writes = %#v, want recreated record unchanged", current)
	}
}

func TestManualHTTPToolRevisionDoesNotReuseETagAfterDeleteAndRecreate(t *testing.T) {
	ctx := context.Background()
	store, _, _ := newTestStore(t)
	group := toolGroupFixture("reports")
	if _, err := store.CreateToolGroup(ctx, ToolGroupRecord{Config: group}); err != nil {
		t.Fatalf("CreateToolGroup() error: %v", err)
	}
	tool := httptool.ToolConfig{Name: "lookup", Enabled: true, Method: "GET", Path: "/lookup"}
	created, err := store.CreateHTTPTool(ctx, HTTPToolRecord{GroupID: group.ID, Config: tool})
	if err != nil {
		t.Fatalf("CreateHTTPTool() error: %v", err)
	}
	if err := store.DeleteHTTPTool(ctx, group.ID, tool.Name, created.Revision); err != nil {
		t.Fatalf("DeleteHTTPTool() error: %v", err)
	}
	recreated, err := store.CreateHTTPTool(ctx, HTTPToolRecord{GroupID: group.ID, Config: tool})
	if err != nil {
		t.Fatalf("recreate HTTP tool: %v", err)
	}
	if recreated.Revision <= created.Revision {
		t.Fatalf("recreated HTTP tool revision = %d, want greater than deleted revision %d", recreated.Revision, created.Revision)
	}
	stale := recreated
	stale.Config.Path = "/stale"
	if _, err := store.UpdateHTTPTool(ctx, stale, created.Revision); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale HTTP tool update with old ETag error = %v, want ErrConflict", err)
	}
	if err := store.DeleteHTTPTool(ctx, group.ID, tool.Name, created.Revision); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale HTTP tool delete with old ETag error = %v, want ErrConflict", err)
	}
	current, err := store.GetHTTPTool(ctx, group.ID, tool.Name)
	if err != nil {
		t.Fatalf("GetHTTPTool() after stale writes: %v", err)
	}
	if current.Revision != recreated.Revision || current.Config.Path != recreated.Config.Path {
		t.Fatalf("HTTP tool after stale writes = %#v, want recreated record unchanged", current)
	}
}

func TestOpenAPIImportRevisionDoesNotReuseETagAfterDeleteAndRecreate(t *testing.T) {
	ctx := context.Background()
	store, _, _ := newTestStore(t)
	group := toolGroupFixture("catalog")
	if _, err := store.CreateToolGroup(ctx, ToolGroupRecord{Config: group}); err != nil {
		t.Fatalf("CreateToolGroup() error: %v", err)
	}
	first := openAPIImportFixture(group.ID, "catalog-v1", []byte("spec-v1"))
	created, _, err := store.CreateOpenAPIImport(ctx, first, nil)
	if err != nil {
		t.Fatalf("CreateOpenAPIImport() error: %v", err)
	}
	if err := store.DeleteOpenAPIImport(ctx, group.ID, first.Config.ID, created.Revision); err != nil {
		t.Fatalf("DeleteOpenAPIImport() error: %v", err)
	}
	recreated, _, err := store.CreateOpenAPIImport(ctx, first, nil)
	if err != nil {
		t.Fatalf("recreate OpenAPI import: %v", err)
	}
	if recreated.Revision <= created.Revision {
		t.Fatalf("recreated OpenAPI import revision = %d, want greater than deleted revision %d", recreated.Revision, created.Revision)
	}
	stale := recreated
	stale.Document = []byte("stale-document")
	if _, _, err := store.ReplaceOpenAPIImport(ctx, stale, created.Revision, nil, "refresh"); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale OpenAPI import replace with old ETag error = %v, want ErrConflict", err)
	}
	if err := store.DeleteOpenAPIImport(ctx, group.ID, first.Config.ID, created.Revision); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale OpenAPI import delete with old ETag error = %v, want ErrConflict", err)
	}
	current, err := store.GetOpenAPIImport(ctx, group.ID, first.Config.ID)
	if err != nil {
		t.Fatalf("GetOpenAPIImport() after stale writes: %v", err)
	}
	if current.Revision != recreated.Revision || string(current.Document) != string(recreated.Document) {
		t.Fatalf("OpenAPI import after stale writes = %#v, want recreated record unchanged", current)
	}
}

func TestOpenAPIImportCreateIsAtomicOnToolConflict(t *testing.T) {
	ctx := context.Background()
	store, _, _ := newTestStore(t)
	group := toolGroupFixture("catalog")
	if _, err := store.CreateToolGroup(ctx, ToolGroupRecord{Config: group}); err != nil {
		t.Fatalf("CreateToolGroup() error: %v", err)
	}
	manual := httptool.ToolConfig{Name: "existing", Enabled: true, Method: "GET", Path: "/existing"}
	if _, err := store.CreateHTTPTool(ctx, HTTPToolRecord{GroupID: group.ID, Config: manual}); err != nil {
		t.Fatalf("CreateHTTPTool() error: %v", err)
	}
	record := openAPIImportFixture(group.ID, "catalog-v1", []byte("spec-v1"))
	tool := HTTPToolRecord{GroupID: group.ID, Config: httptool.ToolConfig{Name: "existing", Enabled: true, Method: "GET", Path: "/imported", ImportID: record.Config.ID, Origin: "openapi"}}
	if _, _, err := store.CreateOpenAPIImport(ctx, record, []HTTPToolRecord{tool}); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflicting CreateOpenAPIImport() error = %v, want ErrConflict", err)
	}
	if _, err := store.GetOpenAPIImport(ctx, group.ID, record.Config.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("import after atomic conflict = %v, want ErrNotFound", err)
	}
	tools, err := store.ListHTTPTools(ctx, group.ID)
	if err != nil || len(tools) != 1 || tools[0].Config.Name != "existing" || tools[0].Config.ImportID != "" {
		t.Fatalf("tools after atomic conflict = %#v, err=%v", tools, err)
	}
}

func TestOpenAPIImportReplaceRefreshesToolsAtomicallyAndKeepsLKGOnFailure(t *testing.T) {
	ctx := context.Background()
	store, _, _ := newTestStore(t)
	group := toolGroupFixture("catalog")
	if _, err := store.CreateToolGroup(ctx, ToolGroupRecord{Config: group}); err != nil {
		t.Fatalf("CreateToolGroup() error: %v", err)
	}
	first := openAPIImportFixture(group.ID, "catalog-v1", []byte("spec-v1"))
	oldTool := HTTPToolRecord{GroupID: group.ID, Config: httptool.ToolConfig{Name: "old", Enabled: true, Method: "GET", Path: "/old", ImportID: first.Config.ID, Origin: "openapi"}}
	created, createdTools, err := store.CreateOpenAPIImport(ctx, first, []HTTPToolRecord{oldTool})
	if err != nil {
		t.Fatalf("CreateOpenAPIImport() error: %v", err)
	}
	if len(createdTools) != 1 {
		t.Fatalf("created imported tools = %#v", createdTools)
	}
	next := created
	next.Document = []byte("spec-v2")
	next.SHA256 = "sha-v2"
	refreshedTool := HTTPToolRecord{GroupID: group.ID, Config: httptool.ToolConfig{Name: "old", Enabled: true, Method: "POST", Path: "/new", ImportID: first.Config.ID, Origin: "openapi"}}
	newOperationTool := HTTPToolRecord{GroupID: group.ID, Config: httptool.ToolConfig{Name: "new", Enabled: true, Method: "GET", Path: "/fresh", ImportID: first.Config.ID, Origin: "openapi"}}
	replaced, _, err := store.ReplaceOpenAPIImport(ctx, next, created.Revision, []HTTPToolRecord{refreshedTool, newOperationTool}, "refresh")
	if err != nil {
		t.Fatalf("ReplaceOpenAPIImport() error: %v", err)
	}
	if replaced.Revision != 2 || string(replaced.Document) != "spec-v2" || replaced.LastRefreshOK == nil || !*replaced.LastRefreshOK {
		t.Fatalf("replaced import = %#v", replaced)
	}
	tools, err := store.ListHTTPTools(ctx, group.ID)
	if err != nil || len(tools) != 2 {
		t.Fatalf("tools after replace = %#v, err=%v", tools, err)
	}
	var preserved, fresh HTTPToolRecord
	for _, tool := range tools {
		switch tool.Config.Name {
		case "old":
			preserved = tool
		case "new":
			fresh = tool
		}
	}
	if preserved.Revision != createdTools[0].Revision+1 || !preserved.CreatedAt.Equal(createdTools[0].CreatedAt) || preserved.Config.Path != "/new" {
		t.Fatalf("preserved imported tool after replace = %#v; before=%#v", preserved, createdTools[0])
	}
	if fresh.Revision != 1 || fresh.Config.Path != "/fresh" {
		t.Fatalf("new imported tool after replace = %#v", fresh)
	}
	stale := replaced
	stale.Document = []byte("spec-stale")
	if _, _, err := store.ReplaceOpenAPIImport(ctx, stale, created.Revision, []HTTPToolRecord{oldTool}, "refresh"); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale ReplaceOpenAPIImport() error = %v, want ErrConflict", err)
	}
	eventsBeforeStaleState, err := store.Events(ctx, 100)
	if err != nil {
		t.Fatalf("Events() before stale refresh state writes: %v", err)
	}
	if err := store.MarkOpenAPIRefreshFailure(ctx, group.ID, first.Config.ID, created.Revision, "stale-failure"); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale MarkOpenAPIRefreshFailure() error = %v, want ErrConflict", err)
	}
	if err := store.MarkOpenAPIRefreshSuccess(ctx, group.ID, first.Config.ID, created.Revision); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale MarkOpenAPIRefreshSuccess() error = %v, want ErrConflict", err)
	}
	unchanged, err := store.GetOpenAPIImport(ctx, group.ID, first.Config.ID)
	if err != nil {
		t.Fatalf("GetOpenAPIImport() after stale refresh state writes: %v", err)
	}
	if unchanged.Revision != replaced.Revision || unchanged.LastRefreshOK == nil || !*unchanged.LastRefreshOK || unchanged.LastRefreshMessage != "" {
		t.Fatalf("import changed after stale refresh state writes = %#v", unchanged)
	}
	eventsAfterStaleState, err := store.Events(ctx, 100)
	if err != nil || len(eventsAfterStaleState) != len(eventsBeforeStaleState) {
		t.Fatalf("events after stale refresh state writes = %#v, before=%#v, err=%v", eventsAfterStaleState, eventsBeforeStaleState, err)
	}
	if err := store.MarkOpenAPIRefreshFailure(ctx, group.ID, first.Config.ID, replaced.Revision, "fetch_failed"); err != nil {
		t.Fatalf("MarkOpenAPIRefreshFailure() error: %v", err)
	}
	failure, err := store.GetOpenAPIImport(ctx, group.ID, first.Config.ID)
	if err != nil {
		t.Fatalf("GetOpenAPIImport() after refresh failure: %v", err)
	}
	if string(failure.Document) != "spec-v2" || failure.LastRefreshOK == nil || *failure.LastRefreshOK || failure.LastRefreshMessage != "fetch_failed" {
		t.Fatalf("LKG after refresh failure = %#v", failure)
	}
	if err := store.MarkOpenAPIRefreshSuccess(ctx, group.ID, first.Config.ID, replaced.Revision); err != nil {
		t.Fatalf("MarkOpenAPIRefreshSuccess() error: %v", err)
	}
	success, err := store.GetOpenAPIImport(ctx, group.ID, first.Config.ID)
	if err != nil || success.LastRefreshOK == nil || !*success.LastRefreshOK || success.LastRefreshMessage != "" {
		t.Fatalf("refresh success state = %#v, err=%v", success, err)
	}
	if err := store.DeleteOpenAPIImport(ctx, group.ID, first.Config.ID, success.Revision); err != nil {
		t.Fatalf("DeleteOpenAPIImport() error: %v", err)
	}
	if err := store.DeleteOpenAPIImport(ctx, group.ID, first.Config.ID, success.Revision); !errors.Is(err, ErrNotFound) {
		t.Fatalf("DeleteOpenAPIImport() missing import error = %v, want ErrNotFound", err)
	}
}

func toolGroupFixture(id string) httptool.GroupConfig {
	return httptool.GroupConfig{
		ID: id, BaseURL: "https://api.example.com", Enabled: true,
		RequestTimeout: time.Second, MaxResponseBodyBytes: httptool.DefaultMaxResponseBytes,
	}
}

func openAPIImportFixture(groupID, id string, document []byte) OpenAPIImportRecord {
	return OpenAPIImportRecord{
		GroupID:  groupID,
		Config:   openapiImportConfigFixture(id),
		Document: document,
		SHA256:   id + "-sha",
	}
}

func openapiImportConfigFixture(id string) openapiimport.ImportConfig {
	return openapiimport.ImportConfig{ID: id, OriginType: "upload", OpenAPIVersion: "3.0.3", Selected: []openapiimport.Selection{{OperationKey: "GET /items", ToolName: "items", Enabled: true}}}
}
