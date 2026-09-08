package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"Zeus/storage"
)

func TestDailyQuotaIsTenantScopedAndResets(t *testing.T) {
	q := newDailyQuota(2)
	now := time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC)
	q.now = func() time.Time { return now }
	if !q.allow("a", 1) || !q.allow("a", 1) || q.allow("a", 1) || !q.allow("b", 1) { t.Fatal("tenant quota isolation failed") }
	now = now.Add(24*time.Hour)
	if !q.allow("a", 1) { t.Fatal("UTC rollover failed") }
}
func TestDailyQuotaCountsRowsAtomically(t *testing.T) {
	q := newDailyQuota(3)
	if !q.allow("tenant", 2) || q.allow("tenant", 2) || !q.allow("tenant", 1) { t.Fatal("rejected reservation consumed allowance") }
}

func TestAuthenticationDoesNotDebitAcceptedData(t *testing.T) {
	old := ingestDailyQuota
	ingestDailyQuota = newDailyQuota(1)
	defer func() { ingestDailyQuota = old }()
	for i := 0; i < 3; i++ {
		r := httptest.NewRequest("POST", "/api/ingest?type=metric", nil)
		if !enforceDailyIngestQuota(httptest.NewRecorder(), r, "tenant") { t.Fatal("authentication charged an unvalidated body") }
	}
	if !ingestDailyQuota.allow("tenant", 1) { t.Fatal("auth consumed accepted-data allowance") }
}

func TestAcceptedQuotaRejectsNewRowsButAllowsReplayAcrossServerRestart(t *testing.T) {
	db, err := storage.Open(filepath.Join(t.TempDir(), "budget.db"))
	if err != nil { t.Fatal(err) }
	defer db.Close()
	const token = "fixture-agent-key"
	if err := db.UpsertAPIKey(storage.APIKeyRecord{KeyHash: storage.HashAPIKey(token), TenantID: "tenant", ClientName: "Fixture", Kind: storage.KindAgent, Scope: storage.ScopeIngest, ServerID: "agent"}); err != nil { t.Fatal(err) }
	srv := New(db, "0")
	srv.ingestPolicy.DailyRows, srv.ingestPolicy.AgentDailyRows = 1, 1
	srv.EnableHubMode(db)
	request := func(server *Server, body []byte) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/api/v1/ingest/batches", bytes.NewReader(body))
		r.Header.Set("X-API-Key", token)
		w := httptest.NewRecorder(); server.Handler().ServeHTTP(w, r); return w
	}
	if w := request(srv, []byte(`{"bad":true}`)); w.Code != 400 { t.Fatalf("malformed request=%d", w.Code) }
	batch := storage.IngestBatch{SchemaVersion: storage.IngestSchema, Events: []storage.IngestEvent{{EventID: "one", BootID: "boot", Sequence: 1, Metric: &storage.MetricRow{Timestamp: time.Now(), CPUPct: 1, MemPct: 1}}}}
	body, _ := json.Marshal(batch)
	if w := request(srv, body); w.Code != 202 { t.Fatalf("first accepted=%d %s", w.Code, w.Body.String()) }
	restarted := New(db, "0")
	restarted.ingestPolicy = srv.ingestPolicy
	restarted.EnableHubMode(db)
	if w := request(restarted, body); w.Code != 202 { t.Fatalf("replay at exhausted budget=%d", w.Code) }
	batch.Events[0].EventID, batch.Events[0].Sequence = "two", 2
	body, _ = json.Marshal(batch)
	if w := request(restarted, body); w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") == "" { t.Fatalf("new row bypassed durable quota: %d", w.Code) }
	rows, err := db.QueryMetricsQ(storage.MetricQuery{Ctx: context.Background(), Since: time.Now().Add(-time.Hour), TenantID: "tenant"})
	if err != nil || len(rows) != 1 { t.Fatal("malformed/replayed/rejected requests changed accepted data") }
}
