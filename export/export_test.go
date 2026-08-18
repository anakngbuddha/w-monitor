package export_test

import (
	"encoding/csv"
	"os"
	"path/filepath"
	"strings"
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
			ServerID:        "srv-01",
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
	t.Logf("CSV path: %s, total CSV rows (including summary): %d", csvPath, len(records))

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

// TestAssessmentHTMLReport verifies the HTML report generator (Phase 12).
func TestAssessmentHTMLReport(t *testing.T) {
	tmp := t.TempDir()
	db, err := storage.Open(filepath.Join(tmp, "report_test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	now := time.Now().UTC()
	for i := 0; i < 20; i++ {
		db.InsertMetric(storage.MetricRow{
			Timestamp:       now.Add(-time.Duration(i) * 30 * time.Minute),
			ServerID:        "srv-01",
			CPUPct:          float64(10 + i*3),
			MemPct:          float64(40 + i),
			DiskFreeGB:      float64(200 - i),
			DiskTotalGB:     250.0,
			MemTotalGB:      16.0,
			CPUCores:        8,
			NetSentBytes:    uint64(1024 * i),
			NetRecvBytes:    uint64(2048 * i),
			DiskIOPS:        float64(100 + i*5),
			NetMBps:         float64(1.0 + float64(i)*0.1),
			ConcurrentUsers: i % 10,
		})
	}

	outPath := filepath.Join(tmp, "assessment.html")
	start := now.Add(-24 * time.Hour)
	end := now

	if err := export.GenerateAssessmentReport(db, start, end, outPath); err != nil {
		t.Fatalf("GenerateAssessmentReport: %v", err)
	}

	// Verify HTML file is created and contains key elements
	content, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read HTML: %v", err)
	}
	html := string(content)

	checks := []string{
		"<!DOCTYPE html>",
		"W-Monitor",
		"Assessment Report",
		"vCPU",
		"Recommended Target Sizing",
		"Minimum",
		"Recommended",
	}
	for _, check := range checks {
		if !strings.Contains(html, check) {
			t.Errorf("HTML missing expected string: %q", check)
		}
	}

	t.Logf("Assessment HTML report: %s, size=%d bytes", outPath, len(content))
}
