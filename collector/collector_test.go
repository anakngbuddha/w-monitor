package collector_test

import (
	"context"
	"net"
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
	rows, err := db.QueryMetrics(time.Now().Add(-2*time.Minute), "")
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

func TestTCPUserTracker(t *testing.T) {
	tracker := collector.NewTCPUserTracker()

	// Initial count
	cnt := tracker.GetConcurrentUsers()
	t.Logf("Initial concurrent users: %d", cnt)

	// Test manual record
	tracker.RecordUser("user1")
	tracker.RecordUser("user2")
	cnt = tracker.GetConcurrentUsers()
	if cnt < 2 {
		t.Errorf("expected at least 2 users after manual record, got %d", cnt)
	}

	// Test real HTTP server traffic
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("net.Listen: %v", err)
	}
	defer listener.Close()

	port := uint32(listener.Addr().(*net.TCPAddr).Port)
	tracker2 := collector.NewTCPUserTracker()
	tracker2.SetAppPorts(port)

	// Dial the listening port
	conn, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatalf("net.Dial: %v", err)
	}
	defer conn.Close()

	serverConn, err := listener.Accept()
	if err != nil {
		t.Fatalf("listener.Accept: %v", err)
	}
	defer serverConn.Close()

	// Give OS socket table a moment to reflect ESTABLISHED state
	time.Sleep(100 * time.Millisecond)

	users := tracker2.GetConcurrentUsers()
	t.Logf("Detected concurrent users on port %d: %d", port, users)
	if users < 1 {
		t.Errorf("expected at least 1 user on test app port %d, got %d", port, users)
	}
}
