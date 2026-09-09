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
type KeyStore interface {
	ResolveAPIKey(keyHash string) (storage.APIKeyRecord, error)
	TouchAPIKey(keyHash string) error
}

// AdminStore is the full credential and provisioning management interface.
type AdminStore interface {
	KeyStore
	ConsumeEnrollCode(codeHash string) (storage.APIKeyRecord, error)
	UpsertAPIKey(rec storage.APIKeyRecord) error
	RevokeAgent(tenantID, serverID string) (int64, error)
	ListAgents(tenantID string) ([]storage.APIKeyRecord, error)
	RevokeAPIKey(clientName string) (int64, error)
	RevokeKeyHash(keyHash string) (int64, error)
	ListAPIKeys() ([]storage.APIKeyRecord, error)
	CompleteEnrollment(req storage.EnrollmentRequest) (storage.EnrollmentResult, error)
	ReplaceAgent(tenantID, serverID, token, tokenHash, prefix string) (storage.EnrollmentResult, error)
	ActiveAgentHashes(tenantID, serverID string) ([]string, error)
	AuthEpoch() (int64, error)
}

const (
	// authCacheTTL bounds how long a successful key lookup is reused. Ingest runs
	// on every agent tick, so hitting the database per request would make auth
	// the bottleneck. Credential expiry always shortens this (V19).
	authCacheTTL = 60 * time.Second

	// authNegativeTTL caches failures. Without this, a brute-force attempt turns
	// into one database query per guess.
	authNegativeTTL = 10 * time.Second

	// AuthRevocationMaxDelay is the documented maximum replica lag before a
	// warmed positive cache is compared to auth_epoch. Local revoke/rotate
	// invalidates immediately. Re-export of storage.AuthRevocationMaxDelay.
	AuthRevocationMaxDelay = storage.AuthRevocationMaxDelay
)

// Principal is the authenticated actor for a request. Identity is never taken
// from client display names or payload tenant/server fields.
type Principal struct {
	TenantID     string
	AgentID      string
	CredentialID string
	Kind         string
	Permissions  string
	ExpiresAt    time.Time
}

type authCacheEntry struct {
	principal Principal
	valid     bool
	expires   time.Time
	epoch     int64
}

type authCache struct {
	mu      sync.RWMutex
	entries map[string]authCacheEntry
}

func credentialExpiryUnix(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

func cacheDeadline(now time.Time, credExpiry time.Time) time.Time {
	deadline := now.Add(authCacheTTL)
	exp := credentialExpiryUnix(credExpiry)
	if exp > 0 && credExpiry.Before(deadline) {
		return credExpiry
	}
	return deadline
}

func newAuthCache() *authCache {
	return &authCache{entries: make(map[string]authCacheEntry)}
}

func (c *authCache) get(hash string, epoch int64) (authCacheEntry, bool) {
	c.mu.RLock()
	entry, ok := c.entries[hash]
	c.mu.RUnlock()
	now := time.Now()
	if !ok || now.After(entry.expires) {
		return authCacheEntry{}, false
	}
	if entry.epoch < epoch {
		c.invalidate(hash)
		return authCacheEntry{}, false
	}
	if exp := credentialExpiryUnix(entry.principal.ExpiresAt); exp > 0 && !now.Before(entry.principal.ExpiresAt) {
		c.invalidate(hash)
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

func principalFromRecord(rec storage.APIKeyRecord) Principal {
	return Principal{
		TenantID:     rec.TenantID,
		AgentID:      rec.ServerID,
		CredentialID: rec.KeyHash,
		Kind:         rec.Kind,
		Permissions:  rec.Scope,
		ExpiresAt:    rec.ExpiresAt,
	}
}

// credentialPermits is fail-closed: enrollment codes never authenticate general
// routes, and legacy/all never satisfy platform administration.
func credentialPermits(kind, granted, required string) bool {
	if kind == storage.KindEnroll || granted == storage.ScopeEnroll {
		return false
	}
	if required == storage.ScopeAdmin {
		return kind == storage.KindAdmin && granted == storage.ScopeAdmin
	}
	if granted == storage.ScopeAdmin && kind == storage.KindAdmin {
		return true
	}
	switch required {
	case storage.ScopeRead:
		return granted == storage.ScopeRead || granted == storage.ScopeAll
	case storage.ScopeIngest:
		return granted == storage.ScopeIngest || granted == storage.ScopeAll
	default:
		return granted == required
	}
}

func (p Principal) permits(required string) bool {
	return credentialPermits(p.Kind, p.Permissions, required)
}

// authorizeTenant applies controls that must run after successful key
// resolution, including the per-tenant daily ingest quota.
func authorizeTenant(w http.ResponseWriter, r *http.Request, tenant string) (string, bool) {
	if !enforceDailyIngestQuota(w, r, tenant) {
		return "", false
	}
	return tenant, true
}

// authTenant authenticates the request with default read scope for backward compatibility.
func (s *Server) authTenant(w http.ResponseWriter, r *http.Request) (string, bool) {
	p, ok := s.authPrincipal(w, r, storage.ScopeRead)
	return p.TenantID, ok
}

// authTenantScope authenticates the request and enforces the required scope.
func (s *Server) authTenantScope(w http.ResponseWriter, r *http.Request, requiredScope string) (string, bool) {
	p, ok := s.authPrincipal(w, r, requiredScope)
	return p.TenantID, ok
}

// authPrincipal authenticates the request and returns a typed principal.
func (s *Server) authPrincipal(w http.ResponseWriter, r *http.Request, requiredScope string) (Principal, bool) {
	if !s.hubMode {
		return Principal{TenantID: storage.LocalTenantID, Kind: storage.KindRead, Permissions: storage.ScopeAll}, true
	}

	// Keys are header or session-cookie only. Accepting them as a query parameter
	// meant every request logged a working credential into access logs and browser history.
	if r.URL.Query().Get("api_key") != "" {
		writeJSONError(w, http.StatusUnauthorized, "api_key query parameter is not accepted; send the key in the X-API-Key header or session cookie")
		return Principal{}, false
	}

	if r.Header.Get("X-API-Key") == "" {
		if _, err := r.Cookie(sessionCookieName); err == nil {
			switch r.Method {
			case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
				if !s.originOK(r) {
					writeJSONError(w, http.StatusForbidden, "cross-site request rejected")
					return Principal{}, false
				}
			}
		}
	}

	presented := r.Header.Get("X-API-Key")
	if presented == "" {
		ent, ok := s.lookupSession(r)
		if ok {
			if !ent.principal.permits(requiredScope) {
				writeJSONError(w, http.StatusForbidden, "insufficient credential scope")
				return Principal{}, false
			}
			if _, ok := authorizeTenant(w, r, ent.principal.TenantID); !ok {
				return Principal{}, false
			}
			return ent.principal, true
		}
	}

	if presented == "" {
		writeJSONError(w, http.StatusUnauthorized, "X-API-Key header or session cookie required")
		return Principal{}, false
	}

	if s.keys == nil {
		log.Printf("[server] hub mode enabled without a key store — rejecting request from %s", r.RemoteAddr)
		writeJSONError(w, http.StatusServiceUnavailable, "authentication unavailable")
		return Principal{}, false
	}

	hash := storage.HashAPIKey(presented)
	epoch := s.currentAuthEpoch()

	if entry, ok := s.authCache.get(hash, epoch); ok {
		if !entry.valid {
			writeJSONError(w, http.StatusUnauthorized, "invalid API key")
			return Principal{}, false
		}
		if !entry.principal.permits(requiredScope) {
			log.Printf("[server] forbidden: credential %s has kind %q scope %q, needs %q", presented[:min(4, len(presented))], entry.principal.Kind, entry.principal.Permissions, requiredScope)
			writeJSONError(w, http.StatusForbidden, "insufficient credential scope")
			return Principal{}, false
		}
		if _, ok := authorizeTenant(w, r, entry.principal.TenantID); !ok {
			return Principal{}, false
		}
		return entry.principal, true
	}

	rec, err := s.keys.ResolveAPIKey(hash)
	if err != nil {
		if errors.Is(err, storage.ErrAPIKeyNotFound) {
			s.authCache.put(hash, authCacheEntry{valid: false, expires: time.Now().Add(authNegativeTTL), epoch: epoch})
			log.Printf("[server] rejected unknown or expired API key from %s", clientIP(r))
			writeJSONError(w, http.StatusUnauthorized, "invalid API key")
			return Principal{}, false
		}
		log.Printf("[server] API key lookup failed: %v", err)
		writeJSONError(w, http.StatusServiceUnavailable, "authentication temporarily unavailable")
		return Principal{}, false
	}

	p := principalFromRecord(rec)
	now := time.Now()
	s.authCache.put(hash, authCacheEntry{
		principal: p,
		valid:     true,
		expires:   cacheDeadline(now, p.ExpiresAt),
		epoch:     epoch,
	})

	if !p.permits(requiredScope) {
		log.Printf("[server] forbidden: client %q kind %q has scope %q, needs %q", rec.ClientName, rec.Kind, rec.Scope, requiredScope)
		writeJSONError(w, http.StatusForbidden, "insufficient credential scope")
		return Principal{}, false
	}

	// Best-effort usage tracking; never block or fail a valid request on it.
	go func() {
		if err := s.keys.TouchAPIKey(hash); err != nil {
			log.Printf("[server] touch api key: %v", err)
		}
	}()

	if _, ok := authorizeTenant(w, r, p.TenantID); !ok {
		return Principal{}, false
	}
	return p, true
}

// clientIP extracts the source address, preferring the proxy-provided value when
// the server is configured to trust one.
func clientIP(r *http.Request) string {
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

// SetAuthEpochInterval overrides replica epoch refresh (tests use 0 = always).
func (s *Server) SetAuthEpochInterval(d time.Duration) {
	s.epochMu.Lock()
	s.epochInterval = d
	s.epochMu.Unlock()
}

// SyncAuthEpoch reloads auth_epoch from storage. Tests call this after a
// sibling replica mutates credentials.
func (s *Server) SyncAuthEpoch() {
	s.syncAuthEpoch()
}

func (s *Server) currentAuthEpoch() int64 {
	s.epochMu.Lock()
	interval := s.epochInterval
	refreshed := s.epochRefreshed
	cached := s.authEpoch
	s.epochMu.Unlock()
	if interval == 0 || refreshed.IsZero() || time.Since(refreshed) >= interval {
		s.syncAuthEpoch()
		s.epochMu.Lock()
		cached = s.authEpoch
		s.epochMu.Unlock()
	}
	return cached
}

func (s *Server) syncAuthEpoch() {
	es, ok := s.keys.(interface{ AuthEpoch() (int64, error) })
	if !ok {
		return
	}
	ep, err := es.AuthEpoch()
	if err != nil {
		return
	}
	s.epochMu.Lock()
	s.authEpoch = ep
	s.epochRefreshed = time.Now()
	s.epochMu.Unlock()
}

func (s *Server) invalidateHashes(hashes []string) {
	for _, h := range hashes {
		s.authCache.invalidate(h)
	}
	s.syncAuthEpoch()
}
