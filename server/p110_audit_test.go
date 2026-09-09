package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"Zeus/server"
	"Zeus/storage"
)

func TestSessionLoginWritesTenantScopedAudit(t *testing.T) {
	srv, db, _ := hubEnrollFixture(t)
	readToken, _ := storage.GenerateToken(storage.KindRead)
	db.UpsertAPIKey(storage.APIKeyRecord{
		TenantID: "t_audit", ClientName: "AuditClient", KeyHash: storage.HashAPIKey(readToken),
		KeyPrefix: "wmr_", Kind: storage.KindRead, Scope: storage.ScopeRead,
	})
	admin, _ := storage.GenerateToken(storage.KindAdmin)
	mint(t, db, storage.APIKeyRecord{TenantID: "admin", ClientName: "Admin", Kind: storage.KindAdmin, Scope: storage.ScopeAdmin}, admin)
	body, _ := json.Marshal(map[string]string{"read_token": readToken})
	req := httptest.NewRequest("POST", "https://example.com/api/session", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://example.com")
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("login: %d %s", w.Code, w.Body.String())
	}
	if w := do(srv, "GET", "/api/admin/audit?tenant_id=t_audit", readToken, nil); w.Code != http.StatusForbidden {
		t.Fatalf("read token listed audit: %d", w.Code)
	}
	listed := do(srv, "GET", "/api/admin/audit?tenant_id=t_audit", admin, nil)
	if listed.Code != http.StatusOK {
		t.Fatalf("admin audit: %d %s", listed.Code, listed.Body.String())
	}
	var payload struct {
		Events []struct {
			Action      string `json:"action"`
			TenantID    string `json:"tenant_id"`
			ActorPrefix string `json:"actor_prefix"`
		} `json:"events"`
	}
	if err := json.Unmarshal(listed.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Events) != 1 || payload.Events[0].Action != "session.login" || payload.Events[0].TenantID != "t_audit" || payload.Events[0].ActorPrefix != "wmr_" {
		t.Fatalf("audit events: %+v", payload.Events)
	}
	foreign := do(srv, "GET", "/api/admin/audit?tenant_id=t_other", admin, nil)
	if foreign.Code != http.StatusOK {
		t.Fatal(foreign.Body.String())
	}
	var empty struct {
		Events []struct{} `json:"events"`
	}
	json.Unmarshal(foreign.Body.Bytes(), &empty)
	if len(empty.Events) != 0 {
		t.Fatal("foreign tenant saw audit events")
	}
	health := do(srv, "GET", "/api/health", "", nil)
	if health.Code != 200 || bytes.Contains(health.Body.Bytes(), []byte("last_seen_at")) {
		t.Fatalf("public health leaked agent inventory: %s", health.Body.String())
	}
	expected := do(srv, "GET", "/api/admin/agents/expected?tenant_id=t_audit", admin, nil)
	if expected.Code != http.StatusOK {
		t.Fatalf("expected agents: %d %s", expected.Code, expected.Body.String())
	}
	if !bytes.Contains(expected.Body.Bytes(), []byte(`"completeness":"not_assessed"`)) {
		t.Fatalf("expected-agent completeness claimed: %s", expected.Body.String())
	}
}

func TestAuditRejectsEmptyTenant(t *testing.T) {
	db, err := storage.Open(filepath.Join(t.TempDir(), "audit.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.AppendAudit(context.Background(), storage.AuditEvent{Action: "session.login", ActorKind: "read"}); err == nil {
		t.Fatal("empty tenant accepted")
	}
	srv := server.New(db, "0")
	srv.EnableHubMode(db)
	admin, _ := storage.GenerateToken(storage.KindAdmin)
	mint(t, db, storage.APIKeyRecord{TenantID: "admin", ClientName: "Admin", Kind: storage.KindAdmin, Scope: storage.ScopeAdmin}, admin)
	if w := do(srv, "GET", "/api/admin/audit", admin, nil); w.Code != http.StatusBadRequest {
		t.Fatalf("missing tenant: %d", w.Code)
	}
}

func TestAdminClientCreateWritesAuditAndReturnsToken(t *testing.T) {
	srv, _, admin := hubEnrollFixture(t)
	body, _ := json.Marshal(map[string]string{"client_name": "AuditNewCo"})
	created := do(srv, "POST", "/api/admin/clients", admin, body)
	if created.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", created.Code, created.Body.String())
	}
	var payload struct {
		TenantID  string `json:"tenant_id"`
		ReadToken string `json:"read_token"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &payload); err != nil || payload.ReadToken == "" || payload.TenantID == "" {
		t.Fatalf("create payload: %s", created.Body.String())
	}
	listed := do(srv, "GET", "/api/admin/audit?tenant_id="+payload.TenantID, admin, nil)
	if listed.Code != http.StatusOK || !bytes.Contains(listed.Body.Bytes(), []byte(`"action":"client.created"`)) {
		t.Fatalf("audit: %d %s", listed.Code, listed.Body.String())
	}
}

func TestMetricsCursorAndCompleteness(t *testing.T) {
	db, err := storage.Open(filepath.Join(t.TempDir(), "page.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	now := time.Now().Add(-time.Minute)
	for i := 0; i < 3; i++ {
		if err := db.InsertMetric(storage.MetricRow{Timestamp: now.Add(time.Duration(i) * time.Second), TenantID: storage.LocalTenantID, ServerID: "s", CPUPct: float64(i)}); err != nil {
			t.Fatal(err)
		}
	}
	srv := server.New(db, "0")
	first := do(srv, "GET", "/api/metrics?range=24h&limit=2", "", nil)
	if first.Code != http.StatusOK {
		t.Fatalf("first: %d %s", first.Code, first.Body.String())
	}
	var page struct {
		Count      int    `json:"count"`
		Complete   bool   `json:"complete"`
		NextCursor string `json:"next_cursor"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if page.Count != 2 || page.Complete || page.NextCursor == "" {
		t.Fatalf("first page: %+v %s", page, first.Body.String())
	}
	second := do(srv, "GET", "/api/metrics?range=24h&limit=2&cursor="+page.NextCursor, "", nil)
	if second.Code != http.StatusOK {
		t.Fatalf("second: %d %s", second.Code, second.Body.String())
	}
	var rest struct {
		Count    int  `json:"count"`
		Complete bool `json:"complete"`
	}
	if err := json.Unmarshal(second.Body.Bytes(), &rest); err != nil {
		t.Fatal(err)
	}
	if rest.Count != 1 || !rest.Complete {
		t.Fatalf("second page: %+v %s", rest, second.Body.String())
	}
}

func TestServersCursorAndCompleteness(t *testing.T) {
	db, err := storage.Open(filepath.Join(t.TempDir(), "servers.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	now := time.Now().Add(-time.Minute)
	for _, id := range []string{"srv-a", "srv-b", "srv-c"} {
		if err := db.InsertMetric(storage.MetricRow{Timestamp: now, TenantID: storage.LocalTenantID, ServerID: id, CPUPct: 1}); err != nil {
			t.Fatal(err)
		}
	}
	srv := server.New(db, "0")
	first := do(srv, "GET", "/api/servers?limit=2", "", nil)
	if first.Code != http.StatusOK {
		t.Fatalf("first: %d %s", first.Code, first.Body.String())
	}
	var page struct {
		Servers    []string `json:"servers"`
		Complete   bool     `json:"complete"`
		NextCursor string   `json:"next_cursor"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Servers) != 2 || page.Complete || page.NextCursor != "srv-b" {
		t.Fatalf("first page: %+v", page)
	}
	second := do(srv, "GET", "/api/servers?limit=2&cursor="+page.NextCursor, "", nil)
	if second.Code != http.StatusOK {
		t.Fatalf("second: %d %s", second.Code, second.Body.String())
	}
	if err := json.Unmarshal(second.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Servers) != 1 || page.Servers[0] != "srv-c" || !page.Complete {
		t.Fatalf("second page: %+v", page)
	}
}
