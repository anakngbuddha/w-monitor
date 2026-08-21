package server_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"Zeus/server"
	"Zeus/storage"
)

// hubFixture builds a hub-mode server with one registered client.
func hubFixture(t *testing.T) (*server.Server, *storage.DB, string) {
	t.Helper()

	db, err := storage.Open(filepath.Join(t.TempDir(), "auth_test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	const plaintext = "registered-client-key"
	if err := db.UpsertAPIKey(storage.APIKeyRecord{
		KeyHash:    storage.HashAPIKey(plaintext),
		TenantID:   "t_registered",
		ClientName: "RegisteredClient",
	}); err != nil {
		t.Fatalf("UpsertAPIKey: %v", err)
	}

	srv := server.New(db, "0")
	srv.EnableHubMode(db)
	return srv, db, plaintext
}

func do(srv *server.Server, method, target, key string, body []byte) *httptest.ResponseRecorder {
	var req *http.Request
	if body != nil {
		req = httptest.NewRequest(method, target, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	} else {
		req = httptest.NewRequest(method, target, nil)
	}
	if key != "" {
		req.Header.Set("X-API-Key", key)
	}
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	return w
}

// The headline fix: a made-up key must not work anywhere.
func TestUnknownKeyRejectedOnEveryEndpoint(t *testing.T) {
	srv, _, _ := hubFixture(t)

	endpoints := []struct {
		method, target string
		body           []byte
	}{
		{"GET", "/api/metrics?range=24h", nil},
		{"GET", "/api/processes?range=24h", nil},
		{"GET", "/api/servers", nil},
		{"GET", "/api/export/csv?range=24h", nil},
		{"POST", "/api/ingest?type=metric", []byte(`{"CPUPct":50}`)},
	}

	for _, e := range endpoints {
		w := do(srv, e.method, e.target, "a-key-i-invented", e.body)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("%s %s with unknown key: got %d, want 401", e.method, e.target, w.Code)
		}
	}
}

func TestRegisteredKeyIsAccepted(t *testing.T) {
	srv, _, key := hubFixture(t)

	if w := do(srv, "GET", "/api/metrics?range=24h", key, nil); w.Code != http.StatusOK {
		t.Errorf("registered key rejected: %d %s", w.Code, w.Body.String())
	}
}

func TestRevokedKeyIsRejected(t *testing.T) {
	srv, db, key := hubFixture(t)

	if w := do(srv, "GET", "/api/servers", key, nil); w.Code != http.StatusOK {
		t.Fatalf("key should work before revocation: %d", w.Code)
	}

	if _, err := db.RevokeAPIKey("RegisteredClient"); err != nil {
		t.Fatalf("RevokeAPIKey: %v", err)
	}

	// A fresh server avoids the positive auth cache, which is the documented
	// 60s window for revocation to propagate.
	srv2 := server.New(db, "0")
	srv2.EnableHubMode(db)
	if w := do(srv2, "GET", "/api/servers", key, nil); w.Code != http.StatusUnauthorized {
		t.Errorf("revoked key still works: got %d, want 401", w.Code)
	}
}

func TestMissingKeyRejected(t *testing.T) {
	srv, _, _ := hubFixture(t)
	if w := do(srv, "GET", "/api/metrics?range=24h", "", nil); w.Code != http.StatusUnauthorized {
		t.Errorf("got %d, want 401", w.Code)
	}
}

// Keys in URLs leak into access logs, proxy logs, and browser history.
func TestKeyInQueryParamRejected(t *testing.T) {
	srv, _, key := hubFixture(t)

	w := do(srv, "GET", "/api/metrics?range=24h&api_key="+key, "", nil)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("api_key query param was accepted: got %d, want 401", w.Code)
	}
}

// Ingest must tag rows with the tenant the key maps to, not with anything the
// client sends in the payload.
func TestIngestIgnoresClientSuppliedTenant(t *testing.T) {
	srv, db, key := hubFixture(t)

	payload, _ := json.Marshal(map[string]interface{}{
		"Timestamp": time.Now(),
		"TenantID":  "t_someone_elses_tenant",
		"ServerID":  "agent-1",
		"CPUPct":    42.0,
		"MemPct":    10.0,
	})

	if w := do(srv, "POST", "/api/ingest?type=metric", key, payload); w.Code != http.StatusAccepted {
		t.Fatalf("ingest failed: %d %s", w.Code, w.Body.String())
	}

	hijacked, err := db.QueryMetrics(time.Now().Add(-time.Hour), "t_someone_elses_tenant")
	if err != nil {
		t.Fatalf("QueryMetrics: %v", err)
	}
	if len(hijacked) != 0 {
		t.Errorf("client-supplied tenant was honoured: %d rows landed in the wrong tenant", len(hijacked))
	}

	mine, err := db.QueryMetrics(time.Now().Add(-time.Hour), "t_registered")
	if err != nil {
		t.Fatalf("QueryMetrics: %v", err)
	}
	if len(mine) != 1 {
		t.Errorf("got %d rows for the authenticated tenant, want 1", len(mine))
	}
}

func TestTenantIsolation(t *testing.T) {
	db, err := storage.Open(filepath.Join(t.TempDir(), "isolation_test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	const keyA = "key-alpha"
	const keyB = "key-beta"
	db.UpsertAPIKey(storage.APIKeyRecord{KeyHash: storage.HashAPIKey(keyA), TenantID: "t_alpha", ClientName: "Alpha"})
	db.UpsertAPIKey(storage.APIKeyRecord{KeyHash: storage.HashAPIKey(keyB), TenantID: "t_beta", ClientName: "Beta"})

	srv := server.New(db, "0")
	srv.EnableHubMode(db)

	mkBody := func(serverID string, cpu float64) []byte {
		b, _ := json.Marshal(map[string]interface{}{
			"Timestamp": time.Now(),
			"ServerID":  serverID,
			"CPUPct":    cpu,
			"MemPct":    5.0,
		})
		return b
	}

	if w := do(srv, "POST", "/api/ingest?type=metric", keyA, mkBody("agent-alpha", 77.5)); w.Code != http.StatusAccepted {
		t.Fatalf("alpha ingest: %d %s", w.Code, w.Body.String())
	}
	if w := do(srv, "POST", "/api/ingest?type=metric", keyB, mkBody("agent-beta", 33.2)); w.Code != http.StatusAccepted {
		t.Fatalf("beta ingest: %d %s", w.Code, w.Body.String())
	}

	for _, tc := range []struct{ key, wantServer string }{
		{keyA, "agent-alpha"},
		{keyB, "agent-beta"},
	} {
		w := do(srv, "GET", "/api/metrics?range=24h", tc.key, nil)
		if w.Code != http.StatusOK {
			t.Fatalf("query failed: %d %s", w.Code, w.Body.String())
		}
		var resp struct {
			Count int `json:"count"`
			Data  []struct {
				ServerID string `json:"server_id"`
			} `json:"data"`
		}
		json.NewDecoder(w.Body).Decode(&resp)
		if resp.Count != 1 {
			t.Errorf("tenant saw %d rows, want 1 (leakage across tenants)", resp.Count)
			continue
		}
		if resp.Data[0].ServerID != tc.wantServer {
			t.Errorf("tenant saw %q, want %q", resp.Data[0].ServerID, tc.wantServer)
		}
	}
}

// Hub mode with no key store must fail closed, not open.
func TestHubModeWithoutKeyStoreRejectsEverything(t *testing.T) {
	db, err := storage.Open(filepath.Join(t.TempDir(), "nokeystore_test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	srv := server.New(db, "0")
	srv.EnableHubMode(nil)

	if w := do(srv, "GET", "/api/metrics?range=24h", "anything", nil); w.Code == http.StatusOK {
		t.Error("hub mode with no key store served data; must fail closed")
	}
}

// Local (non-hub) mode has a single dataset and must not demand a key.
func TestLocalModeNeedsNoKey(t *testing.T) {
	db, err := storage.Open(filepath.Join(t.TempDir(), "local_test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	srv := server.New(db, "0")
	if w := do(srv, "GET", "/api/metrics?range=24h", "", nil); w.Code != http.StatusOK {
		t.Errorf("local mode required a key: %d", w.Code)
	}
}

func TestIngestRejectsOversizedBody(t *testing.T) {
	srv, _, key := hubFixture(t)

	huge := make([]byte, 1<<20) // 1 MB, over the 256 KB cap
	for i := range huge {
		huge[i] = 'a'
	}
	body := append([]byte(`{"ServerID":"`), huge...)
	body = append(body, []byte(`"}`)...)

	w := do(srv, "POST", "/api/ingest?type=metric", key, body)
	if w.Code == http.StatusAccepted {
		t.Error("oversized body was accepted")
	}
}

func TestNoWildcardCORSByDefault(t *testing.T) {
	db, err := storage.Open(filepath.Join(t.TempDir(), "cors_test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	srv := server.New(db, "0")
	req := httptest.NewRequest("GET", "/api/metrics?range=24h", nil)
	req.Header.Set("Origin", "https://evil.example.com")
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)

	if got := w.Header().Get("Access-Control-Allow-Origin"); got == "*" {
		t.Error("wildcard CORS is still being sent on an authenticated API")
	}
	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("unexpected CORS origin %q with no allowlist configured", got)
	}
}

func TestCORSAllowlistEchoesPermittedOrigin(t *testing.T) {
	db, err := storage.Open(filepath.Join(t.TempDir(), "cors2_test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	srv := server.New(db, "0")
	srv.SetAllowedOrigins([]string{"https://dash.example.com"})

	req := httptest.NewRequest("GET", "/api/metrics?range=24h", nil)
	req.Header.Set("Origin", "https://dash.example.com")
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)

	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "https://dash.example.com" {
		t.Errorf("Access-Control-Allow-Origin = %q, want the allowed origin", got)
	}

	req2 := httptest.NewRequest("GET", "/api/metrics?range=24h", nil)
	req2.Header.Set("Origin", "https://evil.example.com")
	w2 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w2, req2)

	if got := w2.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("disallowed origin was echoed: %q", got)
	}
}
