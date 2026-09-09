package server

import (
	"encoding/json"
	"errors"
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
	EnrollCode   string `json:"enroll_code"`
	ServerID     string `json:"server_id"`
	Hostname     string `json:"hostname"`
	OS           string `json:"os"`
	Version      string `json:"version"`
	CurrentToken string `json:"current_token"`
}

type enrollResponse struct {
	Token    string `json:"token"`
	TenantID string `json:"tenant_id"`
	ServerID string `json:"server_id"`
}

type adminEnrollCodeRequest struct {
	ClientName string `json:"client_name"`
	TenantID   string `json:"tenant_id"`
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
		isSecure := requestIsHTTPS(r)
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
	req.CurrentToken = strings.TrimSpace(req.CurrentToken)
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

	agentToken, err := storage.GenerateToken(storage.KindAgent)
	if err != nil {
		log.Printf("[server] generate agent token error: %v", err)
		writeJSONError(w, http.StatusInternalServerError, "internal server error")
		return
	}

	currentHash := ""
	if req.CurrentToken != "" {
		currentHash = storage.HashAPIKey(req.CurrentToken)
	}

	result, err := adminStore.CompleteEnrollment(storage.EnrollmentRequest{
		CodeHash:         storage.HashEnrollCode(req.EnrollCode),
		ServerID:         req.ServerID,
		CurrentTokenHash: currentHash,
		Token:            agentToken,
		TokenHash:        storage.HashAPIKey(agentToken),
		TokenPrefix:      storage.ExtractKeyPrefix(agentToken),
	})
	if err != nil {
		switch {
		case errors.Is(err, storage.ErrRotationRequiresProof):
			writeJSONError(w, http.StatusConflict, "replacement requires current_token or operator approval")
		case errors.Is(err, storage.ErrEnrollmentConflict):
			writeJSONError(w, http.StatusConflict, "enrollment conflict; retry")
		case errors.Is(err, storage.ErrAPIKeyNotFound):
			log.Printf("[server] failed enrollment attempt for server %q from %s", req.ServerID, ip)
			writeJSONError(w, http.StatusUnauthorized, "enrollment failed")
		default:
			log.Printf("[server] store agent token error: %v", err)
			writeJSONError(w, http.StatusInternalServerError, "internal server error")
		}
		return
	}

	s.invalidateHashes(result.RevokedHashes)
	prefix := storage.ExtractKeyPrefix(result.Token)
	if err := s.recordAudit(storage.AuditEvent{TenantID: result.TenantID, ActorKind: storage.KindEnroll, ActorPrefix: "wme_", Action: "enroll.completed", TargetType: "agent", TargetID: req.ServerID}); err != nil {
		if !result.Recovered {
			if adminStore, ok := s.keys.(AdminStore); ok {
				if _, revErr := adminStore.RevokeAgent(result.TenantID, req.ServerID); revErr == nil {
					s.invalidateHashes([]string{storage.HashAPIKey(result.Token)})
				}
			}
		}
		writeJSONError(w, http.StatusServiceUnavailable, "audit unavailable")
		return
	}
	if result.Recovered {
		log.Printf("[server] recovered enrollment handshake for server %q [prefix: %s]", req.ServerID, prefix)
	} else {
		log.Printf("[server] successfully enrolled server %q (%s, %s, ver=%s) for client %q [prefix: %s]",
			req.ServerID, req.Hostname, req.OS, req.Version, result.ClientName, prefix)
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(enrollResponse{
		Token:    result.Token,
		TenantID: result.TenantID,
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

	principal, ok := s.authPrincipal(w, r, storage.ScopeAdmin)
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

	// Find existing tenant ID only when the display name is unique. Duplicates
	// never resolve identity. An explicit tenant_id always wins.
	var tenantID string
	if req.TenantID != "" {
		tenantID = strings.TrimSpace(req.TenantID)
	} else {
		allKeys, err := adminStore.ListAPIKeys()
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "failed to list credentials")
			return
		}
		id, err := storage.UniqueTenantForClientName(allKeys, req.ClientName)
		if err != nil {
			writeJSONError(w, http.StatusConflict, "client_name is ambiguous; pass tenant_id")
			return
		}
		tenantID = id
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

	codeHash := storage.HashEnrollCode(code)
	expiresAt := time.Now().Add(time.Duration(ttlHours) * time.Hour)

	if err := adminStore.UpsertAPIKey(storage.APIKeyRecord{
		TenantID:   tenantID,
		ClientName: req.ClientName,
		KeyHash:    codeHash,
		KeyPrefix:  "wme_",
		Kind:       storage.KindEnroll,
		Scope:      storage.ScopeEnroll,
		ExpiresAt:  expiresAt,
		MaxUses:    maxUses,
		IssuedBy:   "admin",
		CreatedAt:  time.Now(),
	}); err != nil {
		log.Printf("[server] failed to store enroll code: %v", err)
		writeJSONError(w, http.StatusInternalServerError, "failed to save enroll code")
		return
	}

	log.Printf("[server] created enroll code for client %q (max_uses=%d, ttl=%dh)",
		req.ClientName, maxUses, ttlHours)
	if err := s.recordAudit(storage.AuditEvent{TenantID: tenantID, ActorKind: principal.Kind, ActorPrefix: s.actorPrefix(principal.Kind, principal.CredentialID), Action: "enroll.code_issued", TargetType: "client", TargetID: req.ClientName}); err != nil {
		s.revokeUnauditedHash(codeHash)
		writeJSONError(w, http.StatusServiceUnavailable, "audit unavailable")
		return
	}

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
	principal, ok := s.authPrincipal(w, r, storage.ScopeRead)
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
		listTenant := principal.TenantID
		if principal.Kind == storage.KindAdmin && principal.Permissions == storage.ScopeAdmin {
			if q := strings.TrimSpace(r.URL.Query().Get("tenant_id")); q != "" {
				listTenant = q
			} else {
				listTenant = ""
			}
		}
		agents, err := adminStore.ListAgents(listTenant)
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
		if !principal.permits(storage.ScopeAdmin) {
			writeJSONError(w, http.StatusForbidden, "insufficient credential scope")
			return
		}
		targetServerID := strings.TrimSpace(r.URL.Query().Get("server_id"))
		if targetServerID == "" {
			writeJSONError(w, http.StatusBadRequest, "server_id is required")
			return
		}
		revokeTenant := principal.TenantID
		if principal.Kind == storage.KindAdmin {
			if q := strings.TrimSpace(r.URL.Query().Get("tenant_id")); q != "" {
				revokeTenant = q
			}
		}
		hashes, _ := adminStore.ActiveAgentHashes(revokeTenant, targetServerID)
		revoked, err := adminStore.RevokeAgent(revokeTenant, targetServerID)
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "failed to revoke agent")
			return
		}
		s.invalidateHashes(hashes)
		if err := s.recordAudit(storage.AuditEvent{TenantID: revokeTenant, ActorKind: principal.Kind, ActorPrefix: s.actorPrefix(principal.Kind, principal.CredentialID), Action: "agent.revoked", TargetType: "agent", TargetID: targetServerID}); err != nil {
			writeJSONError(w, http.StatusServiceUnavailable, "audit unavailable")
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
	principal, ok := s.authPrincipal(w, r, storage.ScopeAdmin)
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

		existing, err := adminStore.ListAPIKeys()
		if err != nil {
			writeJSONError(w, http.StatusInternalServerError, "failed to list clients")
			return
		}
		if _, err := storage.UniqueTenantForClientName(existing, req.ClientName); err != nil {
			writeJSONError(w, http.StatusConflict, "client_name is ambiguous")
			return
		}
		for _, k := range existing {
			if strings.EqualFold(k.ClientName, req.ClientName) {
				writeJSONError(w, http.StatusConflict, "client_name already exists")
				return
			}
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

		if err := s.recordAudit(storage.AuditEvent{TenantID: tenantID, ActorKind: principal.Kind, ActorPrefix: s.actorPrefix(principal.Kind, principal.CredentialID), Action: "client.created", TargetType: "client", TargetID: req.ClientName}); err != nil {
			s.revokeUnauditedHash(hash)
			writeJSONError(w, http.StatusServiceUnavailable, "audit unavailable")
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

// handleAdminAgentRotate issues a replacement agent token with operator approval
// (POST /api/admin/agents/rotate). No enrollment code is consumed.
func (s *Server) handleAdminAgentRotate(w http.ResponseWriter, r *http.Request) {
	s.writeCORS(w, r)
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	principal, ok := s.authPrincipal(w, r, storage.ScopeAdmin)
	if !ok {
		return
	}
	adminStore, ok := s.keys.(AdminStore)
	if !ok {
		writeJSONError(w, http.StatusServiceUnavailable, "admin store unavailable")
		return
	}

	var req struct {
		ServerID string `json:"server_id"`
		TenantID string `json:"tenant_id"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	req.ServerID = strings.TrimSpace(req.ServerID)
	if req.ServerID == "" {
		writeJSONError(w, http.StatusBadRequest, "server_id is required")
		return
	}
	tenantID := principal.TenantID
	if principal.Kind == storage.KindAdmin {
		if q := strings.TrimSpace(req.TenantID); q != "" {
			tenantID = q
		}
	}
	if tenantID == "" {
		writeJSONError(w, http.StatusBadRequest, "tenant_id is required")
		return
	}

	token, err := storage.GenerateToken(storage.KindAgent)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "failed to generate token")
		return
	}
	result, err := adminStore.ReplaceAgent(tenantID, req.ServerID, token, storage.HashAPIKey(token), storage.ExtractKeyPrefix(token))
	if err != nil {
		if errors.Is(err, storage.ErrAPIKeyNotFound) {
			writeJSONError(w, http.StatusNotFound, "no active agent for that identity")
			return
		}
		if errors.Is(err, storage.ErrEnrollmentConflict) {
			writeJSONError(w, http.StatusConflict, "enrollment conflict; retry")
			return
		}
		writeJSONError(w, http.StatusInternalServerError, "failed to rotate agent")
		return
	}
	s.invalidateHashes(result.RevokedHashes)
	if err := s.recordAudit(storage.AuditEvent{TenantID: result.TenantID, ActorKind: principal.Kind, ActorPrefix: s.actorPrefix(principal.Kind, principal.CredentialID), Action: "agent.rotated", TargetType: "agent", TargetID: result.ServerID}); err != nil {
		s.revokeUnauditedHash(storage.HashAPIKey(token))
		writeJSONError(w, http.StatusServiceUnavailable, "audit unavailable")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(enrollResponse{
		Token:    result.Token,
		TenantID: result.TenantID,
		ServerID: result.ServerID,
	})
}
