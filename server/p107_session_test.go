package server_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
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

func TestSessionCookieJarOverLoopbackTCP(t *testing.T) {
	srv, db, _ := hubEnrollFixture(t)
	readToken, _ := storage.GenerateToken(storage.KindRead)
	if err := db.UpsertAPIKey(storage.APIKeyRecord{
		TenantID: "t_jar", ClientName: "JarClient", KeyHash: storage.HashAPIKey(readToken),
		KeyPrefix: "wmr_", Kind: storage.KindRead, Scope: storage.ScopeRead,
	}); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar}
	hub, err := url.Parse(ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	origin := hub.Scheme + "://" + hub.Host
	body, _ := json.Marshal(map[string]string{"read_token": readToken})
	req, err := http.NewRequest("POST", ts.URL+"/api/session", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", origin)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login: %d", resp.StatusCode)
	}
	cookies := jar.Cookies(hub)
	if len(cookies) != 1 || cookies[0].Name != "wmonitor_session" || cookies[0].Value == readToken {
		t.Fatalf("jar cookies: %+v", cookies)
	}
	metrics, err := client.Get(ts.URL + "/api/metrics")
	if err != nil {
		t.Fatal(err)
	}
	metrics.Body.Close()
	if metrics.StatusCode != http.StatusOK {
		t.Fatalf("cookie metrics: %d", metrics.StatusCode)
	}
	evil, err := http.NewRequest("DELETE", ts.URL+"/api/session", nil)
	if err != nil {
		t.Fatal(err)
	}
	evil.Header.Set("Origin", "https://evil.example")
	csrf, err := client.Do(evil)
	if err != nil {
		t.Fatal(err)
	}
	csrf.Body.Close()
	if csrf.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-site logout: %d", csrf.StatusCode)
	}
	still, err := client.Get(ts.URL + "/api/metrics")
	if err != nil {
		t.Fatal(err)
	}
	still.Body.Close()
	if still.StatusCode != http.StatusOK {
		t.Fatal("CSRF logout revoked the session")
	}
	logout, err := http.NewRequest("DELETE", ts.URL+"/api/session", nil)
	if err != nil {
		t.Fatal(err)
	}
	logout.Header.Set("Origin", origin)
	done, err := client.Do(logout)
	if err != nil {
		t.Fatal(err)
	}
	done.Body.Close()
	if done.StatusCode != http.StatusOK {
		t.Fatalf("logout: %d", done.StatusCode)
	}
	after, err := client.Get(ts.URL + "/api/metrics")
	if err != nil {
		t.Fatal(err)
	}
	after.Body.Close()
	if after.StatusCode != http.StatusUnauthorized {
		t.Fatalf("revoked session: %d", after.StatusCode)
	}
}
