package server

import (
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"
)

const defaultDailyRowQuota int64 = 1_000_000

type dailyQuotaEntry struct {
	day  string
	rows int64
}

type dailyQuota struct {
	mu      sync.Mutex
	entries map[string]dailyQuotaEntry
	limit   int64
	now     func() time.Time
}

func newDailyQuota(limit int64) *dailyQuota {
	if limit <= 0 {
		limit = defaultDailyRowQuota
	}
	return &dailyQuota{
		entries: make(map[string]dailyQuotaEntry),
		limit:   limit,
		now:     time.Now,
	}
}

func dailyQuotaFromEnv() *dailyQuota {
	limit := defaultDailyRowQuota
	if raw := os.Getenv("WMONITOR_DAILY_ROW_QUOTA"); raw != "" {
		if parsed, err := strconv.ParseInt(raw, 10, 64); err == nil && parsed > 0 {
			limit = parsed
		}
	}
	return newDailyQuota(limit)
}

// allow reserves rows for a tenant. Reservations are deliberately made before
// decoding and writing so concurrent requests cannot race past the ceiling.
func (q *dailyQuota) allow(tenant string, rows int64) bool {
	if rows < 1 {
		rows = 1
	}
	day := q.now().UTC().Format("2006-01-02")

	q.mu.Lock()
	defer q.mu.Unlock()

	entry := q.entries[tenant]
	if entry.day != day {
		entry = dailyQuotaEntry{day: day}
	}
	if entry.rows+rows > q.limit {
		q.entries[tenant] = entry
		return false
	}
	entry.rows += rows
	q.entries[tenant] = entry
	return true
}

func (q *dailyQuota) retryAfter() int {
	now := q.now().UTC()
	next := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, time.UTC)
	seconds := int(next.Sub(now).Seconds())
	if seconds < 1 {
		return 1
	}
	return seconds
}

var ingestDailyQuota = dailyQuotaFromEnv()

// enforceDailyIngestQuota applies only to authenticated ingest writes. The
// current compatibility endpoints carry exactly one row per request; batch
// ingestion can pass its decoded row count here when it is enabled.
func enforceDailyIngestQuota(w http.ResponseWriter, r *http.Request, tenant string) bool {
	if r.Method != http.MethodPost || r.URL.Path != "/api/ingest" {
		return true
	}
	if ingestDailyQuota.allow(tenant, 1) {
		return true
	}
	w.Header().Set("Retry-After", strconv.Itoa(ingestDailyQuota.retryAfter()))
	writeJSONError(w, http.StatusTooManyRequests, "daily ingest quota exceeded")
	return false
}
