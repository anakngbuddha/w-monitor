package server

import (
	"strings"
	"sync"
	"time"
)

const (
	defaultRatePerSecond = 20.0
	defaultBurst = 60.0
	maxRateBuckets = 10000
)

type tokenBucket struct { tokens float64; last time.Time }
type rateLimiter struct { mu sync.Mutex; buckets map[string]*tokenBucket; rate float64; burst float64; lastSweep time.Time }
func newRateLimiter(rate, burst float64) *rateLimiter {
	if rate <= 0 { rate = defaultRatePerSecond }
	if burst <= 0 { burst = defaultBurst }
	return &rateLimiter{buckets: make(map[string]*tokenBucket), rate: rate, burst: burst, lastSweep: time.Now()}
}

func (rl *rateLimiter) allow(key string) bool {
	if key == "" { key = "anonymous" }
	now := time.Now()
	rl.mu.Lock()
	defer rl.mu.Unlock()
	rl.sweepLocked(now)
	rate, burst := rl.rate, rl.burst
	// A shared reverse-proxy peer is an abuse-control domain, not a tenant or
	// agent entitlement. Leave headroom for a pilot fleet behind one proxy;
	// authenticated per-agent buckets and durable data budgets stay unchanged.
	if strings.HasPrefix(key, "preauth:") { rate *= 100; burst *= 100 }
	bucket, ok := rl.buckets[key]
	if !ok {
		if len(rl.buckets) >= maxRateBuckets { return false }
		rl.buckets[key] = &tokenBucket{tokens: burst-1, last: now}
		return true
	}
	elapsed := now.Sub(bucket.last).Seconds()
	if elapsed > 0 { bucket.tokens += elapsed*rate; if bucket.tokens > burst { bucket.tokens = burst }; bucket.last = now }
	if bucket.tokens < 1 { return false }
	bucket.tokens--
	return true
}
func (rl *rateLimiter) retryAfter() time.Duration {
	if rl.rate <= 0 { return time.Second }
	wait := time.Duration(float64(time.Second)/rl.rate)
	if wait < time.Second { return time.Second }
	return wait
}
func (rl *rateLimiter) sweepLocked(now time.Time) {
	if now.Sub(rl.lastSweep) < time.Minute { return }
	rl.lastSweep = now
	idle := time.Duration(rl.burst/rl.rate*float64(time.Second))+time.Minute
	for key, bucket := range rl.buckets { if now.Sub(bucket.last) > idle { delete(rl.buckets, key) } }
}
