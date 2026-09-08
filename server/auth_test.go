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

func hubFixture(t *testing.T) (*server.Server, *storage.DB, string) {
	t.Helper()
	db, err := storage.Open(filepath.Join(t.TempDir(), "auth.db"))
	if err != nil { t.Fatal(err) }
	t.Cleanup(func() { db.Close() })
	const key = "registered-client-key"
	if err := db.UpsertAPIKey(storage.APIKeyRecord{KeyHash: storage.HashAPIKey(key), TenantID: "t_registered", ClientName: "RegisteredClient", Kind: storage.KindLegacy, Scope: storage.ScopeAll}); err != nil { t.Fatal(err) }
	srv := server.New(db, "0"); srv.EnableHubMode(db)
	return srv, db, key
}

func do(srv *server.Server, method, target, key string, body []byte) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, target, bytes.NewReader(body))
	if body != nil { r.Header.Set("Content-Type", "application/json") }
	if key != "" { r.Header.Set("X-API-Key", key) }
	w := httptest.NewRecorder(); srv.Handler().ServeHTTP(w, r); return w
}

func registerPhase1Agent(t *testing.T, db *storage.DB, tenant, id, token string) {
	t.Helper()
	if err := db.UpsertAPIKey(storage.APIKeyRecord{KeyHash: storage.HashAPIKey(token), TenantID: tenant, ServerID: id, ClientName: "Agent-"+tenant, Kind: storage.KindAgent, Scope: storage.ScopeIngest}); err != nil { t.Fatal(err) }
}

func TestUnknownKeyRejectedOnEveryEndpoint(t *testing.T) {
	srv, _, _ := hubFixture(t)
	for _, route := range []struct{ method, path string }{{"GET", "/api/metrics"}, {"GET", "/api/processes"}, {"GET", "/api/servers"}, {"GET", "/api/export/csv"}, {"GET", "/metrics"}, {"POST", "/api/ingest?type=metric"}, {"POST", "/api/v1/ingest/batches"}} {
		if w := do(srv, route.method, route.path, "invented-key", nil); w.Code != 401 { t.Errorf("%s %s: %d", route.method, route.path, w.Code) }
	}
}
func TestRegisteredKeyIsAccepted(t *testing.T) { srv, _, key := hubFixture(t); if w := do(srv, "GET", "/api/metrics", key, nil); w.Code != 200 { t.Fatalf("read failed: %d", w.Code) } }
func TestRevokedKeyIsRejected(t *testing.T) {
	srv, db, key := hubFixture(t)
	if w := do(srv, "GET", "/api/servers", key, nil); w.Code != 200 { t.Fatal("initial read failed") }
	if _, err := db.RevokeAPIKey("RegisteredClient"); err != nil { t.Fatal(err) }
	restarted := server.New(db, "0"); restarted.EnableHubMode(db)
	if w := do(restarted, "GET", "/api/servers", key, nil); w.Code != 401 { t.Fatal("revoked key accepted after restart") }
}
func TestMissingKeyRejected(t *testing.T) { srv, _, _ := hubFixture(t); if w := do(srv, "GET", "/api/metrics", "", nil); w.Code != 401 { t.Fatal("missing key accepted") } }
func TestKeyInQueryParamRejected(t *testing.T) { srv, _, key := hubFixture(t); if w := do(srv, "GET", "/api/metrics?api_key="+key, "", nil); w.Code != 401 { t.Fatal("query credential accepted") } }

func TestIngestRejectsClientSuppliedForeignTenant(t *testing.T) {
	srv, db, _ := hubFixture(t)
	const token = "fixture-bound-agent"
	registerPhase1Agent(t, db, "t_registered", "agent-1", token)
	payload, _ := json.Marshal(storage.MetricRow{Timestamp: time.Now(), TenantID: "t_someone_else", ServerID: "agent-1", CPUPct: 42, MemPct: 10})
	if w := do(srv, "POST", "/api/ingest?type=metric", token, payload); w.Code != 400 { t.Fatalf("foreign identity not rejected: %d", w.Code) }
	for _, tenant := range []string{"t_registered", "t_someone_else"} { rows, err := db.QueryMetrics(time.Now().Add(-time.Hour), tenant); if err != nil || len(rows) != 0 { t.Fatal("rejected identity caused a write") } }
	payload, _ = json.Marshal(storage.MetricRow{Timestamp: time.Now(), ServerID: "agent-1", CPUPct: 42, MemPct: 10})
	if w := do(srv, "POST", "/api/ingest?type=metric", token, payload); w.Code != 202 { t.Fatalf("bound ingest failed: %d %s", w.Code, w.Body.String()) }
	rows, err := db.QueryMetrics(time.Now().Add(-time.Hour), "t_registered")
	if err != nil || len(rows) != 1 { t.Fatal("authenticated tenant did not own the accepted event") }
}

func TestTenantIsolation(t *testing.T) {
	db, err := storage.Open(filepath.Join(t.TempDir(), "isolation.db"))
	if err != nil { t.Fatal(err) }
	defer db.Close()
	for _, tenant := range []string{"alpha", "beta"} {
		if err := db.UpsertAPIKey(storage.APIKeyRecord{KeyHash: storage.HashAPIKey("read-"+tenant), TenantID: tenant, ClientName: tenant, Kind: storage.KindRead, Scope: storage.ScopeRead}); err != nil { t.Fatal(err) }
		registerPhase1Agent(t, db, tenant, "same-server", "agent-"+tenant)
	}
	srv := server.New(db, "0"); srv.EnableHubMode(db)
	for i, tenant := range []string{"alpha", "beta"} {
		body, _ := json.Marshal(storage.MetricRow{Timestamp: time.Now(), ServerID: "same-server", CPUPct: float64(10+i*70), MemPct: 5})
		if w := do(srv, "POST", "/api/ingest?type=metric", "agent-"+tenant, body); w.Code != 202 { t.Fatalf("ingest %s: %d %s", tenant, w.Code, w.Body.String()) }
	}
	for i, tenant := range []string{"alpha", "beta"} {
		w := do(srv, "GET", "/api/metrics", "read-"+tenant, nil)
		if w.Code != 200 { t.Fatal("scoped read failed") }
		var response struct { Count int `json:"count"`; Data []struct { ServerID string `json:"server_id"`; CPU float64 `json:"cpu_pct"` } `json:"data"` }
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil { t.Fatal(err) }
		if response.Count != 1 || len(response.Data) != 1 || response.Data[0].ServerID != "same-server" || response.Data[0].CPU != float64(10+i*70) { t.Fatal("same-ID cross-tenant leakage") }
	}
}

func TestHubModeWithoutKeyStoreRejectsEverything(t *testing.T) {
	db, err := storage.Open(filepath.Join(t.TempDir(), "no-keys.db")); if err != nil { t.Fatal(err) }; defer db.Close()
	srv := server.New(db, "0"); srv.EnableHubMode(nil)
	if w := do(srv, "GET", "/api/metrics", "anything", nil); w.Code == 200 { t.Fatal("Hub without auth served data") }
}
func TestLocalModeNeedsNoKey(t *testing.T) {
	db, err := storage.Open(filepath.Join(t.TempDir(), "local.db")); if err != nil { t.Fatal(err) }; defer db.Close()
	if w := do(server.New(db, "0"), "GET", "/api/metrics", "", nil); w.Code != 200 { t.Fatal("local read required a key") }
}
func TestIngestRejectsOversizedBody(t *testing.T) {
	srv, db, _ := hubFixture(t)
	registerPhase1Agent(t, db, "t_registered", "agent-1", "size-fixture")
	body := bytes.Repeat([]byte("a"), 1<<20)
	if w := do(srv, "POST", "/api/ingest?type=metric", "size-fixture", body); w.Code != 413 { t.Fatalf("oversized body: %d", w.Code) }
}
func TestNoWildcardCORSByDefault(t *testing.T) {
	srv, _, key := hubFixture(t)
	r := httptest.NewRequest("GET", "/api/metrics", nil); r.Header.Set("X-API-Key", key); r.Header.Set("Origin", "https://evil.example")
	w := httptest.NewRecorder(); srv.Handler().ServeHTTP(w, r)
	if w.Header().Get("Access-Control-Allow-Origin") != "" { t.Fatal("unconfigured CORS origin echoed") }
}
func TestCORSAllowlistEchoesPermittedOrigin(t *testing.T) {
	srv, _, key := hubFixture(t); srv.SetAllowedOrigins([]string{"https://dash.example"})
	for _, origin := range []string{"https://dash.example", "https://evil.example"} {
		r := httptest.NewRequest("GET", "/api/metrics", nil); r.Header.Set("X-API-Key", key); r.Header.Set("Origin", origin)
		w := httptest.NewRecorder(); srv.Handler().ServeHTTP(w, r)
		want := ""; if origin == "https://dash.example" { want = origin }
		if w.Header().Get("Access-Control-Allow-Origin") != want { t.Fatal("CORS boundary changed") }
	}
}
