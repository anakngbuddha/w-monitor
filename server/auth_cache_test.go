package server

import (
	"testing"
	"time"

	"Zeus/storage"
)

func TestAuthCacheBoundedByCredentialExpiry(t *testing.T) {
	c := newAuthCache()
	now := time.Now()
	exp := now.Add(50 * time.Millisecond)
	hash := storage.HashAPIKey("expiring-token")
	c.put(hash, authCacheEntry{
		principal: Principal{TenantID: "t", ExpiresAt: exp, Kind: storage.KindRead, Permissions: storage.ScopeRead},
		valid:     true,
		expires:   cacheDeadline(now, exp),
		epoch:     1,
	})
	if _, ok := c.get(hash, 1); !ok {
		t.Fatal("expected cache hit before expiry")
	}
	time.Sleep(80 * time.Millisecond)
	if _, ok := c.get(hash, 1); ok {
		t.Fatal("cache accepted credential after expiry")
	}
}

func TestAuthCacheInvalidateDropsWarmedEntry(t *testing.T) {
	c := newAuthCache()
	hash := storage.HashAPIKey("warm")
	c.put(hash, authCacheEntry{
		principal: Principal{TenantID: "t", Kind: storage.KindRead, Permissions: storage.ScopeRead},
		valid:     true,
		expires:   time.Now().Add(time.Minute),
		epoch:     1,
	})
	c.invalidate(hash)
	if _, ok := c.get(hash, 1); ok {
		t.Fatal("invalidated entry still served")
	}
}

func TestAuthCacheMissesOnNewerEpoch(t *testing.T) {
	c := newAuthCache()
	hash := storage.HashAPIKey("epoch")
	c.put(hash, authCacheEntry{
		principal: Principal{TenantID: "t", Kind: storage.KindRead, Permissions: storage.ScopeRead},
		valid:     true,
		expires:   time.Now().Add(time.Minute),
		epoch:     1,
	})
	if _, ok := c.get(hash, 2); ok {
		t.Fatal("stale epoch cache hit")
	}
}

func TestCacheDeadlineShortensToExpiry(t *testing.T) {
	now := time.Now()
	exp := now.Add(2 * time.Second)
	got := cacheDeadline(now, exp)
	if !got.Equal(exp) {
		t.Fatalf("deadline = %s, want credential expiry %s", got, exp)
	}
	never := time.Unix(0, 0)
	got = cacheDeadline(now, never)
	if got.Before(now.Add(authCacheTTL - time.Second)) {
		t.Fatalf("zero expiry should keep default TTL, got %s", got)
	}
}
