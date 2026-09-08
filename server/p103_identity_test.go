package server_test

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"Zeus/server"
	"Zeus/storage"
)

func mint(t *testing.T, db *storage.DB, rec storage.APIKeyRecord, plaintext string) string {
	t.Helper()
	if rec.KeyHash == "" {
		rec.KeyHash = storage.HashAPIKey(plaintext)
	}
	if rec.KeyPrefix == "" {
		rec.KeyPrefix = storage.ExtractKeyPrefix(plaintext)
	}
	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = time.Now()
	}
	if err := db.UpsertAPIKey(rec); err != nil {
		t.Fatalf("UpsertAPIKey: %v", err)
	}
	return plaintext
}

func TestP103RouteMethodKindScopeMatrix(t *testing.T) {
	os.Setenv("WMONITOR_ALLOW_INSECURE_ENROLL", "1")
	t.Cleanup(func() { os.Unsetenv("WMONITOR_ALLOW_INSECURE_ENROLL") })

	db, err := storage.Open(filepath.Join(t.TempDir(), "matrix.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	srv := server.New(db, "0")
	srv.EnableHubMode(db)

	adminTok, _ := storage.GenerateToken(storage.KindAdmin)
	mint(t, db, storage.APIKeyRecord{TenantID: "admin", ClientName: "Admin", Kind: storage.KindAdmin, Scope: storage.ScopeAdmin}, adminTok)

	readTok, _ := storage.GenerateToken(storage.KindRead)
	mint(t, db, storage.APIKeyRecord{TenantID: "t_read", ClientName: "Reader", Kind: storage.KindRead, Scope: storage.ScopeRead}, readTok)

	agentTok, _ := storage.GenerateToken(storage.KindAgent)
	mint(t, db, storage.APIKeyRecord{TenantID: "t_agent", ClientName: "AgentCo", Kind: storage.KindAgent, Scope: storage.ScopeIngest, ServerID: "srv-bound"}, agentTok)

	legacyTok := "legacy-all-tenant-key"
	mint(t, db, storage.APIKeyRecord{TenantID: "t_legacy", ClientName: "LegacyCo", Kind: storage.KindLegacy, Scope: storage.ScopeAll}, legacyTok)

	code, _ := storage.GenerateEnrollCode()
	mint(t, db, storage.APIKeyRecord{
		TenantID: "t_enroll", ClientName: "EnrollCo", Kind: storage.KindEnroll, Scope: storage.ScopeEnroll,
		MaxUses: 5, ExpiresAt: time.Now().Add(time.Hour),
		KeyHash: storage.HashEnrollCode(code), KeyPrefix: "wme_",
	}, code)

	metricBody, _ := json.Marshal(map[string]interface{}{
		"Timestamp": time.Now(), "ServerID": "srv-bound", "CPUPct": 1.0, "MemPct": 1.0,
	})
	enrollBody, _ := json.Marshal(map[string]string{"enroll_code": code, "server_id": "srv-new"})
	clientBody, _ := json.Marshal(map[string]string{"client_name": "BrandNewCo"})
	codeBody, _ := json.Marshal(map[string]interface{}{"client_name": "Reader", "tenant_id": "t_read", "max_uses": 1, "ttl_hours": 1})

	type row struct {
		name, method, target, key string
		body                      []byte
		want                      int
	}
	cases := []row{
		{"enroll GET metrics", "GET", "/api/metrics?range=24h", code, nil, http.StatusUnauthorized},
		{"enroll POST ingest", "POST", "/api/ingest?type=metric", code, metricBody, http.StatusUnauthorized},
		{"enroll GET agents", "GET", "/api/admin/agents", code, nil, http.StatusUnauthorized},
		{"enroll DELETE agents", "DELETE", "/api/admin/agents?server_id=srv-bound", code, nil, http.StatusUnauthorized},
		{"enroll POST enroll-codes", "POST", "/api/admin/enroll-codes", code, codeBody, http.StatusUnauthorized},
		{"enroll GET clients", "GET", "/api/admin/clients", code, nil, http.StatusUnauthorized},
		{"enroll POST clients", "POST", "/api/admin/clients", code, clientBody, http.StatusUnauthorized},

		{"agent GET metrics", "GET", "/api/metrics?range=24h", agentTok, nil, http.StatusForbidden},
		{"agent POST ingest", "POST", "/api/ingest?type=metric", agentTok, metricBody, http.StatusAccepted},
		{"agent GET agents", "GET", "/api/admin/agents", agentTok, nil, http.StatusForbidden},
		{"agent DELETE agents", "DELETE", "/api/admin/agents?server_id=srv-bound", agentTok, nil, http.StatusForbidden},
		{"agent POST enroll-codes", "POST", "/api/admin/enroll-codes", agentTok, codeBody, http.StatusForbidden},

		{"read GET metrics", "GET", "/api/metrics?range=24h", readTok, nil, http.StatusOK},
		{"read POST ingest", "POST", "/api/ingest?type=metric", readTok, metricBody, http.StatusForbidden},
		{"read GET agents", "GET", "/api/admin/agents", readTok, nil, http.StatusOK},
		{"read DELETE agents", "DELETE", "/api/admin/agents?server_id=srv-bound", readTok, nil, http.StatusForbidden},
		{"read POST enroll-codes", "POST", "/api/admin/enroll-codes", readTok, codeBody, http.StatusForbidden},
		{"read GET clients", "GET", "/api/admin/clients", readTok, nil, http.StatusForbidden},

		{"legacy GET metrics", "GET", "/api/metrics?range=24h", legacyTok, nil, http.StatusOK},
		{"legacy POST ingest", "POST", "/api/ingest?type=metric", legacyTok, metricBody, http.StatusAccepted},
		{"legacy POST enroll-codes", "POST", "/api/admin/enroll-codes", legacyTok, codeBody, http.StatusForbidden},
		{"legacy GET clients", "GET", "/api/admin/clients", legacyTok, nil, http.StatusForbidden},
		{"legacy DELETE agents", "DELETE", "/api/admin/agents?server_id=srv-bound", legacyTok, nil, http.StatusForbidden},

		{"admin GET clients", "GET", "/api/admin/clients", adminTok, nil, http.StatusOK},
		{"admin POST enroll-codes", "POST", "/api/admin/enroll-codes", adminTok, codeBody, http.StatusCreated},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := do(srv, tc.method, tc.target, tc.key, tc.body)
			if w.Code != tc.want {
				t.Fatalf("%s %s: got %d want %d body %s", tc.method, tc.target, w.Code, tc.want, w.Body.String())
			}
		})
	}

	// Handshake itself does not use X-API-Key.
	w := do(srv, "POST", "/api/enroll", "", enrollBody)
	if w.Code != http.StatusCreated {
		t.Fatalf("enroll handshake: got %d %s", w.Code, w.Body.String())
	}
}

func TestP103AgentTokenCannotWriteSiblingServer(t *testing.T) {
	db, err := storage.Open(filepath.Join(t.TempDir(), "bound.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	srv := server.New(db, "0")
	srv.EnableHubMode(db)

	tok, _ := storage.GenerateToken(storage.KindAgent)
	mint(t, db, storage.APIKeyRecord{
		TenantID: "t_bound", ClientName: "BoundCo", Kind: storage.KindAgent, Scope: storage.ScopeIngest, ServerID: "host-a",
	}, tok)

	payload, _ := json.Marshal(map[string]interface{}{
		"Timestamp": time.Now(), "ServerID": "host-b", "Hostname": "evil", "CPUPct": 9.0, "MemPct": 9.0,
	})
	w := do(srv, "POST", "/api/ingest?type=metric", tok, payload)
	if w.Code != http.StatusForbidden {
		t.Fatalf("got %d want 403 body %s", w.Code, w.Body.String())
	}
	rows, err := db.QueryMetrics(time.Now().Add(-time.Hour), "t_bound")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.ServerID == "host-b" {
			t.Fatal("token for A wrote B-labeled metrics")
		}
	}

	okBody, _ := json.Marshal(map[string]interface{}{
		"Timestamp": time.Now(), "ServerID": "host-a", "CPUPct": 2.0, "MemPct": 2.0,
	})
	if w := do(srv, "POST", "/api/ingest?type=metric", tok, okBody); w.Code != http.StatusAccepted {
		t.Fatalf("matching ingest: %d %s", w.Code, w.Body.String())
	}

	proc, _ := json.Marshal(map[string]interface{}{
		"Timestamp": time.Now(), "ServerID": "host-b", "PID": 1, "Name": "x", "CPUPct": 1.0, "MemMB": 1.0,
	})
	if w := do(srv, "POST", "/api/ingest?type=process", tok, proc); w.Code != http.StatusForbidden {
		t.Fatalf("process spoof: %d", w.Code)
	}
}

func TestP103RevokedKeyStaysRevokedAfterRestart(t *testing.T) {
	dir := t.TempDir()
	db, err := storage.Open(filepath.Join(dir, "restart.db"))
	if err != nil {
		t.Fatal(err)
	}
	const key = "configured-then-revoked"
	if err := db.UpsertAPIKey(storage.APIKeyRecord{
		KeyHash: storage.HashAPIKey(key), TenantID: "t_cfg", ClientName: "default",
		Kind: storage.KindLegacy, Scope: storage.ScopeRead,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.RevokeAPIKey("default"); err != nil {
		t.Fatal(err)
	}
	csvPath := filepath.Join(dir, "clients_registry.csv")
	if err := os.WriteFile(csvPath, []byte("ClientName,APIKey\ndefault,"+key+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Simulate the old auto-seed upsert of a revoked configured key.
	_ = db.UpsertAPIKey(storage.APIKeyRecord{
		KeyHash: storage.HashAPIKey(key), TenantID: "t_cfg", ClientName: "default",
		Kind: storage.KindLegacy, Scope: storage.ScopeRead,
	})
	db.Close()

	db2, err := storage.Open(filepath.Join(dir, "restart.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db2.Close() })
	srv := server.New(db2, "0")
	srv.EnableHubMode(db2)
	if w := do(srv, "GET", "/api/metrics?range=24h", key, nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("revoked key after restart: got %d want 401", w.Code)
	}
}

func TestP103DuplicateClientNamesNeverResolveIdentity(t *testing.T) {
	os.Setenv("WMONITOR_ALLOW_INSECURE_ENROLL", "1")
	t.Cleanup(func() { os.Unsetenv("WMONITOR_ALLOW_INSECURE_ENROLL") })

	db, err := storage.Open(filepath.Join(t.TempDir(), "dup.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	srv := server.New(db, "0")
	srv.EnableHubMode(db)

	adminTok, _ := storage.GenerateToken(storage.KindAdmin)
	mint(t, db, storage.APIKeyRecord{TenantID: "admin", ClientName: "Admin", Kind: storage.KindAdmin, Scope: storage.ScopeAdmin}, adminTok)
	mint(t, db, storage.APIKeyRecord{TenantID: "t_one", ClientName: "Acme", Kind: storage.KindRead, Scope: storage.ScopeRead}, "read-one")
	mint(t, db, storage.APIKeyRecord{TenantID: "t_two", ClientName: "Acme", Kind: storage.KindRead, Scope: storage.ScopeRead}, "read-two")

	body, _ := json.Marshal(map[string]interface{}{"client_name": "Acme", "max_uses": 1, "ttl_hours": 1})
	w := do(srv, "POST", "/api/admin/enroll-codes", adminTok, body)
	if w.Code != http.StatusConflict {
		t.Fatalf("ambiguous name enroll-code: got %d %s", w.Code, w.Body.String())
	}

	create, _ := json.Marshal(map[string]string{"client_name": "Acme"})
	w = do(srv, "POST", "/api/admin/clients", adminTok, create)
	if w.Code != http.StatusConflict {
		t.Fatalf("duplicate client create: got %d %s", w.Code, w.Body.String())
	}

	explicit, _ := json.Marshal(map[string]interface{}{"client_name": "Acme", "tenant_id": "t_one", "max_uses": 1, "ttl_hours": 1})
	w = do(srv, "POST", "/api/admin/enroll-codes", adminTok, explicit)
	if w.Code != http.StatusCreated {
		t.Fatalf("explicit tenant_id should work: %d %s", w.Code, w.Body.String())
	}
}

func TestP103ReadTokenCannotRevokeAgent(t *testing.T) {
	db, err := storage.Open(filepath.Join(t.TempDir(), "norevoke.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	srv := server.New(db, "0")
	srv.EnableHubMode(db)

	readTok, _ := storage.GenerateToken(storage.KindRead)
	mint(t, db, storage.APIKeyRecord{TenantID: "t_acme", ClientName: "Acme", Kind: storage.KindRead, Scope: storage.ScopeRead}, readTok)
	agentTok, _ := storage.GenerateToken(storage.KindAgent)
	mint(t, db, storage.APIKeyRecord{TenantID: "t_acme", ClientName: "Acme", Kind: storage.KindAgent, Scope: storage.ScopeIngest, ServerID: "srv-keep"}, agentTok)

	w := do(srv, "DELETE", "/api/admin/agents?server_id=srv-keep", readTok, nil)
	if w.Code != http.StatusForbidden {
		t.Fatalf("got %d want 403", w.Code)
	}
	if _, err := db.ResolveAPIKey(storage.HashAPIKey(agentTok)); err != nil {
		t.Fatalf("agent token mutated by read DELETE: %v", err)
	}
}
