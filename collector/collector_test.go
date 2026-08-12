package collector_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"Zeus/collector"
	"Zeus/storage"
)

// TestCollectorRealData runs the collector for 35 seconds and verifies
// that at least 3 rows of real (non-zero) metric data are written to the DB.
func TestCollectorRealData(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping long-running collector test in -short mode")
	}

	tmp := filepath.Join(t.TempDir(), "collector_test.db")
	db, err := storage.Open(tmp)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	ctx, cancel := context.WithCancel(context.Background())

	// Use a 10-second poll interval
	col := collector.NewWithInterval(db, 10*time.Second)

	// Run for 35 seconds to get at least 3 samples
	go col.Run(ctx)

	t.Log("Collector running for 35s...")
	time.Sleep(35 * time.Second)
	cancel()

	// Query all rows
	rows, err := db.QueryMetrics(time.Now().Add(-2 * time.Minute))
	if err != nil {
		t.Fatalf("QueryMetrics: %v", err)
	}

	t.Logf("Collected %d metric rows in 35s", len(rows))
	if len(rows) < 3 {
		t.Errorf("expected at least 3 rows, got %d", len(rows))
	}

	// Verify at least one row has plausible values
	plausible := 0
	for _, r := range rows {
		// MemPct should always be > 0 on a live system
		if r.MemPct > 0 && r.DiskFreeGB > 0 {
			plausible++
			t.Logf("  ts=%v cpu=%.2f%% mem=%.2f%% disk_free=%.1fGB net_sent=%d net_recv=%d",
				r.Timestamp.Format("15:04:05"), r.CPUPct, r.MemPct, r.DiskFreeGB, r.NetSentBytes, r.NetRecvBytes)
		}
	}
	if plausible == 0 {
		t.Error("all rows have zero mem/disk — collector may not be working correctly")
	}
}
