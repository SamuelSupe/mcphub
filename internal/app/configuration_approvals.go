package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"reflect"
	"strings"
	"time"

	"github.com/SamuelSupe/mcphub/v2/internal/config"
	"github.com/SamuelSupe/mcphub/v2/internal/configstore"
	"github.com/SamuelSupe/mcphub/v2/internal/httptool"
)

// Proposals contain resolved configuration, including encrypted credentials and
// the exact imported document. Approval never fetches a new remote definition.
type configurationChange struct {
	Kind          string                           `json:"kind"`
	Revision      int64                            `json:"revision"`
	GroupRevision int64                            `json:"group_revision,omitempty"`
	Backend       *configstore.Record              `json:"backend,omitempty"`
	Group         *configstore.ToolGroupRecord     `json:"group,omitempty"`
	Tool          *configstore.HTTPToolRecord      `json:"tool,omitempty"`
	Import        *configstore.OpenAPIImportRecord `json:"import,omitempty"`
	Tools         []configstore.HTTPToolRecord     `json:"tools,omitempty"`
}

func (c configurationChange) target() string {
	switch c.Kind {
	case "backend":
		return c.Backend.Config.ID
	case "tool_group":
		return c.Group.Config.ID
	case "http_tool":
		return c.Tool.GroupID + "/" + c.Tool.Config.Name
	case "openapi_import":
		return c.Import.GroupID + "/" + c.Import.Config.ID
	}
	return ""
}

func (a *App) stageConfigurationChange(w http.ResponseWriter, req *http.Request, change configurationChange, before, after any) bool {
	if !a.currentConfig().Admin.Approvals.PolicyChanges.Enabled {
		return false
	}
	identity, _ := req.Context().Value(adminIdentityKey{}).(adminIdentity)
	if identity.info == nil {
		writeAPIError(w, 403, "configuration_identity_required", "配置变更必须由已登录管理员提交", "")
		return true
	}
	value, err := a.createConfigurationProposal(req.Context(), identity.info.UserID, change, before, after)
	if err != nil {
		writeAPIError(w, 503, "proposal_failed", "无法保存配置变更提案，请检查审批数量和配置大小", "")
		return true
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"pending_approval": true, "approval_id": value.ID, "approval_url": a.currentConfig().Admin.PublicURL + "/?approval=" + value.ID + "#approvals"})
	return true
}

func (a *App) createConfigurationProposal(ctx context.Context, subject string, change configurationChange, before, after any) (configstore.Approval, error) {
	settings := a.currentConfig().Admin.Approvals
	policy := config.ToolApprovalPolicy{RequireDifferentReviewer: true, RequireStepUp: settings.PolicyChanges.RequireStepUp}
	if len(settings.PolicyChanges.Subjects) > 0 {
		policy.Approvers = []config.ApprovalGrant{{Subjects: settings.PolicyChanges.Subjects}}
	}
	credentialsChanged := false
	if change.Backend != nil {
		previous, err := a.store.Get(ctx, change.Backend.Config.ID)
		if err != nil && !errors.Is(err, configstore.ErrNotFound) {
			return configstore.Approval{}, err
		}
		credentialsChanged = !maps.Equal(previous.Config.Headers, change.Backend.Config.Headers) || !reflect.DeepEqual(previous.Config.OAuth, change.Backend.Config.OAuth)
	}
	if change.Group != nil {
		previous, err := a.store.GetToolGroup(ctx, change.Group.Config.ID)
		if err != nil && !errors.Is(err, configstore.ErrNotFound) {
			return configstore.Approval{}, err
		}
		credentialsChanged = !maps.Equal(previous.Config.Headers, change.Group.Config.Headers) || !reflect.DeepEqual(previous.Config.OAuth, change.Group.Config.OAuth)
	}
	data, err := json.Marshal(change)
	if err != nil {
		return configstore.Approval{}, err
	}
	preview, err := json.Marshal(map[string]any{"before": before, "after": after, "credentials_changed": credentialsChanged})
	if err != nil {
		return configstore.Approval{}, err
	}
	return a.store.CreateApproval(ctx, configstore.ApprovalIntent{Kind: "configuration", Change: data, Issuer: a.currentConfig().Auth.Issuer, Subject: subject, Tool: "configuration." + change.Kind, Target: change.target(), Effect: "configuration", Params: preview, Preview: preview, Rules: []config.ToolApprovalPolicy{policy}, PendingTTL: settings.WaitDuration(), ExecutionTTL: settings.ExecuteDuration()})
}

func importApprovalView(record configstore.OpenAPIImportRecord, tools []configstore.HTTPToolRecord) any {
	views := []httpToolView{}
	for _, tool := range tools {
		if strings.EqualFold(tool.Config.ImportID, record.Config.ID) {
			views = append(views, makeHTTPToolView(tool))
		}
	}
	return map[string]any{"import": makeOpenAPIImportView(record), "tools": views}
}

func backendDisableOnly(current configstore.Record, next config.BackendConfig, enabled bool) bool {
	return current.Enabled && !enabled && reflect.DeepEqual(current.Config, next)
}

func groupDisableOnly(current, next httptool.GroupConfig) bool {
	if !current.Enabled || next.Enabled {
		return false
	}
	current.Enabled = false
	current.Tools, next.Tools = nil, nil
	return reflect.DeepEqual(current, next)
}

func toolDisableOnly(current, next httptool.ToolConfig) bool {
	if !current.Enabled || next.Enabled {
		return false
	}
	current.Enabled = false
	return reflect.DeepEqual(current, next)
}

func (a *App) applyConfigurationApproval(ctx context.Context, value configstore.Approval) error {
	if value.Intent.Kind != "configuration" || !a.currentConfig().Admin.Approvals.PolicyChanges.Enabled {
		return errors.New("configuration approval is disabled")
	}
	var change configurationChange
	if json.Unmarshal(value.Intent.Change, &change) != nil {
		return errors.New("invalid configuration proposal")
	}
	a.reloadMu.Lock()
	defer a.reloadMu.Unlock()
	if err := a.store.ClaimApproval(ctx, value.ID); err != nil {
		return err
	}
	err := a.commitConfigurationChange(ctx, change)
	status, message := "succeeded", "Configuration applied"
	if err != nil {
		status, message = "unknown", "Configuration outcome is uncertain; inspect the current configuration before proposing another change"
		if errors.Is(err, configstore.ErrConflict) {
			status, message = "failed", "Configuration revision changed; review the current configuration and propose a new change"
		}
	}
	result, _ := json.Marshal(map[string]string{"message": message})
	saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if saveErr := a.store.FinishApproval(saveCtx, value.ID, status, result); saveErr != nil {
		return saveErr
	}
	return err
}

func (a *App) commitConfigurationChange(ctx context.Context, change configurationChange) error {
	if change.Kind == "backend" && change.Backend != nil {
		next := *change.Backend
		current, err := a.store.Get(ctx, next.Config.ID)
		if (change.Revision == 0 && !errors.Is(err, configstore.ErrNotFound)) || (change.Revision > 0 && (err != nil || current.Revision != change.Revision)) {
			return configstore.ErrConflict
		}
		records, err := a.store.List(ctx)
		if err != nil {
			return err
		}
		if change.Revision == 0 {
			records = append(records, next)
		} else {
			for i := range records {
				if strings.EqualFold(records[i].Config.ID, next.Config.ID) {
					records[i] = next
				}
			}
		}
		cfg := configWithRecords(a.currentConfig(), records)
		if err := cfg.Validate(); err != nil {
			return err
		}
		candidate, previous, err := a.buildCandidate(cfg)
		if err != nil {
			return err
		}
		return a.commitAdminCandidate(candidate, previous, func() error {
			if change.Revision == 0 {
				_, err = a.store.Create(ctx, next)
			} else {
				_, err = a.store.Update(ctx, next, change.Revision)
			}
			return err
		})
	}
	groups, err := a.store.ListToolGroups(ctx)
	if err != nil {
		return err
	}
	desired := groupConfigs(groups)
	var commit func() error
	switch change.Kind {
	case "tool_group":
		if change.Group == nil {
			return errors.New("invalid group proposal")
		}
		next := *change.Group
		current, err := a.store.GetToolGroup(ctx, next.Config.ID)
		if (change.Revision == 0 && !errors.Is(err, configstore.ErrNotFound)) || (change.Revision > 0 && (err != nil || current.Revision != change.Revision)) {
			return configstore.ErrConflict
		}
		if change.Revision == 0 {
			desired = append(desired, next.Config)
		} else {
			for i := range desired {
				if strings.EqualFold(desired[i].ID, next.Config.ID) {
					next.Config.Tools = desired[i].Tools
					desired[i] = next.Config
				}
			}
		}
		commit = func() error {
			if change.Revision == 0 {
				_, err = a.store.CreateToolGroup(ctx, next)
			} else {
				_, err = a.store.UpdateToolGroup(ctx, next, change.Revision)
			}
			return err
		}
	case "http_tool", "openapi_import":
		var groupID string
		if change.Kind == "http_tool" && change.Tool != nil {
			groupID = change.Tool.GroupID
		} else if change.Kind == "openapi_import" && change.Import != nil {
			groupID = change.Import.GroupID
		} else {
			return errors.New("invalid tool proposal")
		}
		group, err := a.store.GetToolGroup(ctx, groupID)
		if err != nil || group.Revision != change.GroupRevision {
			return configstore.ErrConflict
		}
		if change.Kind == "http_tool" {
			next := *change.Tool
			current, err := a.store.GetHTTPTool(ctx, groupID, next.Config.Name)
			if (change.Revision == 0 && !errors.Is(err, configstore.ErrNotFound)) || (change.Revision > 0 && (err != nil || current.Revision != change.Revision)) {
				return configstore.ErrConflict
			}
			for i := range desired {
				if !strings.EqualFold(desired[i].ID, groupID) {
					continue
				}
				if change.Revision == 0 {
					desired[i].Tools = append(desired[i].Tools, next.Config)
				} else {
					for j := range desired[i].Tools {
						if strings.EqualFold(desired[i].Tools[j].Name, next.Config.Name) {
							desired[i].Tools[j] = next.Config
						}
					}
				}
			}
			commit = func() error {
				if change.Revision == 0 {
					_, err = a.store.CreateHTTPTool(ctx, next)
				} else {
					_, err = a.store.UpdateHTTPTool(ctx, next, change.Revision)
				}
				return err
			}
		} else {
			next := *change.Import
			current, err := a.store.GetOpenAPIImport(ctx, groupID, next.Config.ID)
			if (change.Revision == 0 && !errors.Is(err, configstore.ErrNotFound)) || (change.Revision > 0 && (err != nil || current.Revision != change.Revision)) {
				return configstore.ErrConflict
			}
			if change.Revision > 0 {
				removeImportedTools(desired, groupID, next.Config.ID)
			}
			if err := addImportedTools(desired, groupID, change.Tools, ""); err != nil {
				return err
			}
			commit = func() error {
				if change.Revision == 0 {
					_, _, err = a.store.CreateOpenAPIImport(ctx, next, change.Tools)
				} else {
					_, _, err = a.store.ReplaceOpenAPIImport(ctx, next, change.Revision, change.Tools, "update")
				}
				return err
			}
		}
	default:
		return fmt.Errorf("invalid configuration proposal kind")
	}
	if err := httptool.ValidateGroups(desired, backendIDs(a.currentConfig())); err != nil {
		return err
	}
	previous := a.currentRuntime()
	if previous == nil {
		return errRuntimeApply
	}
	candidate, err := httptool.NewManager(previous.ctx, desired, a.logger)
	if err != nil {
		return err
	}
	return a.commitHTTPToolCandidate(candidate, previous, commit)
}
