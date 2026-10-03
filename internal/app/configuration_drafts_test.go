package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/SamuelSupe/mcphub/v2/internal/config"
	"github.com/SamuelSupe/mcphub/v2/internal/configstore"
)

func TestConfigurationDraftValidationConflictAndRollback(t *testing.T) {
	application := newAdminTestApp(t, configstore.Record{Config: config.BackendConfig{ID: "alpha", URL: "https://initial.example.com/mcp", Headers: map[string]string{"Authorization": "private-old-secret"}}, Enabled: false})
	makeDraft := func(revision int64, url string) configstore.ConfigurationDraft {
		t.Helper()
		raw, _ := json.Marshal(backendInput{ID: "alpha", URL: url, Enabled: new(bool), Headers: []headerInput{{Name: "Authorization"}}})
		req := newAdminRequest("PUT", "/api/v1/backends/alpha", raw)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("If-Match", revisionETag(revision))
		req.Header.Set("X-MCPHub-Change-Mode", "draft")
		w := httptest.NewRecorder()
		application.adminHandler().ServeHTTP(w, req)
		var draft configstore.ConfigurationDraft
		if w.Code != 201 || json.Unmarshal(w.Body.Bytes(), &draft) != nil {
			t.Fatal("create draft", w.Code, w.Body.String())
		}
		if strings.Contains(w.Body.String(), "private-old-secret") {
			t.Fatal("draft leaked credentials")
		}
		return draft
	}
	action := func(draft configstore.ConfigurationDraft, action string, want int) configstore.ConfigurationDraft {
		t.Helper()
		w := serveAdminJSON(t, application, http.MethodPost, "/api/v1/configuration-changes/"+draft.ID+"/"+action, nil, revisionETag(draft.Revision))
		if w.Code != want {
			t.Fatal(action, w.Code, w.Body.String())
		}
		var next configstore.ConfigurationDraft
		if want != 409 && json.Unmarshal(w.Body.Bytes(), &next) != nil {
			t.Fatal("decode draft")
		}
		return next
	}
	draft := makeDraft(1, "https://proposed.example.com/mcp")
	current, _ := application.store.Get(t.Context(), "alpha")
	if current.Revision != 1 || current.Config.URL != "https://initial.example.com/mcp" {
		t.Fatal("draft modified live service")
	}
	action(draft, "apply", 409)
	draft = action(draft, "validate", 200)
	if draft.State != "validated" {
		t.Fatal("not validated", draft)
	}
	current.Config.URL = "https://concurrent.example.com/mcp"
	if _, err := application.store.Update(t.Context(), current, 1); err != nil {
		t.Fatal(err)
	}
	failed := action(draft, "apply", 422)
	if failed.State != "failed" {
		t.Fatal("stale draft not failed")
	}
	current, _ = application.store.Get(t.Context(), "alpha")
	if current.Revision != 2 || current.Config.URL != "https://concurrent.example.com/mcp" {
		t.Fatal("stale draft overwrote service")
	}
	uid, policy := current.Config.EndpointUID, "previous-policy"
	input := configstore.ClientGrant{GrantBinding: configstore.GrantBinding{ClientID: "ci_draft_client_instance", EndpointUID: uid}, Issuer: "https://idp.example.com", Subject: "alice", Resource: "https://hub.example.com/mcp", ClientName: "Editor", EndpointID: "alpha", EndpointPolicy: policy, Capabilities: configstore.GrantCapabilities{Tools: true}, AllowedTools: []string{"echo"}}
	grant, _, _, err := application.store.CreateClientGrant(t.Context(), input, "", time.Hour, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if err = application.store.RevokeClientGrant(t.Context(), grant.GrantID, grant.Issuer, grant.Subject); err != nil {
		t.Fatal(err)
	}
	draft = makeDraft(2, "https://applied.example.com/mcp")
	draft = action(draft, "validate", 200)
	draft = action(draft, "apply", 200)
	if draft.State != "applied" {
		t.Fatal("not applied")
	}
	current, _ = application.store.Get(t.Context(), "alpha")
	if current.Revision != 3 || current.Config.Headers["Authorization"] != "private-old-secret" {
		t.Fatal("apply lost revision or credentials")
	}
	rollback := action(draft, "rollback", 201)
	if rollback.ID == draft.ID || rollback.State != "draft" {
		t.Fatal("rollback did not create new draft")
	}
	rollback = action(rollback, "validate", 200)
	rollback = action(rollback, "apply", 200)
	current, _ = application.store.Get(t.Context(), "alpha")
	if current.Revision != 4 || current.Config.URL != "https://concurrent.example.com/mcp" {
		t.Fatal("rollback not a new revision", current)
	}
	if currentGrant, err := application.store.GetClientGrant(t.Context(), grant.GrantID, grant.Issuer, grant.Subject); err != nil || currentGrant.Status != "revoked" {
		t.Fatal("rollback resurrected old authorization", currentGrant, err)
	}
}
