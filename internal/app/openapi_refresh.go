package app

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/SamuelSupe/mcphub/internal/configstore"
	"github.com/SamuelSupe/mcphub/internal/httptool"
	"github.com/SamuelSupe/mcphub/internal/openapiimport"
)

func (a *App) runOpenAPIRefreshLoop() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-a.ctx.Done():
			return
		case <-ticker.C:
			a.refreshDueOpenAPIImports()
		}
	}
}

func (a *App) refreshDueOpenAPIImports() {
	groups, err := a.store.ListToolGroups(a.ctx)
	if err != nil {
		return
	}
	now := time.Now()
	for _, group := range groups {
		imports, err := a.store.ListOpenAPIImports(a.ctx, group.Config.ID)
		if err != nil {
			continue
		}
		for _, record := range imports {
			if record.Config.OriginType != "url" || record.Config.RefreshInterval <= 0 {
				continue
			}
			last := record.UpdatedAt
			if record.LastRefreshAt != nil {
				last = *record.LastRefreshAt
			}
			if now.Before(last.Add(a.openAPIRefreshDelay(record))) {
				continue
			}
			if err := a.refreshOpenAPIImport(record); err != nil {
				a.noteOpenAPIRefreshFailure(record, err)
			} else {
				a.clearOpenAPIRefreshFailure(record)
			}
		}
	}
}

func (a *App) refreshOpenAPIImport(record configstore.OpenAPIImportRecord) error {
	group, err := a.store.GetToolGroup(a.ctx, record.GroupID)
	if err != nil {
		return err
	}
	document, err := a.openAPIDocument(a.ctx, group.Config, "url", record.Config.SpecURL, "")
	if err != nil {
		return err
	}
	sum := sha256.Sum256(document)
	if hex.EncodeToString(sum[:]) == record.SHA256 {
		if err := a.store.MarkOpenAPIRefreshSuccess(a.ctx, record.GroupID, record.Config.ID, record.Revision); errors.Is(err, configstore.ErrConflict) || errors.Is(err, configstore.ErrNotFound) {
			return nil
		} else {
			return err
		}
	}
	preview, err := openapiimport.Parse(document)
	if err != nil {
		return err
	}
	tools, err := toolsForSelections(record.GroupID, record.Config.ID, preview, record.Config.Selected, true)
	if err != nil {
		return err
	}
	next := record
	next.Document, next.SHA256 = document, preview.SHA256
	next.Config.OpenAPIVersion, next.Config.Title, next.Config.Version = preview.OpenAPIVersion, preview.Title, preview.Version
	next.Config.Selected = existingSelections(preview, record.Config.Selected)

	a.reloadMu.Lock()
	defer a.reloadMu.Unlock()
	latestGroup, err := a.store.GetToolGroup(a.ctx, record.GroupID)
	if err != nil {
		return err
	}
	if latestGroup.Revision != group.Revision {
		return nil
	}
	current, err := a.store.GetOpenAPIImport(a.ctx, record.GroupID, record.Config.ID)
	if err != nil {
		return err
	}
	if current.Revision != record.Revision {
		return nil
	}
	groups, err := a.store.ListToolGroups(a.ctx)
	if err != nil {
		return err
	}
	desired := groupConfigs(groups)
	removeImportedTools(desired, record.GroupID, record.Config.ID)
	if err := addImportedTools(desired, record.GroupID, tools, ""); err != nil {
		return err
	}
	previous := a.currentRuntime()
	if previous == nil {
		return fmt.Errorf("service is shutting down")
	}
	if err := httptool.ValidateGroups(desired, backendIDs(a.currentConfig())); err != nil {
		return err
	}
	candidate, err := httptool.NewManager(previous.ctx, desired, a.logger)
	if err != nil {
		return err
	}
	return a.commitHTTPToolCandidate(candidate, previous, func() error {
		_, _, commitErr := a.store.ReplaceOpenAPIImport(a.ctx, next, record.Revision, tools, "refresh")
		return commitErr
	})
}

func (a *App) openAPIRefreshDelay(record configstore.OpenAPIImportRecord) time.Duration {
	key := strings.ToLower(record.GroupID + "/" + record.Config.ID)
	a.refreshMu.Lock()
	failures := a.refreshFailures[key]
	a.refreshMu.Unlock()
	if failures == 0 {
		return record.Config.RefreshInterval
	}
	delay := 30 * time.Second
	for index := 1; index < failures && delay < record.Config.RefreshInterval; index++ {
		delay *= 2
	}
	if delay > record.Config.RefreshInterval {
		delay = record.Config.RefreshInterval
	}
	return delay
}

func (a *App) noteOpenAPIRefreshFailure(record configstore.OpenAPIImportRecord, err error) {
	if markErr := a.store.MarkOpenAPIRefreshFailure(a.ctx, record.GroupID, record.Config.ID, record.Revision, "refresh_failed"); markErr != nil {
		return
	}
	key := strings.ToLower(record.GroupID + "/" + record.Config.ID)
	a.refreshMu.Lock()
	a.refreshFailures[key]++
	a.refreshMu.Unlock()
	a.logger.Warn("OpenAPI import refresh failed", "tool_group", record.GroupID, "import", record.Config.ID, "error_type", fmt.Sprintf("%T", err))
}

func (a *App) clearOpenAPIRefreshFailure(record configstore.OpenAPIImportRecord) {
	key := strings.ToLower(record.GroupID + "/" + record.Config.ID)
	a.refreshMu.Lock()
	delete(a.refreshFailures, key)
	a.refreshMu.Unlock()
}
