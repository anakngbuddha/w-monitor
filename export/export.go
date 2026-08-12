// Package export generates CSV and PDF summary reports from sysmon data.
package export

import (
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"Zeus/storage"
)

// WriteCSV writes a CSV summary of metrics with a header block to the provided writer.
func WriteCSV(w io.Writer, db *storage.DB, since time.Time) (int, error) {
	rows, err := db.QueryMetrics(since)
	if err != nil {
		return 0, fmt.Errorf("query metrics: %w", err)
	}

	cw := csv.NewWriter(w)
	defer cw.Flush()

	period := fmt.Sprintf("%s to %s", since.UTC().Format("2006-01-02 15:04"), time.Now().UTC().Format("2006-01-02 15:04"))
	s := ComputeSummary(rows, period)

	// Summary Block
	if err := cw.WriteAll([][]string{
		{"App", "W-Monitor System Report"},
		{"Period", s.Period},
		{"System", fmt.Sprintf("%d vCPU | %.1f GB RAM | %.1f GB Disk", s.CPUCores, s.MemTotalGB, s.DiskTotalGB)},
		{"Samples", fmt.Sprintf("%d", s.RowCount)},
		{"Avg CPU", fmt.Sprintf("%.2f%%", s.AvgCPU)},
		{"Peak CPU", fmt.Sprintf("%.2f%%", s.PeakCPU)},
		{"Avg Memory", fmt.Sprintf("%.2f%%", s.AvgMem)},
		{"Peak Memory", fmt.Sprintf("%.2f%%", s.PeakMem)},
		{"Min Disk Free", fmt.Sprintf("%.2f GB", s.MinDiskFreeGB)},
		{"Total Net Sent", fmt.Sprintf("%.2f MB", float64(s.TotalNetSentBytes)/(1024*1024))},
		{"Total Net Recv", fmt.Sprintf("%.2f MB", float64(s.TotalNetRecvBytes)/(1024*1024))},
		{},
	}); err != nil {
		return 0, err
	}

	// Header
	if err := cw.Write([]string{
		"Date/Time (UTC)", "CPU %", "Memory %", "Disk Free GB", "Net Sent MB", "Net Recv MB", "vCPUs", "Total RAM GB", "Total Disk GB",
	}); err != nil {
		return 0, err
	}

	// Data rows
	for _, r := range rows {
		if err := cw.Write([]string{
			r.Timestamp.UTC().Format("2006-01-02 15:04:05"),
			fmt.Sprintf("%.2f", r.CPUPct),
			fmt.Sprintf("%.2f", r.MemPct),
			fmt.Sprintf("%.2f", r.DiskFreeGB),
			fmt.Sprintf("%.4f", float64(r.NetSentBytes)/(1024*1024)),
			fmt.Sprintf("%.4f", float64(r.NetRecvBytes)/(1024*1024)),
			fmt.Sprintf("%d", r.CPUCores),
			fmt.Sprintf("%.1f", r.MemTotalGB),
			fmt.Sprintf("%.1f", r.DiskTotalGB),
		}); err != nil {
			return 0, err
		}
	}

	return len(rows), nil
}

// CSVReport writes a CSV summary of metrics within the given time range to outPath.
func CSVReport(db *storage.DB, since time.Time, outPath string) (int, error) {
	if err := os.MkdirAll(filepath.Dir(outPath), 0755); err != nil {
		return 0, fmt.Errorf("mkdir: %w", err)
	}

	f, err := os.Create(outPath)
	if err != nil {
		return 0, fmt.Errorf("create file: %w", err)
	}
	defer f.Close()

	return WriteCSV(f, db, since)
}

// Summary holds aggregate statistics for the report period.
type Summary struct {
	Period            string
	RowCount          int
	AvgCPU            float64
	PeakCPU           float64
	AvgMem            float64
	PeakMem           float64
	MinDiskFreeGB     float64
	CPUCores          int
	MemTotalGB        float64
	DiskTotalGB       float64
	TotalNetSentBytes uint64
	TotalNetRecvBytes uint64
}

// ComputeSummary calculates aggregate stats from metric rows.
func ComputeSummary(rows []storage.MetricRow, period string) Summary {
	if len(rows) == 0 {
		return Summary{Period: period}
	}
	s := Summary{
		Period:        period,
		RowCount:      len(rows),
		MinDiskFreeGB: rows[0].DiskFreeGB,
		CPUCores:      rows[0].CPUCores,
		MemTotalGB:    rows[0].MemTotalGB,
		DiskTotalGB:   rows[0].DiskTotalGB,
	}
	var sumCPU, sumMem float64
	for _, r := range rows {
		sumCPU += r.CPUPct
		sumMem += r.MemPct
		if r.CPUPct > s.PeakCPU {
			s.PeakCPU = r.CPUPct
		}
		if r.MemPct > s.PeakMem {
			s.PeakMem = r.MemPct
		}
		if r.DiskFreeGB < s.MinDiskFreeGB {
			s.MinDiskFreeGB = r.DiskFreeGB
		}
	}
	n := float64(len(rows))
	s.AvgCPU = sumCPU / n
	s.AvgMem = sumMem / n

	// Calculate network consumption (handle reset by just using last value if less than first)
	first := rows[0]
	last := rows[len(rows)-1]
	if last.NetSentBytes >= first.NetSentBytes {
		s.TotalNetSentBytes = last.NetSentBytes - first.NetSentBytes
	} else {
		s.TotalNetSentBytes = last.NetSentBytes
	}
	if last.NetRecvBytes >= first.NetRecvBytes {
		s.TotalNetRecvBytes = last.NetRecvBytes - first.NetRecvBytes
	} else {
		s.TotalNetRecvBytes = last.NetRecvBytes
	}

	return s
}

// TextReport writes a human-readable plain-text summary report.
func TextReport(db *storage.DB, since time.Time, outPath string) (Summary, error) {
	rows, err := db.QueryMetrics(since)
	if err != nil {
		return Summary{}, fmt.Errorf("query metrics: %w", err)
	}

	period := fmt.Sprintf("%s to %s", since.UTC().Format("2006-01-02 15:04"), time.Now().UTC().Format("2006-01-02 15:04"))
	s := ComputeSummary(rows, period)

	if err := os.MkdirAll(filepath.Dir(outPath), 0755); err != nil {
		return s, fmt.Errorf("mkdir: %w", err)
	}

	f, err := os.Create(outPath)
	if err != nil {
		return s, fmt.Errorf("create: %w", err)
	}
	defer f.Close()

	fmt.Fprintln(f, "===================================================")
	fmt.Fprintln(f, "  W-MONITOR — System Report")
	fmt.Fprintln(f, "===================================================")
	fmt.Fprintln(f)
	fmt.Fprintf(f, "System   : %d vCPU | %.1f GB RAM | %.1f GB Disk\n", s.CPUCores, s.MemTotalGB, s.DiskTotalGB)
	fmt.Fprintf(f, "Period   : %s\n", s.Period)
	fmt.Fprintf(f, "Samples  : %d\n", s.RowCount)
	fmt.Fprintln(f)
	fmt.Fprintln(f, "CPU")
	fmt.Fprintf(f, "  Average : %.2f%%\n", s.AvgCPU)
	fmt.Fprintf(f, "  Peak    : %.2f%%\n", s.PeakCPU)
	fmt.Fprintln(f)
	fmt.Fprintln(f, "Memory")
	fmt.Fprintf(f, "  Average : %.2f%%\n", s.AvgMem)
	fmt.Fprintf(f, "  Peak    : %.2f%%\n", s.PeakMem)
	fmt.Fprintln(f)
	fmt.Fprintln(f, "Disk")
	fmt.Fprintf(f, "  Minimum Free : %.2f GB\n", s.MinDiskFreeGB)
	fmt.Fprintln(f)
	fmt.Fprintln(f, "Network (Traffic Consumption)")
	fmt.Fprintf(f, "  Sent : %.2f GB\n", float64(s.TotalNetSentBytes)/(1024*1024*1024))
	fmt.Fprintf(f, "  Recv : %.2f GB\n", float64(s.TotalNetRecvBytes)/(1024*1024*1024))
	fmt.Fprintln(f)
	fmt.Fprintln(f, "===================================================")
	fmt.Fprintf(f, "Generated: %s\n", time.Now().UTC().Format(time.RFC3339))

	return s, nil
}
