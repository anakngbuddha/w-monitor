package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestDailyQuotaIsTenantScopedAndResets(t *testing.T) {
	q := newDailyQuota(2)
	now := time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC)
	q.now = func() time.Time { return now }

	if !q.allow("tenant-a", 1) || !q.allow("tenant-a", 1) {
		t.Fatal("tenant-a should receive its first two rows")
	}
	if q.allow("tenant-a", 1) {
		t.Fatal("tenant-a should be blocked after reaching its quota")
	}
	if !q.allow("tenant-b", 1) {
		t.Fatal("tenant-b must have an independent quota")
	}

	now = now.Add(24 * time.Hour)
	if !q.allow("tenant-a", 1) {
		t.Fatal("quota should reset at the next UTC day")
	}
}

func TestDailyQuotaCountsRowsAtomically(t *testing.T) {
	q := newDailyQuota(3)
	if !q.allow("tenant", 2) {
		t.Fatal("first reservation should fit")
	}
	if q.allow("tenant", 2) {
		t.Fatal("reservation crossing the limit must be rejected")
	}
	if !q.allow("tenant", 1) {
		t.Fatal("a rejected reservation must not consume quota")
	}
}

func TestEnforceDailyIngestQuotaIgnoresReads(t *testing.T) {
	old := ingestDailyQuota
	ingestDailyQuota = newDailyQuota(1)
	defer func() { ingestDailyQuota = old }()

	read := httptest.NewRequest(http.MethodGet, "/api/metrics", nil)
	if !enforceDailyIngestQuota(httptest.NewRecorder(), read, "tenant") {
		t.Fatal("read endpoint should not consume ingest quota")
	}

	post := httptest.NewRequest(http.MethodPost, "/api/ingest?type=metric", nil)
	if !enforceDailyIngestQuota(httptest.NewRecorder(), post, "tenant") {
		t.Fatal("first ingest should be allowed")
	}

	rr := httptest.NewRecorder()
	if enforceDailyIngestQuota(rr, post, "tenant") {
		t.Fatal("second ingest should be rejected")
	}
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", rr.Code)
	}
	if rr.Header().Get("Retry-After") == "" {
		t.Fatal("429 response must include Retry-After")
	}
}
