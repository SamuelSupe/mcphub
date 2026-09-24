package ratelimit

import (
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// Config applies to all callers of one configured endpoint. Zero disables the
// corresponding limit. A positive rate with burst zero uses a capacity of one.
type Config struct {
	RequestsPerSecond float64 `json:"requests_per_second" yaml:"requests_per_second"`
	Burst             int     `json:"burst" yaml:"burst"`
	MaxConcurrent     int     `json:"max_concurrent" yaml:"max_concurrent"`
}

func (c Config) Validate() error {
	if math.IsNaN(c.RequestsPerSecond) || math.IsInf(c.RequestsPerSecond, 0) || c.RequestsPerSecond < 0 {
		return fmt.Errorf("rate_limit.requests_per_second must be a finite nonnegative number")
	}
	if c.Burst < 0 || c.MaxConcurrent < 0 {
		return fmt.Errorf("rate_limit.burst and max_concurrent must be nonnegative integers")
	}
	if c.Burst > 0 && c.RequestsPerSecond == 0 {
		return fmt.Errorf("rate_limit.burst requires positive requests_per_second")
	}
	return nil
}

type Rejection struct {
	Endpoint   string `json:"endpoint"`
	Reason     string `json:"reason"`
	RetryAfter int    `json:"retry_after_seconds"`
}

type endpoint struct {
	config  Config
	bucket  *rate.Limiter
	active  int
	enabled bool
}

// Registry survives runtime replacement. Configuration changes preserve tokens
// and active requests; database transactions do not sit on the request path.
type Registry struct {
	mu        sync.Mutex
	endpoints map[string]*endpoint
}

func (r *Registry) Configure(policies map[string]Config) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.endpoints == nil {
		r.endpoints = make(map[string]*endpoint)
	}
	for _, state := range r.endpoints {
		state.enabled = false
	}
	now := time.Now()
	for id, policy := range policies {
		id = strings.ToLower(id)
		state := r.endpoints[id]
		if state == nil {
			state = &endpoint{}
			r.endpoints[id] = state
		}
		if policy.RequestsPerSecond == 0 {
			state.bucket = nil
		} else if state.bucket == nil {
			state.bucket = rate.NewLimiter(rate.Limit(policy.RequestsPerSecond), max(1, policy.Burst))
		} else if state.config != policy {
			state.bucket.SetLimitAt(now, rate.Limit(policy.RequestsPerSecond))
			state.bucket.SetBurstAt(now, max(1, policy.Burst))
		}
		state.config, state.enabled = policy, true
	}
	for id, state := range r.endpoints {
		if !state.enabled && state.active == 0 {
			delete(r.endpoints, id)
		}
	}
}

// Acquire rejects immediately, without queueing or consuming another endpoint's
// allowance on rejection. The returned release must run when the response ends.
func (r *Registry) Acquire(ids []string) (release func(), rejection *Rejection) {
	return r.acquire(ids, time.Now())
}

func (r *Registry) acquire(ids []string, now time.Time) (func(), *Rejection) {
	r.mu.Lock()
	defer r.mu.Unlock()
	selected := make(map[string]*endpoint, len(ids))
	for _, id := range ids {
		id = strings.ToLower(id)
		state := r.endpoints[id]
		if state == nil || !state.enabled {
			continue
		}
		if state.config.MaxConcurrent > 0 && state.active >= state.config.MaxConcurrent {
			return nil, &Rejection{Endpoint: id, Reason: "max_concurrent", RetryAfter: 1}
		}
		if state.bucket != nil && state.bucket.TokensAt(now) < 1 {
			seconds := math.Ceil((1 - state.bucket.TokensAt(now)) / state.config.RequestsPerSecond)
			return nil, &Rejection{Endpoint: id, Reason: "requests_per_second", RetryAfter: int(math.Max(1, math.Min(86400, seconds)))}
		}
		selected[id] = state
	}
	for _, state := range selected {
		if state.bucket != nil {
			state.bucket.AllowN(now, 1)
		}
		state.active++
	}
	return sync.OnceFunc(func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		for id, state := range selected {
			state.active--
			if !state.enabled && state.active == 0 {
				delete(r.endpoints, id)
			}
		}
	}), nil
}
