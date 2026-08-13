package export_test

import (
	"encoding/csv"
	"os"
	"path/filepath"
	"testing"
	"time"

	"Zeus/export"
	"Zeus/storage"
)

func TestExportCSVAndText(t *testing.T) {
	tmp := t.TempDir()
	db, err := storage.Open(filepath.Join(tmp, "test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	// Insert 10 test rows over the last 24 hours
	now := time.Now().UTC()
	for i := 0; i < 10; i++ {
		db.InsertMetric(storage.MetricRow{
			Timestamp:       now.Add(-time.Duration(i) * time.Hour),
			CPUPct:          float64(10 + i*5),
			MemPct:          float64(40 + i*2),
			DiskFreeGB:      float64(100 - i),
			NetSentBytes:    uint64(1000 * i),
			NetRecvBytes:    uint64(2000 * i),
			DiskIOPS:        float64(100 + i*10),
			NetMBps:         float64(2.0 + float64(i)*0.2),
			ConcurrentUsers: 5 + i,
		})
	}

	csvPath := filepath.Join(tmp, "report.csv")
	txtPath := filepath.Join(tmp, "report.txt")

	// Test CSV export
	n, err := export.CSVReport(db, now.Add(-48*time.Hour), csvPath)
	if err != nil {
		t.Fatalf("CSVReport: %v", err)
	}
	if n != 10 {
		t.Errorf("expected 10 rows in CSV, got %d", n)
	}

	// Verify CSV file exists and has correct column count
	f, err := os.Open(csvPath)
	if err != nil {
		t.Fatalf("open csv: %v", err)
	}
	defer f.Close()
	reader := csv.NewReader(f)
	reader.FieldsPerRecord = -1
	records, err := reader.ReadAll()
	if err != nil {
		t.Fatalf("read csv: %v", err)
	}
	// summary block (17) + header (1) + 10 data rows = 28
	if len(records) != 28 {
		t.Errorf("expected 28 CSV rows (summary+header+10), got %d", len(records))
	}
	t.Logf("CSV path: %s, rows: %d", csvPath, len(records)-1)

	// Test text report
	s, err := export.TextReport(db, now.Add(-48*time.Hour), txtPath)
	if err != nil {
		t.Fatalf("TextReport: %v", err)
	}
	if s.RowCount != 10 {
		t.Errorf("expected RowCount=10, got %d", s.RowCount)
	}
	if s.PeakCPU < 10 {
		t.Errorf("PeakCPU too low: %v", s.PeakCPU)
	}
	if s.SuggestedMinIOPS <= 0 {
		t.Errorf("SuggestedMinIOPS should be > 0: %v", s.SuggestedMinIOPS)
	}
	if s.SuggestedMinDiskGB <= 0 {
		t.Errorf("SuggestedMinDiskGB should be > 0: %v", s.SuggestedMinDiskGB)
	}
	if s.SuggestedMinNetMBps <= 0 {
		t.Errorf("SuggestedMinNetMBps should be > 0: %v", s.SuggestedMinNetMBps)
	}
	t.Logf("Text report: %s, samples=%d, avg_cpu=%.2f%%, peak_cpu=%.2f%%", txtPath, s.RowCount, s.AvgCPU, s.PeakCPU)

	// Verify text file is non-empty
	info, _ := os.Stat(txtPath)
	if info.Size() == 0 {
		t.Error("text report file is empty")
	}
}
