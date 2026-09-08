package server

import (
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"
)

// Compatibility helpers for historical unit fixtures. Production accepted-data
// budgets are initialized in New and committed with rows by storage.AcceptIngest.
const defaultDailyRowQuota int64 = 20_000_000

type dailyQuotaEntry struct { day string; rows int64 }
type dailyQuota struct { mu sync.Mutex; entries map[string]dailyQuotaEntry; limit int64; now func() time.Time }

func newDailyQuota(limit int64) *dailyQuota {
	if limit <= 0 { limit = defaultDailyRowQuota }
	return &dailyQuota{entries: make(map[string]dailyQuotaEntry), limit: limit, now: time.Now}
}

func dailyQuotaFromEnv() *dailyQuota {
	limit := defaultDailyRowQuota
	if value, err := strconv.ParseInt(os.Getenv("WMONITOR_DAILY_ROW_QUOTA"), 10, 64); err == nil && value > 0 { limit = value }
	return newDailyQuota(limit)
}

func (q *dailyQuota) allow(tenant string, rows int64) bool {
	if rows < 1 { rows = 1 }
	day := q.now().UTC().Format("2006-01-02")
	q.mu.Lock()
	defer q.mu.Unlock()
	for key, entry := range q.entries { if entry.day != day { delete(q.entries, key) } }
	entry := q.entries[tenant]
	if entry.day != day { entry = dailyQuotaEntry{day: day} }
	if rows > q.limit || entry.rows > q.limit-rows { return false }
	entry.rows += rows
	q.entries[tenant] = entry
	return true
}

func (q *dailyQuota) retryAfter() int {
	now := q.now().UTC()
	seconds := int(time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, time.UTC).Sub(now).Seconds())
	if seconds < 1 { return 1 }
	return seconds
}

// Never capture environment configuration or debit entitlements during auth.
// Malformed bodies, rejected scopes, duplicate events and failed transactions
// consume abuse-control capacity, but no accepted-row/byte allowance.
var ingestDailyQuota = newDailyQuota(defaultDailyRowQuota)
func enforceDailyIngestQuota(w http.ResponseWriter, r *http.Request, tenant string) bool { return true }
