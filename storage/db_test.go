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
			CPUPct:          float64(10 + i*5),
			MemPct:          float64(40 + i*2),
			DiskFreeGB:      float64(100 - i),
			NetSentBytes:    uint64(1000 * i),
			NetRecvBytes:    uint64(2000 * i),
			DiskIOPS:        float64(50 + i*10),
			NetMBps:         float64(1.5 + float64(i)*0.5),
			ConcurrentUsers: i + 1,
		}
		if err := db.InsertMetric(m); err != nil {
			t.Fatalf("InsertMetric[%d]: %v", i, err)
		}
	}

	// Insert 5 fake process rows
	for i := 0; i < 5; i++ {
		p := storage.ProcessRow{
			Timestamp: now.Add(time.Duration(i) * 10 * time.Second),
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
	metrics, err := db.QueryMetrics(now.Add(-time.Minute))
	if err != nil {
		t.Fatalf("QueryMetrics: %v", err)
	}
	if len(metrics) != 5 {
		t.Errorf("expected 5 metric rows, got %d", len(metrics))
	}

	processes, err := db.QueryProcesses(now.Add(-time.Minute))
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
	if processes[2].Name != "proc_2" {
		t.Errorf("expected name=proc_2, got %v", processes[2].Name)
	}

	// Confirm DB file exists on disk
	if _, err := os.Stat(tmp); os.IsNotExist(err) {
		t.Error("DB file not found on disk")
	}
}
