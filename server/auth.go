package server

import (
	"errors"
	"log"
	"net"
	"net/http"
	"sync"
	"time"

	"Zeus/storage"
)

// KeyStore is the subset of the storage backend needed to authenticate clients.
//
// Declared here as a narrow interface rather than bolted onto storage.Store so
// that agent mode (which is write-only and holds no credentials) is not forced
// to implement authentication methods it can never satisfy.
type KeyStore interface {
	ResolveAPIKey(keyHash string) (storage.APIKeyRecord, error)
	TouchAPIKey(keyHash string) error
}

const (
	// authCacheTTL bounds how long a successful key lookup is reused. Ingest runs
	// on every agent tick, so hitting the database per request would make auth
	// the bottleneck.
	authCacheTTL = 60 * time.Second

	// authNegativeTTL caches failures. Without this, a brute-force attempt turns
	// into one database query per guess.
	authNegativeTTL = 10 * time.Second
)

type authCacheEntry struct {
	tenantID string
	client   string
	valid    bool
	expires  time.Time
}

type authCache struct {
	mu      sync.RWMutex
	entries map[string]authCacheEntry
}

func newAuthCache() *authCache {
	return &authCache{entries: make(map[string]authCacheEntry)}
}

func (c *authCache) get(hash string) (authCacheEntry, bool) {
	c.mu.RLock()
	entry, ok := c.entries[hash]
	c.mu.RUnlock()
	if !ok || time.Now().After(entry.expires) {
		return authCacheEntry{}, false
	}
	return entry, true
}

func (c *authCache) put(hash string, entry authCacheEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Bound memory: an attacker rotating keys would otherwise grow this map
	// without limit. Drop everything and start over rather than track LRU state.
	if len(c.entries) > 10000 {
		c.entries = make(map[string]authCacheEntry)
	}
	c.entries[hash] = entry
}

// invalidate drops a cached entry so a revocation takes effect immediately
// rather than after the TTL.
func (c *authCache) invalidate(hash string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, hash)
}

// authorizeTenant applies controls that must run after successful key
// resolution, including the per-tenant daily ingest quota.
func authorizeTenant(w http.ResponseWriter, r *http.Request, tenant string) (string, bool) {
	if !enforceDailyIngestQuota(w, r, tenant) {
		return "", false
	}
	return tenant, true
}

// authTenant authenticates the request and returns the tenant it may access.
//
// Outside hub mode there is a single local dataset and no tenancy, so it
// returns an empty tenant ID and allows the request.
//
// On failure it has already written the response; the caller must simply return.
func (s *Server) authTenant(w http.ResponseWriter, r *http.Request) (string, bool) {
	if !s.hubMode {
		return "", true
	}

	// Keys are header-only. Accepting them as a query parameter meant every
	// request logged a working credential into access logs and browser history.
	if r.URL.Query().Get("api_key") != "" {
		writeJSONError(w, http.StatusUnauthorized, "api_key query parameter is not accepted; send the key in the X-API-Key header")
		return "", false
	}

	presented := r.Header.Get("X-API-Key")
	if presented == "" {
		writeJSONError(w, http.StatusUnauthorized, "X-API-Key header required")
		return "", false
	}

	if s.keys == nil {
		// Fail closed. An unconfigured key store previously meant "accept
		// everything", which is exactly the bug being fixed.
		log.Printf("[server] hub mode enabled without a key store — rejecting request from %s", r.RemoteAddr)
		writeJSONError(w, http.StatusServiceUnavailable, "authentication unavailable")
		return "", false
	}

	hash := storage.HashAPIKey(presented)

	if entry, ok := s.authCache.get(hash); ok {
		if !entry.valid {
			writeJSONError(w, http.StatusUnauthorized, "invalid API key")
			return "", false
		}
		return authorizeTenant(w, r, entry.tenantID)
	}

	rec, err := s.keys.ResolveAPIKey(hash)
	if err != nil {
		if errors.Is(err, storage.ErrAPIKeyNotFound) {
			s.authCache.put(hash, authCacheEntry{valid: false, expires: time.Now().Add(authNegativeTTL)})
			log.Printf("[server] rejected unknown API key from %s", clientIP(r))
			writeJSONError(w, http.StatusUnauthorized, "invalid API key")
			return "", false
		}
		// A database failure must not be reported as an auth failure, or operators
		// will spend the outage hunting for a credential problem.
		log.Printf("[server] API key lookup failed: %v", err)
		writeJSONError(w, http.StatusServiceUnavailable, "authentication temporarily unavailable")
		return "", false
	}

	s.authCache.put(hash, authCacheEntry{
		tenantID: rec.TenantID,
		client:   rec.ClientName,
		valid:    true,
		expires:  time.Now().Add(authCacheTTL),
	})

	// Best-effort usage tracking; never block or fail a valid request on it.
	go func() {
		if err := s.keys.TouchAPIKey(hash); err != nil {
			log.Printf("[server] touch api key: %v", err)
		}
	}()

	return authorizeTenant(w, r, rec.TenantID)
}

// clientIP extracts the source address, preferring the proxy-provided value when
// the server is configured to trust one.
func clientIP(r *http.Request) string {
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}
