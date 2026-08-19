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

func TestMultiServerExportAndAssessment(t *testing.T) {
	tmp := t.TempDir()
	db, err := storage.Open(filepath.Join(tmp, "multiserver_export.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	now := time.Now().UTC()

	// Server 1: web-01 (4 cores, 16GB RAM, 100GB Disk) - Peak CPU 80%, Peak Mem 60%
	for i := 0; i < 5; i++ {
		db.InsertMetric(storage.MetricRow{
			Timestamp:       now.Add(-time.Duration(i) * time.Hour),
			ServerID:        "web-01",
			Hostname:        "web-01.local",
			CPUPct:          float64(40 + i*10), // Peak = 80% (3.2 cores)
			MemPct:          float64(40 + i*5),  // Peak = 60% (9.6 GB)
			DiskFreeGB:      float64(50 - i*2),  // Peak used = 100 - 42 = 58 GB
			DiskTotalGB:     100.0,
			MemTotalGB:      16.0,
			CPUCores:        4,
			NetSentBytes:    uint64(1000 * (i + 1)),
			NetRecvBytes:    uint64(2000 * (i + 1)),
			DiskIOPS:        float64(200 + i*20), // Peak = 280
			NetMBps:         float64(5.0 + float64(i)),
			ConcurrentUsers: 10 + i,
		})
	}

	// Server 2: db-01 (8 cores, 64GB RAM, 500GB Disk) - Peak CPU 50%, Peak Mem 80%
	for i := 0; i < 5; i++ {
		db.InsertMetric(storage.MetricRow{
			Timestamp:       now.Add(-time.Duration(i) * time.Hour),
			ServerID:        "db-01",
			Hostname:        "db-01.local",
			CPUPct:          float64(30 + i*5),  // Peak = 50% (4.0 cores)
			MemPct:          float64(60 + i*5),  // Peak = 80% (51.2 GB)
			DiskFreeGB:      float64(250 - i*5), // Peak used = 500 - 230 = 270 GB
			DiskTotalGB:     500.0,
			MemTotalGB:      64.0,
			CPUCores:        8,
			NetSentBytes:    uint64(5000 * (i + 1)),
			NetRecvBytes:    uint64(10000 * (i + 1)),
			DiskIOPS:        float64(1000 + i*100), // Peak = 1400
			NetMBps:         float64(10.0 + float64(i)*2),
			ConcurrentUsers: 5,
		})
	}

	// 1. Test ComputePerServerSummaries directly
	rows, err := db.QueryMetrics(now.Add(-24*time.Hour), "")
	if err != nil {
		t.Fatalf("QueryMetrics: %v", err)
	}
	if len(rows) != 10 {
		t.Fatalf("expected 10 rows, got %d", len(rows))
	}

	serverSummaries := export.ComputePerServerSummaries(rows, "24h")
	if len(serverSummaries) != 2 {
		t.Fatalf("expected 2 server summaries, got %d", len(serverSummaries))
	}

	// 2. Test AggregateFleetSummary
	fleet := export.AggregateFleetSummary(serverSummaries, "24h")
	if fleet.FleetServerCount != 2 {
		t.Errorf("expected FleetServerCount=2, got %d", fleet.FleetServerCount)
	}
	if fleet.CPUCores != 12 { // 4 + 8
		t.Errorf("expected fleet CPUCores=12, got %d", fleet.CPUCores)
	}
	if fleet.MemTotalGB != 80.0 { // 16 + 64
		t.Errorf("expected fleet MemTotalGB=80.0, got %f", fleet.MemTotalGB)
	}
	if fleet.DiskTotalGB != 600.0 { // 100 + 500
		t.Errorf("expected fleet DiskTotalGB=600.0, got %f", fleet.DiskTotalGB)
	}

	// Peak absolute compute: 3.2 cores (web) + 4.0 cores (db) = 7.2 cores
	// Min vCPU = int(7.2 * 1.2 + 0.5) = 9
	// Rec vCPU = int(7.2 * 2.0 + 0.5) = 14
	if fleet.SuggestedMinCPU < 8 {
		t.Errorf("expected SuggestedMinCPU >= 8, got %d", fleet.SuggestedMinCPU)
	}
	if fleet.SuggestedRecCPU < 14 {
		t.Errorf("expected SuggestedRecCPU >= 14, got %d", fleet.SuggestedRecCPU)
	}

	// Peak absolute RAM: 9.6 GB (web) + 51.2 GB (db) = 60.8 GB
	// Min RAM = 60.8 * 1.2 = ~72.96 GB
	// Rec RAM = 60.8 * 2.0 = ~121.6 GB
	if fleet.SuggestedMinRAM < 70.0 {
		t.Errorf("expected SuggestedMinRAM >= 70.0, got %f", fleet.SuggestedMinRAM)
	}
	if fleet.SuggestedRecRAM < 120.0 {
		t.Errorf("expected SuggestedRecRAM >= 120.0, got %f", fleet.SuggestedRecRAM)
	}

	// 3. Test GenerateAssessmentReport with multi-server data
	htmlPath := filepath.Join(tmp, "multiserver_assessment.html")
	if err := export.GenerateAssessmentReport(db, now.Add(-24*time.Hour), now, htmlPath); err != nil {
		t.Fatalf("GenerateAssessmentReport: %v", err)
	}

	htmlContent, err := os.ReadFile(htmlPath)
	if err != nil {
		t.Fatalf("read multiserver HTML: %v", err)
	}
	htmlStr := string(htmlContent)

	expectedStrings := []string{
		"Fleet Baseline (2 Servers)",
		"Per-Server Breakdown",
		"web-01",
		"db-01",
		"Recommended Target Sizing",
	}
	for _, exp := range expectedStrings {
		if !strings.Contains(htmlStr, exp) {
			t.Errorf("HTML missing expected string: %q", exp)
		}
	}

	// 4. Test TextReport with multi-server data
	txtPath := filepath.Join(tmp, "multiserver_report.txt")
	s, err := export.TextReport(db, now.Add(-24*time.Hour), txtPath)
	if err != nil {
		t.Fatalf("TextReport: %v", err)
	}
	if s.FleetServerCount != 2 {
		t.Errorf("expected text report FleetServerCount=2, got %d", s.FleetServerCount)
	}

	txtContent, err := os.ReadFile(txtPath)
	if err != nil {
		t.Fatalf("read multiserver txt: %v", err)
	}
	if !strings.Contains(string(txtContent), "2 servers") {
		t.Errorf("text report missing '2 servers' indicator: %s", string(txtContent))
	}
}

