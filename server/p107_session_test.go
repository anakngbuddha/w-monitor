package server_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"Zeus/storage"
)

func TestSessionCrossSiteLoginRejected(t *testing.T) {
	srv, db, _ := hubEnrollFixture(t)
	readToken, _ := storage.GenerateToken(storage.KindRead)
	db.UpsertAPIKey(storage.APIKeyRecord{
		TenantID:   "t_csrf",
		ClientName: "CSRFClient",
		KeyHash:    storage.HashAPIKey(readToken),
		KeyPrefix:  "wmr_",
		Kind:       storage.KindRead,
		Scope:      storage.ScopeRead,
	})
	body, _ := json.Marshal(map[string]string{"read_token": readToken})
	req := httptest.NewRequest("POST", "/api/session", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://evil.example")
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("cross-site login: got %d %s", w.Code, w.Body.String())
	}
}

func TestSessionSecurityHeaders(t *testing.T) {
	srv, _, _ := hubEnrollFixture(t)
	req := httptest.NewRequest("GET", "/api/health", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Header().Get("X-Frame-Options") != "DENY" {
		t.Fatalf("X-Frame-Options = %q", w.Header().Get("X-Frame-Options"))
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("Cache-Control = %q", w.Header().Get("Cache-Control"))
	}
	csp := w.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "frame-ancestors 'none'") {
		t.Fatalf("CSP missing frame-ancestors: %s", csp)
	}
}

func TestSessionLoginThrottleBeforeDB(t *testing.T) {
	srv, db, _ := hubEnrollFixture(t)
	readToken, _ := storage.GenerateToken(storage.KindRead)
	db.UpsertAPIKey(storage.APIKeyRecord{
		TenantID:   "t_thr",
		ClientName: "Thr",
		KeyHash:    storage.HashAPIKey(readToken),
		KeyPrefix:  "wmr_",
		Kind:       storage.KindRead,
		Scope:      storage.ScopeRead,
	})
	body, _ := json.Marshal(map[string]string{"read_token": "wrong"})
	saw429 := false
	for i := 0; i < 12; i++ {
		req := httptest.NewRequest("POST", "/api/session", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", "http://example.com")
		req.RemoteAddr = "203.0.113.50:9"
		w := httptest.NewRecorder()
		srv.Handler().ServeHTTP(w, req)
		if w.Code == http.StatusTooManyRequests {
			saw429 = true
			break
		}
	}
	if !saw429 {
		t.Fatal("expected login throttle (429) before unlimited DB auth attempts")
	}
}
