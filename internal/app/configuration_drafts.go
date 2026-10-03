package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"slices"
	"strings"

	"github.com/SamuelSupe/mcphub/v2/internal/configstore"
)

type validateConfigurationKey struct{}

func validatingConfiguration(ctx context.Context) bool {
	value, _ := ctx.Value(validateConfigurationKey{}).(bool)
	return value
}

func (a *App) newConfigurationDraft(ctx context.Context, change configurationChange, before, after any) (configstore.ConfigurationDraft, error) {
	raw, err := json.Marshal(change)
	if err != nil {
		return configstore.ConfigurationDraft{}, err
	}
	previous := configurationChange{Kind: change.Kind}
	switch change.Kind {
	case "backend":
		if value, e := a.store.Get(ctx, change.target()); e == nil {
			previous.Backend = &value
		} else if !errors.Is(e, configstore.ErrNotFound) {
			return configstore.ConfigurationDraft{}, e
		}
	case "tool_group":
		if value, e := a.store.GetToolGroup(ctx, change.target()); e == nil {
			previous.Group = &value
		} else if !errors.Is(e, configstore.ErrNotFound) {
			return configstore.ConfigurationDraft{}, e
		}
	case "http_tool":
		if value, e := a.store.GetHTTPTool(ctx, change.Tool.GroupID, change.Tool.Config.Name); e == nil {
			previous.Tool = &value
		} else if !errors.Is(e, configstore.ErrNotFound) {
			return configstore.ConfigurationDraft{}, e
		}
	case "openapi_import":
		if value, e := a.store.GetOpenAPIImport(ctx, change.Import.GroupID, change.Import.Config.ID); e == nil {
			previous.Import = &value
			previous.Tools, e = a.store.ListHTTPTools(ctx, change.Import.GroupID)
			previous.Tools = slices.DeleteFunc(previous.Tools, func(tool configstore.HTTPToolRecord) bool {
				return !strings.EqualFold(tool.Config.ImportID, change.Import.Config.ID)
			})
			if e != nil {
				return configstore.ConfigurationDraft{}, e
			}
		} else if !errors.Is(e, configstore.ErrNotFound) {
			return configstore.ConfigurationDraft{}, e
		}
	default:
		return configstore.ConfigurationDraft{}, errors.New("unsupported configuration change")
	}
	old, _ := json.Marshal(previous)
	beforeJSON, err := json.Marshal(before)
	if err != nil {
		return configstore.ConfigurationDraft{}, err
	}
	afterJSON, err := json.Marshal(after)
	if err != nil {
		return configstore.ConfigurationDraft{}, err
	}
	endpoint := strings.Split(change.target(), "/")[0]
	impact, err := a.store.ConfigurationImpact(ctx, endpoint)
	if err != nil {
		return configstore.ConfigurationDraft{}, err
	}
	credentialsChanged := false
	if change.Backend != nil {
		credentialsChanged = previous.Backend == nil && (len(change.Backend.Config.Headers) > 0 || change.Backend.Config.OAuth != nil || change.Backend.Config.Credentials != nil)
		if previous.Backend != nil {
			credentialsChanged = !reflect.DeepEqual(previous.Backend.Config.Headers, change.Backend.Config.Headers) || !reflect.DeepEqual(previous.Backend.Config.OAuth, change.Backend.Config.OAuth) || !reflect.DeepEqual(previous.Backend.Config.Credentials, change.Backend.Config.Credentials)
		}
	}
	if change.Group != nil {
		credentialsChanged = previous.Group == nil && (len(change.Group.Config.Headers) > 0 || change.Group.Config.OAuth != nil)
		if previous.Group != nil {
			credentialsChanged = !reflect.DeepEqual(previous.Group.Config.Headers, change.Group.Config.Headers) || !reflect.DeepEqual(previous.Group.Config.OAuth, change.Group.Config.OAuth)
		}
	}
	return a.store.SaveConfigurationDraft(ctx, configstore.ConfigurationDraft{CredentialsChanged: credentialsChanged, Kind: change.Kind, Target: change.target(), State: "draft", Change: raw, Previous: old, Before: beforeJSON, After: afterJSON, Impact: impact}, 0)
}

func (a *App) serveConfigurationDrafts(w http.ResponseWriter, r *http.Request, suffix string) {
	if suffix == "" && r.Method == "GET" {
		values, err := a.store.ConfigurationDrafts(r.Context())
		if err != nil {
			writeStoreError(w, err)
			return
		}
		for i := range values {
			a.refreshDraftApproval(r.Context(), &values[i])
		}
		writeJSON(w, 200, map[string]any{"changes": values})
		return
	}
	id, action, _ := strings.Cut(strings.TrimPrefix(suffix, "/"), "/")
	draft, err := a.store.ConfigurationDraft(r.Context(), id)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	a.refreshDraftApproval(r.Context(), &draft)
	if action == "" && r.Method == "GET" {
		w.Header().Set("ETag", revisionETag(draft.Revision))
		writeJSON(w, 200, draft)
		return
	}
	revision, ok := requireRevision(w, r)
	if !ok {
		return
	}
	if revision != draft.Revision {
		writeStoreError(w, configstore.ErrConflict)
		return
	}
	if action == "" && r.Method == "DELETE" {
		a.reloadMu.Lock()
		defer a.reloadMu.Unlock()
		if err = a.store.DeleteConfigurationDraft(r.Context(), id, revision); err != nil {
			writeStoreError(w, err)
			return
		}
		w.WriteHeader(204)
		return
	}
	if r.Method != "POST" || !slices.Contains([]string{"validate", "apply", "discard", "rollback"}, action) {
		http.NotFound(w, r)
		return
	}
	a.reloadMu.Lock()
	defer a.reloadMu.Unlock()
	// Re-read after acquiring the writer lock; two browser tabs cannot apply the
	// same draft or race the validation state machine.
	draft, err = a.store.ConfigurationDraft(r.Context(), id)
	if err != nil || revision != draft.Revision {
		writeStoreError(w, configstore.ErrConflict)
		return
	}
	var change configurationChange
	if err = json.Unmarshal(draft.Change, &change); err != nil {
		writeStoreError(w, err)
		return
	}
	if action == "rollback" {
		if draft.State != "applied" || string(draft.Before) == "null" {
			writeAPIError(w, 409, "revision_conflict", "此记录没有可回退的配置", "")
			return
		}
		if err = json.Unmarshal(draft.Previous, &change); err != nil {
			writeStoreError(w, err)
			return
		}
		var current any
		switch change.Kind {
		case "backend":
			value, e := a.store.Get(r.Context(), change.target())
			if e != nil {
				writeStoreError(w, e)
				return
			}
			change.Revision = value.Revision
			change.Backend.Config.EndpointUID = value.Config.EndpointUID
			current = makeBackendView(value, nil)
		case "tool_group":
			value, e := a.store.GetToolGroup(r.Context(), change.target())
			if e != nil {
				writeStoreError(w, e)
				return
			}
			change.Revision = value.Revision
			change.Group.Config.EndpointUID = value.Config.EndpointUID
			current = a.makeToolGroupView(value)
		case "http_tool":
			value, e := a.store.GetHTTPTool(r.Context(), change.Tool.GroupID, change.Tool.Config.Name)
			if e != nil {
				writeStoreError(w, e)
				return
			}
			change.Revision = value.Revision
			group, e := a.store.GetToolGroup(r.Context(), value.GroupID)
			if e != nil {
				writeStoreError(w, e)
				return
			}
			change.GroupRevision = group.Revision
			current = makeHTTPToolView(value)
		case "openapi_import":
			value, e := a.store.GetOpenAPIImport(r.Context(), change.Import.GroupID, change.Import.Config.ID)
			if e != nil {
				writeStoreError(w, e)
				return
			}
			change.Revision = value.Revision
			group, e := a.store.GetToolGroup(r.Context(), value.GroupID)
			if e != nil {
				writeStoreError(w, e)
				return
			}
			change.GroupRevision = group.Revision
			tools, e := a.store.ListHTTPTools(r.Context(), value.GroupID)
			if e != nil {
				writeStoreError(w, e)
				return
			}
			current = importApprovalView(value, tools)
		}
		next, e := a.newConfigurationDraft(a.adminMutationContext(r), change, current, json.RawMessage(draft.Before))
		if e != nil {
			writeStoreError(w, e)
			return
		}
		writeJSON(w, 201, next)
		return
	}
	if !slices.Contains([]string{"draft", "validated", "failed"}, draft.State) {
		writeAPIError(w, 409, "revision_conflict", "配置草稿状态已改变", "")
		return
	}
	if action == "discard" {
		draft.State = "discarded"
	} else {
		if action == "apply" && draft.State != "validated" {
			writeAPIError(w, 409, "revision_conflict", "请先校验草稿", "")
			return
		}
		err = a.commitConfigurationChange(context.WithValue(r.Context(), validateConfigurationKey{}, true), change)
		if err != nil {
			draft.State = "failed"
			draft.Error = "Validation failed; refresh the service revision and check connectivity"
		} else {
			draft.State = "validated"
			draft.Error = ""
			if action == "apply" {
				if a.currentConfig().Admin.Approvals.PolicyChanges.Enabled {
					identity, _ := r.Context().Value(adminIdentityKey{}).(adminIdentity)
					if identity.info == nil {
						writeAPIError(w, 403, "configuration_identity_required", "需要管理员登录", "")
						return
					}
					change.DraftID = draft.ID
					var approval configstore.Approval
					approval, err = a.createConfigurationProposal(r.Context(), identity.info.UserID, change, json.RawMessage(draft.Before), json.RawMessage(draft.After))
					if err == nil {
						draft.State = "awaiting_approval"
						draft.ApprovalID = approval.ID
					}
				} else {
					err = a.commitConfigurationChange(a.adminMutationContext(r), change)
					if err == nil {
						draft.State = "applied"
					}
				}
				if err != nil {
					draft.State = "failed"
					draft.Error = "Apply failed; inspect the current service revision before retrying"
				}
			}
		}
	}
	saved, saveErr := a.store.SaveConfigurationDraft(a.adminMutationContext(r), draft, draft.Revision)
	if saveErr != nil {
		writeStoreError(w, saveErr)
		return
	}
	status := 200
	if err != nil {
		status = 422
	}
	writeJSON(w, status, saved)
}

func (a *App) refreshDraftApproval(ctx context.Context, draft *configstore.ConfigurationDraft) {
	if draft.State != "awaiting_approval" {
		return
	}
	approval, err := a.store.GetApproval(ctx, draft.ApprovalID)
	if err != nil {
		return
	}
	state := ""
	switch approval.Status {
	case "succeeded":
		state = "applied"
	case "failed", "unknown":
		state = "failed"
	case "rejected", "expired", "revoked", "cancelled":
		state = "discarded"
	}
	if state != "" {
		draft.State = state
		if saved, e := a.store.SaveConfigurationDraft(ctx, *draft, draft.Revision); e == nil {
			*draft = saved
		}
	}
}
