package server

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"Zeus/storage"
)

var errAuditUnavailable = errors.New("audit unavailable")

type auditStore interface {
	AppendAudit(ctx context.Context, ev storage.AuditEvent) error
	ListAudit(ctx context.Context, tenantID string, limit int) ([]storage.AuditEvent, error)
}

func (s *Server) recordAudit(ev storage.AuditEvent) error {
	store, ok := s.db.(auditStore)
	if !ok {
		return errAuditUnavailable
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return store.AppendAudit(ctx, ev)
}

func (s *Server) revokeUnauditedHash(hash string) {
	admin, ok := s.keys.(AdminStore)
	if !ok || hash == "" {
		return
	}
	if _, err := admin.RevokeKeyHash(hash); err != nil {
		log.Printf("[server] failed to revoke unaudited credential")
	}
	s.invalidateHashes([]string{hash})
}

func (s *Server) actorPrefix(kind, credHash string) string {
	if s.keys == nil || credHash == "" {
		return kind
	}
	rec, err := s.keys.ResolveAPIKey(credHash)
	if err != nil || rec.KeyPrefix == "" {
		return kind
	}
	return rec.KeyPrefix
}

func (s *Server) handleAdminAudit(w http.ResponseWriter, r *http.Request) {
	s.writeCORS(w, r)
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if _, ok := s.authTenantScope(w, r, storage.ScopeAdmin); !ok {
		return
	}
	tenant := strings.TrimSpace(r.URL.Query().Get("tenant_id"))
	if err := storage.RequireTenant(tenant); err != nil {
		writeJSONError(w, http.StatusBadRequest, "tenant_id is required")
		return
	}
	store, ok := s.db.(auditStore)
	if !ok {
		writeJSONError(w, http.StatusServiceUnavailable, "audit unavailable")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	events, err := store.ListAudit(ctx, tenant, 256)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "audit query failed")
		return
	}
	type row struct {
		At          int64  `json:"at"`
		TenantID    string `json:"tenant_id"`
		ActorKind   string `json:"actor_kind"`
		ActorPrefix string `json:"actor_prefix"`
		Action      string `json:"action"`
		TargetType  string `json:"target_type"`
		TargetID    string `json:"target_id"`
	}
	out := make([]row, 0, len(events))
	for _, ev := range events {
		out = append(out, row{At: ev.At.Unix(), TenantID: ev.TenantID, ActorKind: ev.ActorKind, ActorPrefix: ev.ActorPrefix, Action: ev.Action, TargetType: ev.TargetType, TargetID: ev.TargetID})
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(map[string]interface{}{"events": out, "complete": len(out) < 256})
}

// Expected-agent status is separately authorized and is not inferred from
// public /api/health. Completeness is not claimed without a campaign contract.
func (s *Server) handleExpectedAgents(w http.ResponseWriter, r *http.Request) {
	s.writeCORS(w, r)
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if _, ok := s.authTenantScope(w, r, storage.ScopeAdmin); !ok {
		return
	}
	tenant := strings.TrimSpace(r.URL.Query().Get("tenant_id"))
	if err := storage.RequireTenant(tenant); err != nil {
		writeJSONError(w, http.StatusBadRequest, "tenant_id is required")
		return
	}
	adminStore, ok := s.keys.(AdminStore)
	if !ok {
		writeJSONError(w, http.StatusServiceUnavailable, "admin store unavailable")
		return
	}
	agents, err := adminStore.ListAgents(tenant)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "agent inventory unavailable")
		return
	}
	type item struct {
		ServerID     string `json:"server_id"`
		LastSeenAt   int64  `json:"last_seen_at"`
		Revoked      bool   `json:"revoked"`
		Completeness string `json:"completeness"`
	}
	out := make([]item, 0, len(agents))
	for _, a := range agents {
		out = append(out, item{ServerID: a.ServerID, LastSeenAt: a.LastSeenAt.Unix(), Revoked: a.Revoked, Completeness: "not_assessed"})
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(map[string]interface{}{"tenant_id": tenant, "agents": out, "completeness": "not_assessed"})
}
