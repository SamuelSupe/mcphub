package configstore

import (
	"context"
	"slices"

	"github.com/SamuelSupe/mcphub/v2/internal/config"
)

// A mapping references discovered, source-owned IDs; a login claim cannot name
// a policy group or create a mapping. Each access entry remains independent.
type PermissionSource struct {
	Identity      Identity `json:"group"`
	Origin        string   `json:"origin"`
	MatchedGroups []string `json:"matched_groups,omitempty"`
}

func permissionSources(ctx context.Context, db identityReader, user Identity, groups map[string]Identity) ([]PermissionSource, error) {
	result := []PermissionSource{}
	activeSources := []string{}
	for _, id := range user.Groups {
		group, ok := groups[id]
		if !ok {
			return nil, ErrNotFound
		}
		if group.Kind == "user" || (group.Provider != user.Provider && group.Provider != config.PermissionGroupProvider) || !group.Enabled || !group.DirectoryActive {
			continue
		}
		origin := "direct"
		if !group.ManagedLocally {
			origin = "organization"
			activeSources = append(activeSources, id)
		}
		result = append(result, PermissionSource{Identity: group, Origin: origin})
	}
	if len(activeSources) == 0 {
		return result, nil
	}
	policies, err := readIdentities(ctx, db, "WHERE provider=? ORDER BY id", config.PermissionGroupProvider)
	if err != nil {
		return nil, err
	}
	for _, group := range policies {
		if !group.Enabled || !group.DirectoryActive || slices.Contains(user.Groups, group.ID) {
			continue
		}
		matched := []string{}
		for _, id := range group.SourceGroups {
			if slices.Contains(activeSources, id) {
				matched = append(matched, id)
			}
		}
		if len(matched) > 0 {
			result = append(result, PermissionSource{Identity: group, Origin: "mapping", MatchedGroups: matched})
		}
	}
	return result, nil
}

func (s *Store) UpdatePermissionGroup(ctx context.Context, id string, revision int64, enabled bool, permissions config.IdentityPermissions, sourceGroups []string) (Identity, error) {
	return s.updateIdentityMapping(ctx, id, revision, enabled, permissions, &sourceGroups)
}
