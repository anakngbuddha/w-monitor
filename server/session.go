package server

import (
	"encoding/json"
	"net/http"
	"strings"

	"Zeus/storage"
)

type sessionRequest struct {
	ReadToken string `json:"read_token"`
}

type sessionResponse struct {
	ClientName string `json:"client_name"`
	TenantID   string `json:"tenant_id"`
}

// handleSession manages dashboard authentication sessions using HttpOnly cookies.
func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	s.writeCORS(w, r)
	switch r.Method {
	case http.MethodPost:
		var req sessionRequest
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
			// Fallback: accept token from X-API-Key header if body is empty
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

		// Must have read, admin, or all scope
		if !checkScope(rec.Scope, storage.ScopeRead) {
			writeJSONError(w, http.StatusForbidden, "token lacks read scope")
			return
		}

		isSecure := r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
		http.SetCookie(w, &http.Cookie{
			Name:     "wmonitor_session",
			Value:    rawToken,
			Path:     "/",
			HttpOnly: true,
			Secure:   isSecure,
			SameSite: http.SameSiteStrictMode,
			MaxAge:   86400 * 7, // 7 days
		})

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(sessionResponse{
			ClientName: rec.ClientName,
			TenantID:   rec.TenantID,
		})

	case http.MethodDelete:
		http.SetCookie(w, &http.Cookie{
			Name:     "wmonitor_session",
			Value:    "",
			Path:     "/",
			HttpOnly: true,
			MaxAge:   -1,
		})
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "logged_out"})

	default:
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}
