package server

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"Zeus/storage"
)

var serverIDRegex = regexp.MustCompile(`^[A-Za-z0-9._-]{3,128}$`)

type enrollRequest struct {
	EnrollCode string `json:"enroll_code"`
	ServerID   string `json:"server_id"`
	Hostname   string `json:"hostname"`
	OS         string `json:"os"`
	Version    string `json:"version"`
}

type enrollResponse struct {
	Token    string `json:"token"`
	TenantID string `json:"tenant_id"`
	ServerID string `json:"server_id"`
}

type adminEnrollCodeRequest struct {
	ClientName string `json:"client_name"`
	MaxUses    int    `json:"max_uses"`
	TTLHours   int    `json:"ttl_hours"`
}

type adminEnrollCodeResponse struct {
	Code       string `json:"code"`
	ClientName string `json:"client_name"`
	TenantID   string `json:"tenant_id"`
	ExpiresAt  int64  `json:"expires_at"`
	MaxUses    int    `json:"max_uses"`
}

type agentListItem struct {
	ServerID   string `json:"server_id"`
	ClientName string `json:"client_name"`
	KeyPrefix  string `json:"key_prefix"`
	CreatedAt  int64  `json:"created_at"`
	LastSeenAt int64  `json:"last_seen_at"`
	Revoked    bool   `json:"revoked"`
}

// handleEnroll implements the first-run machine enrollment handshake (POST /api/enroll).
func (s *Server) handleEnroll(w http.ResponseWriter, r *http.Request) {
	s.writeCORS(w, r)
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	// Security: Require HTTPS unless explicit test override is active
	if os.Getenv("WMONITOR_ALLOW_INSECURE_ENROLL") != "1" {
		isSecure := r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
		if !isSecure {
			writeJSONError(w, http.StatusUpgradeRequired, "enrollment requires HTTPS")
			return
		}
	}

	// Rate limiting per IP
	ip := clientIP(r)
	if !s.limiter.allow("enroll:" + ip) {
		w.Header().Set("Retry-After", "10")
		writeJSONError(w, http.StatusTooManyRequests, "rate limit exceeded")
		return
	}

	// Hostile endpoint controls: 4 KB max body, strict schema
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	var req enrollRequest
	if err := dec.Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid enrollment payload")
		return
	}

	req.EnrollCode = strings.TrimSpace(req.EnrollCode)
	req.ServerID = strings.TrimSpace(req.ServerID)
	if req.EnrollCode == "" || req.ServerID == "" {
		writeJSONError(w, http.StatusBadRequest, "enroll_code and server_id are required")
		return
	}

	if !serverIDRegex.MatchString(req.ServerID) {
		writeJSONError(w, http.StatusBadRequest, "invalid server_id format")
		return
	}

	adminStore, ok := s.keys.(AdminStore)
	if !ok {
		log.Printf("[server] enrollment failed: key store does not satisfy AdminStore (%T)", s.keys)
		writeJSONError(w, http.StatusServiceUnavailable, "enrollment service unavailable")
		return
	}

	// Atomic consume: validates code, checks expiry & max_uses, increments uses
	codeHash := storage.HashAPIKey(req.EnrollCode)
	codeRec, err := adminStore.ConsumeEnrollCode(codeHash)
	if err != nil {
		log.Printf("[server] failed enrollment attempt for server %q from %s", req.ServerID, ip)
		// Byte-identical 401 response for all invalid/exhausted/expired codes
		writeJSONError(w, http.StatusUnauthorized, "enrollment failed")
		return
	}

	// Idempotent rotation: if an existing token was active for this (tenant, server_id), revoke it
	if n, _ := adminStore.RevokeAgent(codeRec.TenantID, req.ServerID); n > 0 {
		log.Printf("[server] rotated %d existing agent token(s) for server %q", n, req.ServerID)
	}

	// Generate new machine-bound agent token (scope: ingest)
	agentToken, err := storage.GenerateToken(storage.KindAgent)
	if err != nil {
		log.Printf("[server] generate agent token error: %v", err)
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	agentHash := storage.HashAPIKey(agentToken)
	prefix := storage.ExtractKeyPrefix(agentToken)

	err = adminStore.UpsertAPIKey(storage.APIKeyRecord{
		TenantID:   codeRec.TenantID,
		ClientName: codeRec.ClientName,
		KeyHash:    agentHash,
		KeyPrefix:  prefix,
		Kind:       storage.KindAgent,
		Scope:      storage.ScopeIngest,
		ServerID:   req.ServerID,
		IssuedBy:   "enroll:" + codeRec.ClientName,
		CreatedAt:  time.Now(),
	})
	if err != nil {
		log.Printf("[server] store agent token error: %v", err)
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	log.Printf("[server] successfully enrolled server %q (%s, %s, ver=%s) for client %q [prefix: %s]",
		req.ServerID, req.Hostname, req.OS, req.Version, codeRec.ClientName, prefix)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(enrollResponse{
		Token:    agentToken,
		TenantID: codeRec.TenantID,
		ServerID: req.ServerID,
	})
}

// handleAdminEnrollCodes creates short-lived, use-limited enrollment codes (POST /api/admin/enroll-codes).
func (s *Server) handleAdminEnrollCodes(w http.ResponseWriter, r *http.Request) {
	s.writeCORS(w, r)
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	// Requires admin scope
	_, ok := s.authTenantScope(w, r, storage.ScopeAdmin)
	if !ok {
		return
	}

	adminStore, ok := s.keys.(AdminStore)
	if !ok {
		writeJSONError(w, http.StatusServiceUnavailable, "admin store unavailable")
		return
	}

	var req adminEnrollCodeRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	req.ClientName = strings.TrimSpace(req.ClientName)
	if req.ClientName == "" {
		writeJSONError(w, http.StatusBadRequest, "client_name is required")
		return
	}

	maxUses := req.MaxUses
	if maxUses <= 0 {
		maxUses = 25 // default 25 uses
	}

	ttlHours := req.TTLHours
	if ttlHours <= 0 {
		ttlHours = 336 // default 14 days (336 hours)
	}

	// Find existing tenant ID for client_name or mint a fresh one
	var tenantID string
	allKeys, _ := adminStore.ListAPIKeys()
	for _, k := range allKeys {
		if strings.EqualFold(k.ClientName, req.ClientName) && k.TenantID != "" {
			tenantID = k.TenantID
			break
		}
	}
	if tenantID == "" {
		var err error
		tenantID, err = storage.NewTenantID()
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "failed to generate tenant ID")
			return
		}
	}

	code, err := storage.GenerateEnrollCode()
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to generate enroll code")
		return
	}

	codeHash := storage.HashAPIKey(code)
	expiresAt := time.Now().Add(time.Duration(ttlHours) * time.Hour)

	if err := adminStore.UpsertAPIKey(storage.APIKeyRecord{
		TenantID:   tenantID,
		ClientName: req.ClientName,
		KeyHash:    codeHash,
		KeyPrefix:  "wme_",
		Kind:       storage.KindEnroll,
		Scope:      storage.ScopeIngest,
		ExpiresAt:  expiresAt,
		MaxUses:    maxUses,
		IssuedBy:   "admin",
		CreatedAt:  time.Now(),
	}); err != nil {
		log.Printf("[server] failed to store enroll code: %v", err)
		writeJSONError(w, http.StatusInternalServerError, "failed to save enroll code")
		return
	}

	log.Printf("[server] created enroll code %s for client %q (max_uses=%d, ttl=%dh)",
		code, req.ClientName, maxUses, ttlHours)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(adminEnrollCodeResponse{
		Code:       code,
		ClientName: req.ClientName,
		TenantID:   tenantID,
		ExpiresAt:  expiresAt.Unix(),
		MaxUses:    maxUses,
	})
}

// handleAdminAgents lists or revokes agents (GET/DELETE /api/admin/agents).
func (s *Server) handleAdminAgents(w http.ResponseWriter, r *http.Request) {
	s.writeCORS(w, r)
	// Requires read or admin scope
	tenantID, ok := s.authTenantScope(w, r, storage.ScopeRead)
	if !ok {
		return
	}

	adminStore, ok := s.keys.(AdminStore)
	if !ok {
		writeJSONError(w, http.StatusServiceUnavailable, "admin store unavailable")
		return
	}

	switch r.Method {
	case http.MethodGet:
		// Optional filter for tenant if caller is tenant-scoped
		agents, err := adminStore.ListAgents(tenantID)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "failed to list agents")
			return
		}

		out := make([]agentListItem, 0, len(agents))
		for _, a := range agents {
			out = append(out, agentListItem{
				ServerID:   a.ServerID,
				ClientName: a.ClientName,
				KeyPrefix:  a.KeyPrefix,
				CreatedAt:  a.CreatedAt.Unix(),
				LastSeenAt: a.LastSeenAt.Unix(),
				Revoked:    a.Revoked,
			})
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"agents": out})

	case http.MethodDelete:
		targetServerID := strings.TrimSpace(r.URL.Query().Get("server_id"))
		if targetServerID == "" {
			writeJSONError(w, http.StatusBadRequest, "server_id is required")
			return
		}
		revoked, err := adminStore.RevokeAgent(tenantID, targetServerID)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "failed to revoke agent")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"server_id": targetServerID,
			"revoked":   revoked,
		})

	default:
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// handleAdminClients creates a new client read token or lists clients (POST/GET /api/admin/clients).
func (s *Server) handleAdminClients(w http.ResponseWriter, r *http.Request) {
	s.writeCORS(w, r)
	_, ok := s.authTenantScope(w, r, storage.ScopeAdmin)
	if !ok {
		return
	}

	adminStore, ok := s.keys.(AdminStore)
	if !ok {
		writeJSONError(w, http.StatusServiceUnavailable, "admin store unavailable")
		return
	}

	switch r.Method {
	case http.MethodGet:
		allKeys, err := adminStore.ListAPIKeys()
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "failed to list clients")
			return
		}
		type clientInfo struct {
			ClientName string `json:"client_name"`
			TenantID   string `json:"tenant_id"`
			Kind       string `json:"kind"`
			KeyPrefix  string `json:"key_prefix"`
			Revoked    bool   `json:"revoked"`
		}
		var out []clientInfo
		for _, k := range allKeys {
			if k.Kind == storage.KindRead || k.Kind == storage.KindLegacy {
				out = append(out, clientInfo{
					ClientName: k.ClientName,
					TenantID:   k.TenantID,
					Kind:       k.Kind,
					KeyPrefix:  k.KeyPrefix,
					Revoked:    k.Revoked,
				})
			}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"clients": out})

	case http.MethodPost:
		var req struct {
			ClientName string `json:"client_name"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
			writeJSONError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		req.ClientName = strings.TrimSpace(req.ClientName)
		if req.ClientName == "" {
			writeJSONError(w, http.StatusBadRequest, "client_name is required")
			return
		}

		readToken, err := storage.GenerateToken(storage.KindRead)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "failed to generate read token")
			return
		}

		tenantID, err := storage.NewTenantID()
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "failed to generate tenant ID")
			return
		}

		hash := storage.HashAPIKey(readToken)
		prefix := storage.ExtractKeyPrefix(readToken)

		if err := adminStore.UpsertAPIKey(storage.APIKeyRecord{
			TenantID:   tenantID,
			ClientName: req.ClientName,
			KeyHash:    hash,
			KeyPrefix:  prefix,
			Kind:       storage.KindRead,
			Scope:      storage.ScopeRead,
			IssuedBy:   "admin_api",
			CreatedAt:  time.Now(),
		}); err != nil {
			writeJSONError(w, http.StatusInternalServerError, "failed to save client")
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]string{
			"client_name": req.ClientName,
			"tenant_id":   tenantID,
			"read_token":  readToken,
		})

	default:
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}
