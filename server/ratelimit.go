package server

import (
	"sync"
	"time"
)

// Rate limits. The hub accepts one batch per agent per 10s interval in normal
// operation, so these ceilings are generous for legitimate traffic while still
// capping how fast a single client can fill the database.
const (
	defaultRatePerSecond = 20.0
	defaultBurst         = 60.0
)

type tokenBucket struct {
	tokens float64
	last   time.Time
}

// rateLimiter is a per-key token bucket.
//
// Hand-rolled deliberately: golang.org/x/time/rate would do this better, but
// adding a module dependency for ~40 lines is not worth the supply-chain and
// vendoring cost in a single-binary tool.
type rateLimiter struct {
	mu        sync.Mutex
	buckets   map[string]*tokenBucket
	rate      float64
	burst     float64
	lastSweep time.Time
}

func newRateLimiter(ratePerSecond, burst float64) *rateLimiter {
	if ratePerSecond <= 0 {
		ratePerSecond = defaultRatePerSecond
	}
	if burst <= 0 {
		burst = defaultBurst
	}
	return &rateLimiter{
		buckets:   make(map[string]*tokenBucket),
		rate:      ratePerSecond,
		burst:     burst,
		lastSweep: time.Now(),
	}
}

// allow consumes one token for key, reporting whether the request may proceed.
func (rl *rateLimiter) allow(key string) bool {
	if key == "" {
		key = "anonymous"
	}

	now := time.Now()

	rl.mu.Lock()
	defer rl.mu.Unlock()

	rl.sweepLocked(now)

	b, ok := rl.buckets[key]
	if !ok {
		rl.buckets[key] = &tokenBucket{tokens: rl.burst - 1, last: now}
		return true
	}

	// Refill proportionally to elapsed time.
	elapsed := now.Sub(b.last).Seconds()
	if elapsed > 0 {
		b.tokens += elapsed * rl.rate
		if b.tokens > rl.burst {
			b.tokens = rl.burst
		}
		b.last = now
	}

	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// retryAfter reports how long the caller should wait before retrying.
func (rl *rateLimiter) retryAfter() time.Duration {
	if rl.rate <= 0 {
		return time.Second
	}
	d := time.Duration(float64(time.Second) / rl.rate)
	if d < time.Second {
		return time.Second
	}
	return d
}

// sweepLocked discards buckets that have refilled completely.
//
// Without this, a client rotating through many keys or source IPs would grow the
// map without bound: a memory leak reachable by an unauthenticated caller.
func (rl *rateLimiter) sweepLocked(now time.Time) {
	if now.Sub(rl.lastSweep) < time.Minute {
		return
	}
	rl.lastSweep = now

	fullAfter := time.Duration(rl.burst/rl.rate*float64(time.Second)) + time.Minute
	for key, b := range rl.buckets {
		if now.Sub(b.last) > fullAfter {
			delete(rl.buckets, key)
		}
	}
}
