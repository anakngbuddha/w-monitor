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

// Summary holds aggregate statistics for the report period.
type Summary struct {
	Period              string
	RowCount            int
	AvgCPU              float64
	PeakCPU             float64
	AvgMem              float64
	PeakMem             float64
	MinDiskFreeGB       float64
	CPUCores            int
	MemTotalGB          float64
	DiskTotalGB         float64
	TotalNetSentBytes   uint64
	TotalNetRecvBytes   uint64

	MinDiskIOPS         float64
	AvgDiskIOPS         float64
	PeakDiskIOPS        float64

	MinNetMBps          float64
	AvgNetMBps          float64
	PeakNetMBps         float64

	MinConcurrentUsers  int
	AvgConcurrentUsers  float64
	MaxConcurrentUsers  int

	SuggestedMinCPU     int
	SuggestedMinRAM     float64
	SuggestedMinDiskGB  float64
	SuggestedMinIOPS    int
	SuggestedMinNetMBps float64

	SuggestedRecCPU     int
	SuggestedRecRAM     float64
	SuggestedRecDiskGB  float64
	SuggestedRecIOPS    int
	SuggestedRecNetMBps float64
}

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
		{"Disk IOPS (Min/Avg/Peak)", fmt.Sprintf("%.1f / %.1f / %.1f IOPS", s.MinDiskIOPS, s.AvgDiskIOPS, s.PeakDiskIOPS)},
		{"Net Bandwidth (Min/Avg/Peak)", fmt.Sprintf("%.2f / %.2f / %.2f MB/s", s.MinNetMBps, s.AvgNetMBps, s.PeakNetMBps)},
		{"Concurrent Users (Min/Avg/Max)", fmt.Sprintf("%d / %.1f / %d", s.MinConcurrentUsers, s.AvgConcurrentUsers, s.MaxConcurrentUsers)},
		{},
		{"Suggested Requirements"},
		{"Minimum Specs", fmt.Sprintf("%d vCPU | %.2f GB RAM | %.1f GB Disk | %d IOPS | %.2f MB/s Net BW", s.SuggestedMinCPU, s.SuggestedMinRAM, s.SuggestedMinDiskGB, s.SuggestedMinIOPS, s.SuggestedMinNetMBps)},
		{"Recommended Specs", fmt.Sprintf("%d vCPU | %.2f GB RAM | %.1f GB Disk | %d IOPS | %.2f MB/s Net BW", s.SuggestedRecCPU, s.SuggestedRecRAM, s.SuggestedRecDiskGB, s.SuggestedRecIOPS, s.SuggestedRecNetMBps)},
		{},
	}); err != nil {
		return 0, err
	}

	// Header
	if err := cw.Write([]string{
		"Date/Time (UTC)", "CPU %", "Memory %", "Disk Free GB", "Net Sent MB", "Net Recv MB", "vCPUs", "Total RAM GB", "Total Disk GB", "Disk IOPS", "Net MB/s", "Concurrent Users",
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
			fmt.Sprintf("%.1f", r.DiskIOPS),
			fmt.Sprintf("%.2f", r.NetMBps),
			fmt.Sprintf("%d", r.ConcurrentUsers),
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

// ComputeSummary calculates aggregate stats from metric rows.
func ComputeSummary(rows []storage.MetricRow, period string) Summary {
	if len(rows) == 0 {
		return Summary{Period: period}
	}
	s := Summary{
		Period:             period,
		RowCount:           len(rows),
		MinDiskFreeGB:      rows[0].DiskFreeGB,
		CPUCores:           rows[0].CPUCores,
		MemTotalGB:         rows[0].MemTotalGB,
		DiskTotalGB:        rows[0].DiskTotalGB,
		MinDiskIOPS:        rows[0].DiskIOPS,
		MinNetMBps:         rows[0].NetMBps,
		MinConcurrentUsers: rows[0].ConcurrentUsers,
	}
	var sumCPU, sumMem, sumIOPS, sumNetMBps, sumUsers float64
	var totalNetSent, totalNetRecv uint64

	for i, r := range rows {
		sumCPU += r.CPUPct
		sumMem += r.MemPct
		sumIOPS += r.DiskIOPS
		sumNetMBps += r.NetMBps
		sumUsers += float64(r.ConcurrentUsers)

		if r.CPUPct > s.PeakCPU {
			s.PeakCPU = r.CPUPct
		}
		if r.MemPct > s.PeakMem {
			s.PeakMem = r.MemPct
		}
		if r.DiskFreeGB < s.MinDiskFreeGB {
			s.MinDiskFreeGB = r.DiskFreeGB
		}

		if r.DiskIOPS < s.MinDiskIOPS {
			s.MinDiskIOPS = r.DiskIOPS
		}
		if r.DiskIOPS > s.PeakDiskIOPS {
			s.PeakDiskIOPS = r.DiskIOPS
		}

		if r.NetMBps < s.MinNetMBps {
			s.MinNetMBps = r.NetMBps
		}
		if r.NetMBps > s.PeakNetMBps {
			s.PeakNetMBps = r.NetMBps
		}

		if r.ConcurrentUsers < s.MinConcurrentUsers {
			s.MinConcurrentUsers = r.ConcurrentUsers
		}
		if r.ConcurrentUsers > s.MaxConcurrentUsers {
			s.MaxConcurrentUsers = r.ConcurrentUsers
		}

		if i > 0 {
			prev := rows[i-1]
			if r.NetSentBytes >= prev.NetSentBytes {
				totalNetSent += (r.NetSentBytes - prev.NetSentBytes)
			} else {
				totalNetSent += r.NetSentBytes
			}

			if r.NetRecvBytes >= prev.NetRecvBytes {
				totalNetRecv += (r.NetRecvBytes - prev.NetRecvBytes)
			} else {
				totalNetRecv += r.NetRecvBytes
			}
		}
	}
	n := float64(len(rows))
	s.AvgCPU = sumCPU / n
	s.AvgMem = sumMem / n
	s.AvgDiskIOPS = sumIOPS / n
	s.AvgNetMBps = sumNetMBps / n
	s.AvgConcurrentUsers = sumUsers / n

	s.TotalNetSentBytes = totalNetSent
	s.TotalNetRecvBytes = totalNetRecv

	// Specs computations
	actualPeakCPU := (s.PeakCPU / 100.0) * float64(s.CPUCores)
	actualPeakRAM := (s.PeakMem / 100.0) * s.MemTotalGB
	peakUsedDiskGB := s.DiskTotalGB - s.MinDiskFreeGB
	if peakUsedDiskGB < 0 {
		peakUsedDiskGB = 0
	}

	s.SuggestedMinCPU = int(actualPeakCPU + 0.5)
	if s.SuggestedMinCPU < 1 {
		s.SuggestedMinCPU = 1
	}
	s.SuggestedRecCPU = s.SuggestedMinCPU + 1
	if s.SuggestedRecCPU < 2 {
		s.SuggestedRecCPU = 2
	}

	s.SuggestedMinRAM = actualPeakRAM * 1.2
	if s.SuggestedMinRAM < 0.25 {
		s.SuggestedMinRAM = 0.25
	}
	s.SuggestedRecRAM = actualPeakRAM * 2.0
	if s.SuggestedRecRAM < 0.5 {
		s.SuggestedRecRAM = 0.5
	}

	s.SuggestedMinDiskGB = peakUsedDiskGB * 1.2
	if s.SuggestedMinDiskGB < 10.0 {
		s.SuggestedMinDiskGB = 10.0
	}
	s.SuggestedRecDiskGB = peakUsedDiskGB * 2.0
	if s.SuggestedRecDiskGB < 20.0 {
		s.SuggestedRecDiskGB = 20.0
	}

	s.SuggestedMinIOPS = int(s.PeakDiskIOPS*1.2 + 0.5)
	if s.SuggestedMinIOPS < 100 {
		s.SuggestedMinIOPS = 100
	}
	s.SuggestedRecIOPS = int(s.PeakDiskIOPS*2.0 + 0.5)
	if s.SuggestedRecIOPS < 300 {
		s.SuggestedRecIOPS = 300
	}

	s.SuggestedMinNetMBps = s.PeakNetMBps * 1.2
	if s.SuggestedMinNetMBps < 1.0 {
		s.SuggestedMinNetMBps = 1.0
	}
	s.SuggestedRecNetMBps = s.PeakNetMBps * 2.0
	if s.SuggestedRecNetMBps < 5.0 {
		s.SuggestedRecNetMBps = 5.0
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
	fmt.Fprintln(f, "Disk IOPS")
	fmt.Fprintf(f, "  Minimum : %.1f IOPS\n", s.MinDiskIOPS)
	fmt.Fprintf(f, "  Average : %.1f IOPS\n", s.AvgDiskIOPS)
	fmt.Fprintf(f, "  Peak    : %.1f IOPS\n", s.PeakDiskIOPS)
	fmt.Fprintln(f)
	fmt.Fprintln(f, "Network (Traffic & Bandwidth)")
	fmt.Fprintf(f, "  Sent Total: %.2f GB\n", float64(s.TotalNetSentBytes)/(1024*1024*1024))
	fmt.Fprintf(f, "  Recv Total: %.2f GB\n", float64(s.TotalNetRecvBytes)/(1024*1024*1024))
	fmt.Fprintf(f, "  Bandwidth Rate (Min/Avg/Peak): %.2f / %.2f / %.2f MB/s\n", s.MinNetMBps, s.AvgNetMBps, s.PeakNetMBps)
	fmt.Fprintln(f)
	fmt.Fprintln(f, "Concurrent Users")
	fmt.Fprintf(f, "  Minimum : %d\n", s.MinConcurrentUsers)
	fmt.Fprintf(f, "  Average : %.1f\n", s.AvgConcurrentUsers)
	fmt.Fprintf(f, "  Maximum : %d\n", s.MaxConcurrentUsers)
	fmt.Fprintln(f)
	fmt.Fprintln(f, "Suggested System Requirements")
	fmt.Fprintf(f, "  Minimum Specs     : %d vCPU | %.2f GB RAM | %.1f GB Disk | %d IOPS | %.2f MB/s Net BW\n",
		s.SuggestedMinCPU, s.SuggestedMinRAM, s.SuggestedMinDiskGB, s.SuggestedMinIOPS, s.SuggestedMinNetMBps)
	fmt.Fprintf(f, "  Recommended Specs : %d vCPU | %.2f GB RAM | %.1f GB Disk | %d IOPS | %.2f MB/s Net BW\n",
		s.SuggestedRecCPU, s.SuggestedRecRAM, s.SuggestedRecDiskGB, s.SuggestedRecIOPS, s.SuggestedRecNetMBps)
	fmt.Fprintln(f)
	fmt.Fprintln(f, "===================================================")
	fmt.Fprintf(f, "Generated: %s\n", time.Now().UTC().Format(time.RFC3339))

	return s, nil
}
