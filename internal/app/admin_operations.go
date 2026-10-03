package app

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"time"

	"github.com/SamuelSupe/mcphub/v2/internal/config"
	"github.com/SamuelSupe/mcphub/v2/internal/configstore"
	"github.com/SamuelSupe/mcphub/v2/internal/version"
)

func (a *App) serveServices(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.NotFound(w, r)
		return
	}
	records, err := a.store.List(r.Context())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	groups, err := a.store.ListToolGroups(r.Context())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	services := []any{}
	status := a.adminStatusDetails()
	for _, record := range records {
		services = append(services, map[string]any{"kind": "mcp", "id": record.Config.ID, "uid": record.Config.EndpointUID, "configuration": makeBackendView(record, status)})
	}
	for _, record := range groups {
		services = append(services, map[string]any{"kind": "http", "id": record.Config.ID, "uid": record.Config.EndpointUID, "configuration": a.makeToolGroupView(record)})
	}
	writeJSON(w, 200, map[string]any{"services": services})
}

func fileDigest(path string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

func (a *App) serveOperations(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.NotFound(w, r)
		return
	}
	cfg := a.currentConfig()
	operations, err := a.store.Operations(r.Context())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	delivery, err := a.store.ApprovalDeliveryStatus(r.Context())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	identities, err := a.store.Identities(r.Context())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	enterprise, stale := 0, 0
	latest := time.Time{}
	for _, user := range identities {
		if user.Kind != "user" || user.Provider == config.LocalIdentityProvider {
			continue
		}
		enterprise++
		if user.VerifiedAt.After(latest) {
			latest = user.VerifiedAt
		}
		if user.VerifiedAt.IsZero() || time.Since(user.VerifiedAt) > cfg.Auth.EnterpriseMembershipMaxAge.Duration {
			stale++
		}
	}
	backends, err := a.store.List(r.Context())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	groups, err := a.store.ListToolGroups(r.Context())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	revisions := map[string]int64{}
	for _, b := range backends {
		revisions["mcp:"+b.Config.ID] = b.Revision
	}
	for _, g := range groups {
		revisions["http:"+g.Config.ID] = g.Revision
	}
	for _, p := range identities {
		revisions["identity:"+p.ID] = p.Revision
	}
	clients, err := a.store.NativeClients(r.Context())
	if err != nil {
		writeStoreError(w, err)
		return
	}
	revisions["oauth_clients"] = clients.Revision
	if a.sso != nil {
		revisions["identity_connections"] = a.sso.Connections().Revision
	}
	raw, _ := json.Marshal(revisions)
	digest := sha256.Sum256(raw)
	key, err := config.AdminEncryptionKey(cfg.Admin)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	disk := fileDigest(a.configPath)
	writeJSON(w, 200, map[string]any{"version": version.Value, "architecture": "single_instance", "database_engine": cfg.Admin.Driver(), "configuration_version": hex.EncodeToString(digest[:8]), "deployment_sha256": a.deploymentDigest, "deployment_file_changed": disk != "" && disk != a.deploymentDigest, "key_id": configstore.EncryptionKeyID(key), "sources": map[string]any{"deployment": "yaml/environment", "managed": "database", "bootstrap": "once"}, "directory": map[string]any{"users": enterprise, "stale": stale, "latest_verified_at": latest, "maximum_age": cfg.Auth.EnterpriseMembershipMaxAge.String()}, "operations": operations, "audit_delivery": delivery, "request_record_failures": a.requestWriteFailures.Load()})
}
