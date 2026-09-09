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

func mint(t *testing.T, db *storage.DB, record storage.APIKeyRecord, plaintext string) string {
	t.Helper()
	if record.KeyHash == "" {
		record.KeyHash = storage.HashAPIKey(plaintext)
	}
	if record.KeyPrefix == "" {
		record.KeyPrefix = storage.ExtractKeyPrefix(plaintext)
	}
	if record.CreatedAt.IsZero() {
		record.CreatedAt = time.Now()
	}
	if err := db.UpsertAPIKey(record); err != nil {
		t.Fatal(err)
	}
	return plaintext
}

func TestP103RouteMethodKindScopeMatrix(t *testing.T) {
	t.Setenv("WMONITOR_ALLOW_INSECURE_ENROLL", "1")
	db, err := storage.Open(filepath.Join(t.TempDir(), "matrix.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	srv := server.New(db, "0")
	srv.EnableHubMode(db)
	admin, _ := storage.GenerateToken(storage.KindAdmin)
	read, _ := storage.GenerateToken(storage.KindRead)
	agent, _ := storage.GenerateToken(storage.KindAgent)
	legacy := "legacy-all-tenant-key"
	mint(t, db, storage.APIKeyRecord{TenantID: "admin", ClientName: "Admin", Kind: storage.KindAdmin, Scope: storage.ScopeAdmin}, admin)
	mint(t, db, storage.APIKeyRecord{TenantID: "t_read", ClientName: "Reader", Kind: storage.KindRead, Scope: storage.ScopeRead}, read)
	mint(t, db, storage.APIKeyRecord{TenantID: "t_agent", ClientName: "AgentCo", Kind: storage.KindAgent, Scope: storage.ScopeIngest, ServerID: "srv-bound"}, agent)
	mint(t, db, storage.APIKeyRecord{TenantID: "t_legacy", ClientName: "LegacyCo", Kind: storage.KindLegacy, Scope: storage.ScopeAll}, legacy)
	code, _ := storage.GenerateEnrollCode()
	mint(t, db, storage.APIKeyRecord{TenantID: "t_enroll", ClientName: "EnrollCo", Kind: storage.KindEnroll, Scope: storage.ScopeEnroll, MaxUses: 5, ExpiresAt: time.Now().Add(time.Hour), KeyHash: storage.HashEnrollCode(code), KeyPrefix: "wme_"}, code)
	metric, _ := json.Marshal(storage.MetricRow{Timestamp: time.Now(), ServerID: "srv-bound", CPUPct: 1, MemPct: 1})
	codeBody, _ := json.Marshal(map[string]interface{}{"client_name": "Reader", "tenant_id": "t_read", "max_uses": 1, "ttl_hours": 1})
	clientBody, _ := json.Marshal(map[string]string{"client_name": "BrandNewCo"})
	type row struct {
		name, method, path, key string
		body                    []byte
		want                    int
	}
	cases := []row{
		{"enroll read metrics", "GET", "/api/metrics", code, nil, 401},
		{"enroll ingest", "POST", "/api/ingest?type=metric", code, metric, 401},
		{"enroll read agents", "GET", "/api/admin/agents", code, nil, 401},
		{"enroll revoke", "DELETE", "/api/admin/agents?server_id=srv-bound", code, nil, 401},
		{"enroll issue", "POST", "/api/admin/enroll-codes", code, codeBody, 401},
		{"enroll list clients", "GET", "/api/admin/clients", code, nil, 401},
		{"enroll create client", "POST", "/api/admin/clients", code, clientBody, 401},
		{"agent read", "GET", "/api/metrics", agent, nil, 403},
		{"agent ingest", "POST", "/api/ingest?type=metric", agent, metric, 202},
		{"agent list agents", "GET", "/api/admin/agents", agent, nil, 403},
		{"agent revoke", "DELETE", "/api/admin/agents?server_id=srv-bound", agent, nil, 403},
		{"agent issue", "POST", "/api/admin/enroll-codes", agent, codeBody, 403},
		{"read metrics", "GET", "/api/metrics", read, nil, 200},
		{"read ingest", "POST", "/api/ingest?type=metric", read, metric, 403},
		{"read list agents", "GET", "/api/admin/agents", read, nil, 200},
		{"read revoke", "DELETE", "/api/admin/agents?server_id=srv-bound", read, nil, 403},
		{"read issue", "POST", "/api/admin/enroll-codes", read, codeBody, 403},
		{"read list clients", "GET", "/api/admin/clients", read, nil, 403},
		{"legacy read", "GET", "/api/metrics", legacy, nil, 200},
		// Scope=all no longer bypasses the machine-binding requirement.
		{"legacy ingest", "POST", "/api/ingest?type=metric", legacy, metric, 403},
		{"legacy issue", "POST", "/api/admin/enroll-codes", legacy, codeBody, 403},
		{"legacy list clients", "GET", "/api/admin/clients", legacy, nil, 403},
		{"legacy revoke", "DELETE", "/api/admin/agents?server_id=srv-bound", legacy, nil, 403},
		{"admin list", "GET", "/api/admin/clients", admin, nil, 200},
		{"admin issue", "POST", "/api/admin/enroll-codes", admin, codeBody, 201},
		{"admin not machine", "POST", "/api/ingest?type=metric", admin, metric, 403},
		{"read audit", "GET", "/api/admin/audit?tenant_id=t_read", read, nil, 403},
		{"admin audit", "GET", "/api/admin/audit?tenant_id=t_read", admin, nil, 200},
		{"read expected agents", "GET", "/api/admin/agents/expected?tenant_id=t_read", read, nil, 403},
		{"admin expected agents", "GET", "/api/admin/agents/expected?tenant_id=t_agent", admin, nil, 200},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if w := do(srv, tc.method, tc.path, tc.key, tc.body); w.Code != tc.want {
				t.Fatalf("%s: got %d want %d: %s", tc.path, w.Code, tc.want, w.Body.String())
			}
		})
	}
	body, _ := json.Marshal(map[string]string{"enroll_code": code, "server_id": "srv-new"})
	if w := do(srv, "POST", "/api/enroll", "", body); w.Code != http.StatusCreated {
		t.Fatalf("handshake failed: %d %s", w.Code, w.Body.String())
	}
}

func TestP103AgentTokenCannotWriteSiblingServer(t *testing.T) {
	db, err := storage.Open(filepath.Join(t.TempDir(), "bound.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	srv := server.New(db, "0")
	srv.EnableHubMode(db)
	token, _ := storage.GenerateToken(storage.KindAgent)
	mint(t, db, storage.APIKeyRecord{TenantID: "t_bound", ClientName: "BoundCo", Kind: storage.KindAgent, Scope: storage.ScopeIngest, ServerID: "host-a"}, token)
	metric, _ := json.Marshal(storage.MetricRow{Timestamp: time.Now(), ServerID: "host-b", CPUPct: 9, MemPct: 9})
	process, _ := json.Marshal(storage.ProcessRow{Timestamp: time.Now(), ServerID: "host-b", PID: 1, Name: "x", CPUPct: 1, MemMB: 1})
	for _, attempt := range []struct {
		kind string
		body []byte
	}{{"metric", metric}, {"process", process}} {
		if w := do(srv, "POST", "/api/ingest?type="+attempt.kind, token, attempt.body); w.Code != 400 {
			t.Fatalf("invalid bound identity: %d", w.Code)
		}
	}
	rows, err := db.QueryMetrics(time.Now().Add(-time.Hour), "t_bound")
	if err != nil || len(rows) != 0 {
		t.Fatal("spoofed metric inserted")
	}
	processes, err := db.QueryProcesses(time.Now().Add(-time.Hour), "t_bound")
	if err != nil || len(processes) != 0 {
		t.Fatal("spoofed process inserted")
	}
	metric, _ = json.Marshal(storage.MetricRow{Timestamp: time.Now(), ServerID: "host-a", CPUPct: 2, MemPct: 2})
	if w := do(srv, "POST", "/api/ingest?type=metric", token, metric); w.Code != 202 {
		t.Fatalf("matching ingest failed: %d", w.Code)
	}
}

func TestP103RevokedKeyStaysRevokedAfterRestart(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "restart.db")
	db, err := storage.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	const key = "configured-then-revoked"
	record := storage.APIKeyRecord{KeyHash: storage.HashAPIKey(key), TenantID: "t_cfg", ClientName: "default", Kind: storage.KindLegacy, Scope: storage.ScopeRead}
	if err := db.UpsertAPIKey(record); err != nil {
		t.Fatal(err)
	}
	if _, err := db.RevokeAPIKey("default"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "clients_registry.csv"), []byte("ClientName,APIKey\ndefault,"+key+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	_ = db.UpsertAPIKey(record)
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	db, err = storage.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	srv := server.New(db, "0")
	srv.EnableHubMode(db)
	if w := do(srv, "GET", "/api/metrics", key, nil); w.Code != 401 {
		t.Fatal("revoked credential resurrected")
	}
}

func TestP103DuplicateClientNamesNeverResolveIdentity(t *testing.T) {
	t.Setenv("WMONITOR_ALLOW_INSECURE_ENROLL", "1")
	db, err := storage.Open(filepath.Join(t.TempDir(), "dup.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	srv := server.New(db, "0")
	srv.EnableHubMode(db)
	admin, _ := storage.GenerateToken(storage.KindAdmin)
	mint(t, db, storage.APIKeyRecord{TenantID: "admin", ClientName: "Admin", Kind: storage.KindAdmin, Scope: storage.ScopeAdmin}, admin)
	mint(t, db, storage.APIKeyRecord{TenantID: "t_one", ClientName: "Acme", Kind: storage.KindRead, Scope: storage.ScopeRead}, "read-one")
	mint(t, db, storage.APIKeyRecord{TenantID: "t_two", ClientName: "Acme", Kind: storage.KindRead, Scope: storage.ScopeRead}, "read-two")
	body, _ := json.Marshal(map[string]interface{}{"client_name": "Acme", "max_uses": 1, "ttl_hours": 1})
	if w := do(srv, "POST", "/api/admin/enroll-codes", admin, body); w.Code != 409 {
		t.Fatal("ambiguous name resolved")
	}
	body, _ = json.Marshal(map[string]string{"client_name": "Acme"})
	if w := do(srv, "POST", "/api/admin/clients", admin, body); w.Code != 409 {
		t.Fatal("duplicate client created")
	}
	body, _ = json.Marshal(map[string]interface{}{"client_name": "Acme", "tenant_id": "t_one", "max_uses": 1, "ttl_hours": 1})
	if w := do(srv, "POST", "/api/admin/enroll-codes", admin, body); w.Code != 201 {
		t.Fatalf("explicit tenant rejected: %d", w.Code)
	}
}

func TestP103ReadTokenCannotRevokeAgent(t *testing.T) {
	db, err := storage.Open(filepath.Join(t.TempDir(), "norevoke.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	srv := server.New(db, "0")
	srv.EnableHubMode(db)
	read, _ := storage.GenerateToken(storage.KindRead)
	agent, _ := storage.GenerateToken(storage.KindAgent)
	mint(t, db, storage.APIKeyRecord{TenantID: "t_acme", ClientName: "Acme", Kind: storage.KindRead, Scope: storage.ScopeRead}, read)
	mint(t, db, storage.APIKeyRecord{TenantID: "t_acme", ClientName: "Acme", Kind: storage.KindAgent, Scope: storage.ScopeIngest, ServerID: "srv-keep"}, agent)
	if w := do(srv, "DELETE", "/api/admin/agents?server_id=srv-keep", read, nil); w.Code != 403 {
		t.Fatal("read-only revocation allowed")
	}
	if _, err := db.ResolveAPIKey(storage.HashAPIKey(agent)); err != nil {
		t.Fatal("read-only request changed agent credential")
	}
}
