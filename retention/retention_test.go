package retention_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"Zeus/retention"
	"Zeus/storage"
)

func TestRetentionPreservesTenantServerRows(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "test_retention.db")
	db, err := storage.Open(tmp)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	now := time.Now().UTC()
	hourBucket := now.Add(-25 * time.Hour).Truncate(time.Hour)

	if err := db.InsertMetric(storage.MetricRow{
		Timestamp: hourBucket.Add(1 * time.Minute),
		TenantID:  "tenant-a", ServerID: "srv-same", Hostname: "host-a",
		CPUPct: 10, MemPct: 40, DiskFreeGB: 100, CPUCores: 4, MemTotalGB: 16, DiskTotalGB: 200,
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertMetric(storage.MetricRow{
		Timestamp: hourBucket.Add(2 * time.Minute),
		TenantID:  "tenant-b", ServerID: "srv-same", Hostname: "host-b",
		CPUPct: 90, MemPct: 80, DiskFreeGB: 10, CPUCores: 8, MemTotalGB: 32, DiskTotalGB: 400,
	}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if err := db.InsertMetric(storage.MetricRow{
			Timestamp: now.Add(-31 * 24 * time.Hour).Add(time.Duration(i) * time.Second),
			TenantID:  "tenant-a", ServerID: "srv-old", CPUPct: 1, MemPct: 1, DiskFreeGB: 1,
		}); err != nil {
			t.Fatal(err)
		}
	}

	before, err := db.CountMetrics()
	if err != nil {
		t.Fatal(err)
	}
	if before != 7 {
		t.Fatalf("before count = %d, want 7", before)
	}

	job := retention.New(db.Conn())
	job.SetDataPath(tmp)
	if err := job.Run(); err != nil {
		t.Fatalf("Run: %v", err)
	}

	after, err := db.CountMetrics()
	if err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("retention cycle changed row count %d -> %d; tenant/server rows must survive (V07 containment)", before, after)
	}

	rowsA, err := db.QueryMetrics(time.Unix(0, 0), "tenant-a")
	if err != nil {
		t.Fatal(err)
	}
	rowsB, err := db.QueryMetrics(time.Unix(0, 0), "tenant-b")
	if err != nil {
		t.Fatal(err)
	}
	if len(rowsA) != 6 || len(rowsB) != 1 {
		t.Fatalf("tenant isolation lost: A=%d B=%d", len(rowsA), len(rowsB))
	}
	for _, r := range rowsA {
		if r.TenantID != "tenant-a" || r.ServerID == "" {
			t.Errorf("tenant-a row mutated: %+v", r)
		}
	}
	if rowsB[0].CPUPct != 90 || rowsB[0].CPUCores != 8 {
		t.Errorf("tenant-b row mutated: %+v", rowsB[0])
	}

	st := job.Status()
	if st.DownsamplingEnabled || st.PurgeEnabled {
		t.Errorf("containment flags: %+v", st)
	}
	if st.Reason == "" {
		t.Error("missing retention-disabled reason")
	}
}

func TestDiskBudgetExhaustionIsExplicit(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "budget.db")
	db, err := storage.Open(tmp)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.InsertMetric(storage.MetricRow{
		Timestamp: time.Now(), TenantID: "t", ServerID: "s", CPUPct: 1, MemPct: 1, DiskFreeGB: 1,
	}); err != nil {
		t.Fatal(err)
	}

	job := retention.New(db.Conn())
	job.SetDataPath(tmp)
	job.SetDiskBudget(1) // 1 byte; the SQLite file is larger
	err = job.Run()
	if !errors.Is(err, retention.ErrDiskPressure) {
		t.Fatalf("Run error = %v, want ErrDiskPressure", err)
	}
	st := job.Status()
	if !st.DiskPressure {
		t.Fatal("Status.DiskPressure = false")
	}
	if st.DiskUsedBytes <= 1 {
		t.Fatalf("DiskUsedBytes = %d, want file size > 1", st.DiskUsedBytes)
	}

	n, err := db.CountMetrics()
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("disk pressure deleted rows: count=%d", n)
	}
	_ = os.Remove(tmp)
}
