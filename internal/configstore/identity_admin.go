package configstore

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/SamuelSupe/mcphub/v2/internal/config"
)

var ErrLastAdministrator = errors.New("the last active administrator cannot be disabled or lose administration access")

type identityAdminPolicy struct {
	provider  string
	directory bool
	admin     config.AdminConfig
}

func (s *Store) ConfigureIdentityProtection(auth config.AuthConfig, admin config.AdminConfig) {
	s.identityMu.Lock()
	defer s.identityMu.Unlock()
	s.identityAdmin = nil
	s.identityMaxAge = auth.EnterpriseMembershipMaxAge.Duration
	if s.identityMaxAge == 0 && (auth.Builtin() || auth.SSO != nil) {
		s.identityMaxAge = 24 * time.Hour
	}
	if auth.Builtin() {
		s.identityAdmin = &identityAdminPolicy{config.LocalIdentityProvider, false, admin}
	} else if auth.SSO != nil && admin.Remote() {
		s.identityAdmin = &identityAdminPolicy{auth.SSO.Upstream.Namespace(), auth.SSO.DirectoryTokenEnv != "", admin}
	}
}

// Check the effective users before and after the edit inside the same transaction.
// Group edits and concurrent demotions must not bypass the last-admin safeguard.
// Authoritative directory revocations still take effect; recovery is local-only.
func (s *Store) checkLastAdministrator(ctx context.Context, tx *transaction, replacement Identity) error {
	policy := s.identityAdmin
	if policy == nil || (replacement.Provider != policy.provider && replacement.Provider != config.PermissionGroupProvider) {
		return nil
	}
	rows, err := tx.QueryContext(ctx, "SELECT data FROM identities WHERE provider=? OR provider=?", policy.provider, config.PermissionGroupProvider)
	if err != nil {
		return err
	}
	identities := map[string]Identity{}
	for rows.Next() {
		var data []byte
		var identity Identity
		if err = rows.Scan(&data); err == nil {
			err = json.Unmarshal(data, &identity)
		}
		if err != nil {
			break
		}
		identities[identity.ID] = identity
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	before := policy.administratorCount(identities)
	identities[replacement.ID] = replacement
	if before > 0 && policy.administratorCount(identities) == 0 {
		return ErrLastAdministrator
	}
	return nil
}

func (p *identityAdminPolicy) administratorCount(identities map[string]Identity) int {
	count := 0
	for _, user := range identities {
		if user.Kind != "user" || user.Provider != p.provider || !user.Enabled || !user.DirectoryActive || (p.directory && !user.DirectoryManaged) {
			continue
		}
		permissions := config.IdentityPermissions{}
		for _, id := range user.Groups {
			group, ok := identities[id]
			if ok && group.Kind != "user" && group.Enabled && group.DirectoryActive {
				permissions.Roles = append(permissions.Roles, group.Permissions.Roles...)
				permissions.Scopes = append(permissions.Scopes, group.Permissions.Scopes...)
			}
		}
		for _, group := range identities {
			if group.Provider != config.PermissionGroupProvider || !group.Enabled || !group.DirectoryActive || slices.Contains(user.Groups, group.ID) {
				continue
			}
			if slices.ContainsFunc(group.SourceGroups, func(id string) bool {
				source, ok := identities[id]
				return ok && source.Enabled && source.DirectoryActive && slices.Contains(user.Groups, id)
			}) {
				permissions.Roles = append(permissions.Roles, group.Permissions.Roles...)
				permissions.Scopes = append(permissions.Scopes, group.Permissions.Scopes...)
			}
		}
		scopes := permissions.EffectiveScopes(p.admin)
		if len(p.admin.RequiredScopes) > 0 && !slices.ContainsFunc(p.admin.RequiredScopes, func(scope string) bool { return !slices.Contains(scopes, scope) }) {
			count++
		}
	}
	return count
}
