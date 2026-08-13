package retention_test

import (
	"path/filepath"
	"testing"
	"time"

	"Zeus/retention"
	"Zeus/storage"
)

func TestRetentionPurgeAndDownsample(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "test_retention.db")
	db, err := storage.Open(tmp)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	now := time.Now().UTC()

	// --- Insert synthetic data ---

	// 5 rows that are 31 days old (should be DELETED)
	old31d := now.Add(-31 * 24 * time.Hour)
	for i := 0; i < 5; i++ {
		ts := old31d.Add(time.Duration(i) * 10 * time.Second)
		db.InsertMetric(storage.MetricRow{
			Timestamp: ts, CPUPct: 10, MemPct: 40, DiskFreeGB: 100,
			NetSentBytes: 1000, NetRecvBytes: 2000,
		})
	}

	// 12 rows that are 25 hours old, all within the same hour (should be DOWNSAMPLED to 1)
	old25h := now.Add(-25 * time.Hour)
	// Align to a clean hour boundary so they all fall in the same bucket
	hourBucket := old25h.Truncate(time.Hour)
	for i := 0; i < 12; i++ {
		ts := hourBucket.Add(time.Duration(i) * 5 * time.Minute)
		db.InsertMetric(storage.MetricRow{
			Timestamp: ts, CPUPct: float64(20 + i), MemPct: 50, DiskFreeGB: 90,
			NetSentBytes: 500, NetRecvBytes: 1500,
			DiskIOPS: float64(100 + i*10), NetMBps: float64(1.0 + float64(i)*0.1), ConcurrentUsers: 2 + i%3,
		})
	}

	// 3 rows that are recent (< 24h) — should be UNTOUCHED
	for i := 0; i < 3; i++ {
		ts := now.Add(-time.Duration(i) * 10 * time.Minute)
		db.InsertMetric(storage.MetricRow{
			Timestamp: ts, CPUPct: 55, MemPct: 60, DiskFreeGB: 80,
			NetSentBytes: 9000, NetRecvBytes: 8000,
			DiskIOPS: 250, NetMBps: 3.5, ConcurrentUsers: 10,
		})
	}

	beforeCount, _ := db.CountMetrics()
	t.Logf("Before retention: %d metric rows", beforeCount)
	if beforeCount != 20 {
		t.Errorf("expected 20 rows before, got %d", beforeCount)
	}

	// --- Run retention ---
	job := retention.New(db.Conn())
	if err := job.Run(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	afterCount, _ := db.CountMetrics()
	t.Logf("After retention: %d metric rows", afterCount)

	// Expected:
	//   - 5 old-31d rows → deleted
	//   - 12 old-25h rows in one hour bucket → collapsed to 1 average row
	//   - 3 recent rows → untouched
	// Total expected = 1 + 3 = 4
	if afterCount != 4 {
		t.Errorf("expected 4 rows after retention, got %d", afterCount)
	}

	// Verify the averaged row has a plausible CPU, DiskIOPS, NetMBps, and ConcurrentUsers value
	rows, err := db.QueryMetrics(time.Unix(0, 0))
	if err != nil {
		t.Fatalf("QueryMetrics: %v", err)
	}
	var foundAvg bool
	for _, r := range rows {
		if r.Timestamp.Unix() == hourBucket.Unix() {
			// This is our averaged row
			foundAvg = true
			if r.CPUPct < 20 || r.CPUPct > 32 {
				t.Errorf("averaged CPUPct out of range: %v", r.CPUPct)
			}
			if r.DiskIOPS < 100 || r.DiskIOPS > 220 {
				t.Errorf("averaged DiskIOPS out of range: %v", r.DiskIOPS)
			}
			if r.ConcurrentUsers < 1 || r.ConcurrentUsers > 5 {
				t.Errorf("averaged ConcurrentUsers out of range: %v", r.ConcurrentUsers)
			}
			t.Logf("Averaged row: ts=%v cpu=%.2f iops=%.1f users=%d", r.Timestamp, r.CPUPct, r.DiskIOPS, r.ConcurrentUsers)
		}
	}
	if !foundAvg {
		t.Errorf("expected to find an averaged row at hour bucket ts=%d; got rows: %v",
			hourBucket.Unix(), func() []int64 {
				var tss []int64
				for _, r := range rows { tss = append(tss, r.Timestamp.Unix()) }
				return tss
			}())
	}
}
