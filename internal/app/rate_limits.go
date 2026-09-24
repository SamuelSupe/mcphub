package app

import "github.com/SamuelSupe/mcphub/internal/ratelimit"

func (r *runtime) rateLimitPolicies() map[string]ratelimit.Config {
	policies := r.hub.HTTPToolRateLimits()
	for _, backend := range r.cfg.Backends {
		policies[backend.ID] = backend.RateLimit
	}
	return policies
}
