package server

import (
	"testing"
	"time"
)

func TestRateLimiterAllowsBurstThenBlocks(t *testing.T) {
	rl := newRateLimiter(10, 5)

	for i := 0; i < 5; i++ {
		if !rl.allow("tenant-a") {
			t.Fatalf("request %d within burst was blocked", i+1)
		}
	}
	if rl.allow("tenant-a") {
		t.Error("request beyond burst was allowed")
	}
}

// One noisy tenant must not be able to deny service to another.
func TestRateLimiterIsolatesKeys(t *testing.T) {
	rl := newRateLimiter(10, 3)

	for i := 0; i < 3; i++ {
		rl.allow("noisy")
	}
	if rl.allow("noisy") {
		t.Fatal("noisy tenant should be throttled by now")
	}

	if !rl.allow("quiet") {
		t.Error("a second tenant was throttled by the first tenant's traffic")
	}
}

func TestRateLimiterRefills(t *testing.T) {
	// 100/s means a token every 10ms.
	rl := newRateLimiter(100, 2)

	rl.allow("k")
	rl.allow("k")
	if rl.allow("k") {
		t.Fatal("bucket should be empty")
	}

	time.Sleep(60 * time.Millisecond)

	if !rl.allow("k") {
		t.Error("bucket did not refill over time")
	}
}

func TestRateLimiterEmptyKeyDoesNotPanic(t *testing.T) {
	rl := newRateLimiter(10, 2)
	if !rl.allow("") {
		t.Error("first anonymous request should be allowed")
	}
}

func TestRateLimiterDefaults(t *testing.T) {
	rl := newRateLimiter(0, 0)
	if rl.rate != defaultRatePerSecond {
		t.Errorf("rate = %v, want default %v", rl.rate, defaultRatePerSecond)
	}
	if rl.burst != defaultBurst {
		t.Errorf("burst = %v, want default %v", rl.burst, defaultBurst)
	}
}

func TestRetryAfterIsAtLeastOneSecond(t *testing.T) {
	if got := newRateLimiter(1000, 10).retryAfter(); got < time.Second {
		t.Errorf("retryAfter() = %v, want >= 1s (Retry-After is expressed in whole seconds)", got)
	}
}

func TestSweepReclaimsIdleBuckets(t *testing.T) {
	rl := newRateLimiter(100, 10)
	rl.allow("old-client")

	rl.mu.Lock()
	rl.buckets["old-client"].last = time.Now().Add(-time.Hour)
	rl.lastSweep = time.Now().Add(-time.Hour)
	rl.mu.Unlock()

	rl.allow("new-client")

	rl.mu.Lock()
	_, stillThere := rl.buckets["old-client"]
	rl.mu.Unlock()

	if stillThere {
		t.Error("idle bucket was not reclaimed; map grows without bound")
	}
}
