package storage_test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"Zeus/storage"
)

func TestStorageRoundtrip(t *testing.T) {
	// Use a temp file for the test DB
	tmp := filepath.Join(t.TempDir(), "test.db")
	db, err := storage.Open(tmp)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	now := time.Now().UTC()

	// Insert 5 fake metric rows
	for i := 0; i < 5; i++ {
		m := storage.MetricRow{
			Timestamp:       now.Add(time.Duration(i) * 10 * time.Second),
			ServerID:        "srv-test",
			Hostname:        "testhost",
			CPUPct:          float64(10 + i*5),
			MemPct:          float64(40 + i*2),
			DiskFreeGB:      float64(100 - i),
			NetSentBytes:    uint64(1000 * i),
			NetRecvBytes:    uint64(2000 * i),
			DiskIOPS:        float64(50 + i*10),
			NetMBps:         float64(1.5 + float64(i)*0.5),
			ConcurrentUsers: i + 1,
			NetSentExternal: uint64(500 * i),
			NetRecvExternal: uint64(800 * i),
			NetSentInternal: uint64(500 * i),
			NetRecvInternal: uint64(1200 * i),
		}
		if err := db.InsertMetric(m); err != nil {
			t.Fatalf("InsertMetric[%d]: %v", i, err)
		}
	}

	// Insert 5 fake process rows
	for i := 0; i < 5; i++ {
		p := storage.ProcessRow{
			Timestamp: now.Add(time.Duration(i) * 10 * time.Second),
			ServerID:  "srv-test",
			Hostname:  "testhost",
			PID:       int32(1000 + i),
			Name:      fmt.Sprintf("proc_%d", i),
			CPUPct:    float64(i),
			MemMB:     float64(50 + i*10),
		}
		if err := db.InsertProcess(p); err != nil {
			t.Fatalf("InsertProcess[%d]: %v", i, err)
		}
	}

	// Read back
	metrics, err := db.QueryMetrics(now.Add(-time.Minute), "")
	if err != nil {
		t.Fatalf("QueryMetrics: %v", err)
	}
	if len(metrics) != 5 {
		t.Errorf("expected 5 metric rows, got %d", len(metrics))
	}

	processes, err := db.QueryProcesses(now.Add(-time.Minute), "")
	if err != nil {
		t.Fatalf("QueryProcesses: %v", err)
	}
	if len(processes) != 5 {
		t.Errorf("expected 5 process rows, got %d", len(processes))
	}

	// Verify counts
	mc, _ := db.CountMetrics()
	pc, _ := db.CountProcesses()

	t.Logf("DB path: %s", tmp)
	t.Logf("Metric rows: %d, Process rows: %d", mc, pc)

	// Verify spot values
	if metrics[0].CPUPct != 10.0 {
		t.Errorf("expected CPUPct=10.0, got %v", metrics[0].CPUPct)
	}
	if metrics[0].DiskIOPS != 50.0 {
		t.Errorf("expected DiskIOPS=50.0, got %v", metrics[0].DiskIOPS)
	}
	if metrics[0].ConcurrentUsers != 1 {
		t.Errorf("expected ConcurrentUsers=1, got %v", metrics[0].ConcurrentUsers)
	}
	if metrics[0].ServerID != "srv-test" {
		t.Errorf("expected ServerID=srv-test, got %q", metrics[0].ServerID)
	}
	if metrics[0].Hostname != "testhost" {
		t.Errorf("expected Hostname=testhost, got %q", metrics[0].Hostname)
	}
	if processes[2].Name != "proc_2" {
		t.Errorf("expected name=proc_2, got %v", processes[2].Name)
	}

	// Test QueryServers
	servers, err := db.QueryServers("")
	if err != nil {
		t.Fatalf("QueryServers: %v", err)
	}
	if len(servers) != 1 || servers[0] != "srv-test" {
		t.Errorf("expected [srv-test] from QueryServers, got %v", servers)
	}

	// Confirm DB file exists on disk
	if _, err := os.Stat(tmp); os.IsNotExist(err) {
		t.Error("DB file not found on disk")
	}
}

// BenchmarkInsertMetric measures SQLite insert throughput for a single MetricRow.
func BenchmarkInsertMetric(b *testing.B) {
	tmp := filepath.Join(b.TempDir(), "bench.db")
	db, err := storage.Open(tmp)
	if err != nil {
		b.Fatalf("Open: %v", err)
	}
	defer db.Close()

	m := storage.MetricRow{
		Timestamp:   time.Now(),
		ServerID:    "bench-srv",
		CPUPct:      42.0,
		MemPct:      55.0,
		DiskFreeGB:  100.0,
		NetSentBytes: 1024,
		NetRecvBytes: 2048,
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		m.Timestamp = time.Now()
		if err := db.InsertMetric(m); err != nil {
			b.Fatalf("InsertMetric: %v", err)
		}
	}
	b.StopTimer()
	count, _ := db.CountMetrics()
	b.Logf("Inserted %d rows", count)
}

// BenchmarkQueryMetrics measures query performance over 1000 rows.
func BenchmarkQueryMetrics(b *testing.B) {
	tmp := filepath.Join(b.TempDir(), "bench_query.db")
	db, err := storage.Open(tmp)
	if err != nil {
		b.Fatalf("Open: %v", err)
	}
	defer db.Close()

	now := time.Now()
	for i := 0; i < 1000; i++ {
		db.InsertMetric(storage.MetricRow{
			Timestamp:  now.Add(time.Duration(i) * time.Second),
			ServerID:   "bench-srv",
			CPUPct:     float64(i % 100),
			MemPct:     50.0,
			DiskFreeGB: 100.0,
		})
	}

	since := now.Add(-2 * time.Hour)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rows, err := db.QueryMetrics(since, "")
		if err != nil {
			b.Fatalf("QueryMetrics: %v", err)
		}
		if len(rows) == 0 {
			b.Fatal("expected rows")
		}
	}
}
