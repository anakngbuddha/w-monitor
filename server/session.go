package server

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"Zeus/storage"
)

const (
	sessionCookieName     = "wmonitor_session"
	sessionTTL            = 7 * 24 * time.Hour
	sessionIDPrefix       = "wms_"
	maxSessions           = 4096
	maxCredentialSessions = 5
)

type sessionRequest struct {
	ReadToken string `json:"read_token"`
}

type sessionResponse struct {
	ClientName string `json:"client_name"`
	TenantID   string `json:"tenant_id"`
}

type sessionEntry struct {
	credHash   string
	principal  Principal
	clientName string
	expires    time.Time
}

type sessionStore struct {
	mu sync.Mutex
	m  map[string]sessionEntry
}

func newSessionStore() *sessionStore {
	return &sessionStore{m: make(map[string]sessionEntry)}
}

func newSessionID() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return sessionIDPrefix + hex.EncodeToString(b), nil
}

// put rejects exhaustion rather than evicting another tenant's live session.
// Expired entries are reclaimed at every insertion. A credential may have five
// concurrent browser sessions; signing in again replaces its oldest one.
func (st *sessionStore) put(id string, ent sessionEntry) bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	now := time.Now()
	oldest := ""
	var oldestExpiry time.Time
	count := 0
	for key, value := range st.m {
		if !now.Before(value.expires) {
			delete(st.m, key)
			continue
		}
		if value.credHash == ent.credHash {
			count++
			if oldest == "" || value.expires.Before(oldestExpiry) {
				oldest, oldestExpiry = key, value.expires
			}
		}
	}
	if count >= maxCredentialSessions {
		delete(st.m, oldest)
	}
	if len(st.m) >= maxSessions {
		return false
	}
	st.m[id] = ent
	return true
}

func (st *sessionStore) get(id string) (sessionEntry, bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	ent, ok := st.m[id]
	if !ok || !time.Now().Before(ent.expires) {
		delete(st.m, id)
		return sessionEntry{}, false
	}
	return ent, true
}

func (st *sessionStore) delete(id string) {
	st.mu.Lock()
	delete(st.m, id)
	st.mu.Unlock()
}

func loopbackSessionRequest(r *http.Request) bool {
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	peer := net.ParseIP(clientIP(r))
	ip := net.ParseIP(host)
	return peer != nil && peer.IsLoopback() && (host == "localhost" || (ip != nil && ip.IsLoopback())) && !trustedProxyPeer(r)
}

func (s *Server) setSessionCookie(w http.ResponseWriter, r *http.Request, value string, maxAge int) {
	cookie := &http.Cookie{
		Name: sessionCookieName, Value: value, Path: "/",
		HttpOnly: true, Secure: !loopbackSessionRequest(r) || requestIsHTTPS(r),
		SameSite: http.SameSiteStrictMode, MaxAge: maxAge,
	}
	if maxAge < 0 {
		cookie.Expires = time.Unix(1, 0)
	}
	http.SetCookie(w, cookie)
}

// Session CSRF checks compare the entire origin, not merely the hostname.
// The API CORS allowlist does not authorize cross-site session mutations.
func (s *Server) originOK(r *http.Request) bool {
	raw := r.Header.Get("Origin")
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return false
	}
	scheme := "http"
	if requestIsHTTPS(r) {
		scheme = "https"
	}
	return u.Scheme == scheme && strings.EqualFold(u.Host, r.Host)
}

func (s *Server) requireSameOrigin(w http.ResponseWriter, r *http.Request) bool {
	if s.originOK(r) {
		return true
	}
	writeJSONError(w, http.StatusForbidden, "cross-site request rejected")
	return false
}

func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json")
	switch r.Method {
	case http.MethodGet:
		ent, ok := s.lookupSession(r)
		if !ok {
			writeJSONError(w, http.StatusUnauthorized, "no session")
			return
		}
		json.NewEncoder(w).Encode(sessionResponse{ClientName: ent.clientName, TenantID: ent.principal.TenantID})
	case http.MethodPost:
		if !s.requireSameOrigin(w, r) {
			return
		}
		if s.loginLimiter != nil && !s.loginLimiter.allow("login:"+clientIP(r)) {
			w.Header().Set("Retry-After", "1")
			writeJSONError(w, http.StatusTooManyRequests, "too many login attempts")
			return
		}
		if !requestIsHTTPS(r) && !loopbackSessionRequest(r) {
			writeJSONError(w, http.StatusForbidden, "HTTPS is required for browser login")
			return
		}
		var req sessionRequest
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&req); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid login request")
			return
		}
		if err := dec.Decode(&struct{}{}); err != io.EOF {
			writeJSONError(w, http.StatusBadRequest, "exactly one JSON object is required")
			return
		}
		// Credentials are opaque: do not normalize or fall back to a header
		// after a malformed body. In particular, never log the request body.
		if req.ReadToken == "" || len(req.ReadToken) > 1024 {
			writeJSONError(w, http.StatusBadRequest, "read_token is required")
			return
		}
		if s.keys == nil || s.sessions == nil {
			writeJSONError(w, http.StatusServiceUnavailable, "authentication unavailable")
			return
		}
		hash := storage.HashAPIKey(req.ReadToken)
		rec, err := s.keys.ResolveAPIKey(hash)
		if err != nil || rec.Revoked || (credentialExpiryUnix(rec.ExpiresAt) > 0 && !time.Now().Before(rec.ExpiresAt)) {
			writeJSONError(w, http.StatusUnauthorized, "invalid read token")
			return
		}
		if !credentialPermits(rec.Kind, rec.Scope, storage.ScopeRead) || rec.Kind == storage.KindAgent {
			writeJSONError(w, http.StatusForbidden, "token lacks browser read scope")
			return
		}
		id, err := newSessionID()
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "failed to create session")
			return
		}
		expires := time.Now().Add(sessionTTL)
		if credentialExpiryUnix(rec.ExpiresAt) > 0 && rec.ExpiresAt.Before(expires) {
			expires = rec.ExpiresAt
		}
		if !s.sessions.put(id, sessionEntry{credHash: hash, principal: principalFromRecord(rec), clientName: rec.ClientName, expires: expires}) {
			writeJSONError(w, http.StatusServiceUnavailable, "browser session capacity reached")
			return
		}
		if old, err := r.Cookie(sessionCookieName); err == nil {
			s.sessions.delete(old.Value)
		}
		maxAge := int(time.Until(expires).Seconds())
		if maxAge < 1 {
			maxAge = 1
		}
		s.setSessionCookie(w, r, id, maxAge)
		if err := s.recordAudit(storage.AuditEvent{TenantID: rec.TenantID, ActorKind: rec.Kind, ActorPrefix: rec.KeyPrefix, Action: "session.login", TargetType: "session", TargetID: "browser"}); err != nil {
			s.sessions.delete(id)
			s.setSessionCookie(w, r, "", -1)
			writeJSONError(w, http.StatusServiceUnavailable, "audit unavailable")
			return
		}
		json.NewEncoder(w).Encode(sessionResponse{ClientName: rec.ClientName, TenantID: rec.TenantID})
	case http.MethodDelete:
		if !s.requireSameOrigin(w, r) {
			return
		}
		if c, err := r.Cookie(sessionCookieName); err == nil && s.sessions != nil {
			if ent, ok := s.sessions.get(c.Value); ok {
				if err := s.recordAudit(storage.AuditEvent{TenantID: ent.principal.TenantID, ActorKind: ent.principal.Kind, ActorPrefix: s.actorPrefix(ent.principal.Kind, ent.credHash), Action: "session.logout", TargetType: "session", TargetID: "browser"}); err != nil {
					writeJSONError(w, http.StatusServiceUnavailable, "audit unavailable")
					return
				}
			}
			s.sessions.delete(c.Value)
		}
		s.setSessionCookie(w, r, "", -1)
		json.NewEncoder(w).Encode(map[string]string{"status": "logged_out"})
	default:
		w.Header().Set("Allow", "GET, POST, DELETE")
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// Human sessions are checked against the registry on every request. Replica
// restart logs browsers out; credential revocation does not wait for cache TTL.
func (s *Server) lookupSession(r *http.Request) (sessionEntry, bool) {
	c, err := r.Cookie(sessionCookieName)
	if err != nil || len(c.Value) != len(sessionIDPrefix)+64 || !strings.HasPrefix(c.Value, sessionIDPrefix) || s.sessions == nil || s.keys == nil {
		return sessionEntry{}, false
	}
	ent, ok := s.sessions.get(c.Value)
	if !ok {
		return sessionEntry{}, false
	}
	rec, err := s.keys.ResolveAPIKey(ent.credHash)
	if err != nil || rec.Revoked || rec.TenantID != ent.principal.TenantID || rec.Kind == storage.KindAgent || !credentialPermits(rec.Kind, rec.Scope, storage.ScopeRead) || (credentialExpiryUnix(rec.ExpiresAt) > 0 && !time.Now().Before(rec.ExpiresAt)) {
		s.sessions.delete(c.Value)
		return sessionEntry{}, false
	}
	ent.principal = principalFromRecord(rec)
	ent.clientName = rec.ClientName
	return ent, true
}
