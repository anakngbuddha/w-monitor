package server

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"Zeus/storage"
)

const (
	sessionCookieName = "wmonitor_session"
	sessionTTL        = 7 * 24 * time.Hour
	sessionIDPrefix   = "wms_"
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

func (st *sessionStore) put(id string, ent sessionEntry) {
	st.mu.Lock()
	st.m[id] = ent
	st.mu.Unlock()
}

func (st *sessionStore) get(id string) (sessionEntry, bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	ent, ok := st.m[id]
	if !ok || time.Now().After(ent.expires) {
		if ok {
			delete(st.m, id)
		}
		return sessionEntry{}, false
	}
	return ent, true
}

func (st *sessionStore) delete(id string) {
	st.mu.Lock()
	delete(st.m, id)
	st.mu.Unlock()
}

func (s *Server) setSessionCookie(w http.ResponseWriter, r *http.Request, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    value,
		Path:     "/",
		HttpOnly: true,
		Secure:   requestIsHTTPS(r),
		SameSite: http.SameSiteStrictMode,
		MaxAge:   maxAge,
	})
}

func (s *Server) originOK(r *http.Request) bool {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return false
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	if strings.EqualFold(u.Host, r.Host) {
		return true
	}
	for _, allowed := range s.allowedOrigins {
		if allowed != "" && strings.EqualFold(strings.TrimRight(allowed, "/"), strings.TrimRight(origin, "/")) {
			return true
		}
	}
	return false
}

func (s *Server) requireSameOrigin(w http.ResponseWriter, r *http.Request) bool {
	if s.originOK(r) {
		return true
	}
	writeJSONError(w, http.StatusForbidden, "cross-site request rejected")
	return false
}

// handleSession manages opaque server-side dashboard sessions (HttpOnly cookie).
func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	s.writeCORS(w, r)
	switch r.Method {
	case http.MethodGet:
		ent, ok := s.lookupSession(r)
		if !ok {
			writeJSONError(w, http.StatusUnauthorized, "no session")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(sessionResponse{
			ClientName: ent.clientName,
			TenantID:   ent.principal.TenantID,
		})

	case http.MethodPost:
		if !s.requireSameOrigin(w, r) {
			return
		}
		if s.loginLimiter != nil && !s.loginLimiter.allow("login:"+clientIP(r)) {
			w.Header().Set("Retry-After", "1")
			writeJSONError(w, http.StatusTooManyRequests, "too many login attempts")
			return
		}

		var req sessionRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
			req.ReadToken = r.Header.Get("X-API-Key")
		}
		rawToken := strings.TrimSpace(req.ReadToken)
		if rawToken == "" {
			writeJSONError(w, http.StatusBadRequest, "read_token is required")
			return
		}
		if s.keys == nil {
			writeJSONError(w, http.StatusServiceUnavailable, "authentication unavailable")
			return
		}

		hash := storage.HashAPIKey(rawToken)
		rec, err := s.keys.ResolveAPIKey(hash)
		if err != nil {
			writeJSONError(w, http.StatusUnauthorized, "invalid read token")
			return
		}
		if rec.Kind == storage.KindEnroll || !credentialPermits(rec.Kind, rec.Scope, storage.ScopeRead) {
			writeJSONError(w, http.StatusForbidden, "token lacks read scope")
			return
		}

		id, err := newSessionID()
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "failed to create session")
			return
		}
		s.sessions.put(id, sessionEntry{
			credHash:   hash,
			principal:  principalFromRecord(rec),
			clientName: rec.ClientName,
			expires:    time.Now().Add(sessionTTL),
		})
		s.setSessionCookie(w, r, id, int(sessionTTL.Seconds()))

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(sessionResponse{
			ClientName: rec.ClientName,
			TenantID:   rec.TenantID,
		})

	case http.MethodDelete:
		if !s.requireSameOrigin(w, r) {
			return
		}
		if c, err := r.Cookie(sessionCookieName); err == nil {
			s.sessions.delete(c.Value)
		}
		s.setSessionCookie(w, r, "", -1)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "logged_out"})

	default:
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *Server) lookupSession(r *http.Request) (sessionEntry, bool) {
	c, err := r.Cookie(sessionCookieName)
	if err != nil || c.Value == "" || s.sessions == nil {
		return sessionEntry{}, false
	}
	ent, ok := s.sessions.get(c.Value)
	if !ok {
		return sessionEntry{}, false
	}
	if s.keys == nil {
		return sessionEntry{}, false
	}
	epoch := s.currentAuthEpoch()
	if cached, hit := s.authCache.get(ent.credHash, epoch); hit {
		if !cached.valid {
			s.sessions.delete(c.Value)
			return sessionEntry{}, false
		}
		ent.principal = cached.principal
		return ent, true
	}
	rec, err := s.keys.ResolveAPIKey(ent.credHash)
	if err != nil {
		s.sessions.delete(c.Value)
		return sessionEntry{}, false
	}
	ent.principal = principalFromRecord(rec)
	return ent, true
}
