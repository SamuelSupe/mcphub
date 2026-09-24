package ratelimit

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestTokenBudgetIsolationReloadAndAtomicAdmission(t *testing.T) {
	var registry Registry
	policies := map[string]Config{"Alpha": {RequestsPerSecond: 1, Burst: 2}, "beta": {RequestsPerSecond: 1, Burst: 1}}
	registry.Configure(policies)
	now := time.Now()
	for range 2 {
		release, rejected := registry.acquire([]string{"alpha", "ALPHA"}, now)
		if rejected != nil {
			t.Fatal(rejected)
		}
		release()
	}
	registry.Configure(policies)
	if _, rejected := registry.acquire([]string{"beta", "alpha"}, now); rejected == nil || rejected.Endpoint != "alpha" || rejected.RetryAfter < 1 {
		t.Fatalf("reload replenished an exhausted endpoint: %+v", rejected)
	}
	release, rejected := registry.acquire([]string{"beta"}, now)
	if rejected != nil {
		t.Fatalf("rejected multi-endpoint request consumed beta's budget: %+v", rejected)
	}
	release()
	release, rejected = registry.acquire([]string{"alpha"}, now.Add(time.Second))
	if rejected != nil {
		t.Fatalf("tokens did not refill: %+v", rejected)
	}
	release()
}

func TestConcurrentAdmissionAndLivePolicyChanges(t *testing.T) {
	var registry Registry
	registry.Configure(map[string]Config{"alpha": {}})
	var releases []func()
	for range 50 {
		release, rejected := registry.Acquire([]string{"alpha"})
		if rejected != nil {
			t.Fatal("default policy limited requests")
		}
		releases = append(releases, release)
	}
	registry.Configure(map[string]Config{"alpha": {MaxConcurrent: 2}})
	if _, rejected := registry.Acquire([]string{"alpha"}); rejected == nil || rejected.Reason != "max_concurrent" {
		t.Fatal("enabling a concurrency limit forgot in-flight requests")
	}
	for _, release := range releases {
		release()
		release()
	}
	var admitted atomic.Int32
	var wg sync.WaitGroup
	held := make(chan func(), 20)
	for range 20 {
		wg.Go(func() {
			release, rejected := registry.Acquire([]string{"alpha"})
			if rejected == nil {
				admitted.Add(1)
				held <- release
			}
		})
	}
	wg.Wait()
	close(held)
	if admitted.Load() != 2 {
		t.Fatalf("admitted %d concurrent requests, want 2", admitted.Load())
	}
	registry.Configure(map[string]Config{"alpha": {MaxConcurrent: 1}})
	if _, rejected := registry.Acquire([]string{"alpha"}); rejected == nil {
		t.Fatal("policy edit forgot active requests")
	}
	for release := range held {
		release()
	}
	release, rejected := registry.Acquire([]string{"alpha"})
	if rejected != nil {
		t.Fatal("release did not restore concurrency slot")
	}
	registry.Configure(nil)
	release()
	if len(registry.endpoints) != 0 {
		t.Fatal("removed endpoint retained idle state")
	}
}
