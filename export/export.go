// Package export generates CSV and text summary reports from sysmon data.
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
// When FleetServerCount > 1 the CPU/Mem percentages are expressed relative
// to the total fleet capacity, and all hardware fields (CPUCores, MemTotalGB,
// DiskTotalGB) are fleet totals. Suggested specs reflect the combined
// peak load across all servers.
type Summary struct {
	Period           string
	RowCount         int
	FleetServerCount int // number of distinct servers contributing data (≥1)

	AvgCPU        float64
	PeakCPU       float64
	AvgMem        float64
	PeakMem       float64
	MinDiskFreeGB float64
	CPUCores      int
	MemTotalGB    float64
	DiskTotalGB   float64
	TotalNetSentBytes uint64
	TotalNetRecvBytes uint64

	MinDiskIOPS  float64
	AvgDiskIOPS  float64
	PeakDiskIOPS float64

	MinNetMBps  float64
	AvgNetMBps  float64
	PeakNetMBps float64

	MinConcurrentUsers int
	AvgConcurrentUsers float64
	MaxConcurrentUsers int

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

// ServerSummary wraps per-server aggregate statistics.
type ServerSummary struct {
	ServerID string
	Hostname string
	Summary
}

// computeServerSummary calculates aggregate stats for a slice of rows from a single server.
// All rows must belong to the same server (same ServerID), ordered oldest-first.
func computeServerSummary(rows []storage.MetricRow, period string) Summary {
	if len(rows) == 0 {
		return Summary{Period: period, FleetServerCount: 1}
	}
	s := Summary{
		Period:             period,
		RowCount:           len(rows),
		FleetServerCount:   1,
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

		// Net traffic delta: only diff within the same server (counters are cumulative per-server)
		if i > 0 {
			prev := rows[i-1]
			if r.NetSentBytes >= prev.NetSentBytes {
				totalNetSent += r.NetSentBytes - prev.NetSentBytes
			} else {
				totalNetSent += r.NetSentBytes // counter reset
			}
			if r.NetRecvBytes >= prev.NetRecvBytes {
				totalNetRecv += r.NetRecvBytes - prev.NetRecvBytes
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

	// Compute spec suggestions using absolute peak values for this server
	peakCPUCores := (s.PeakCPU / 100.0) * float64(s.CPUCores)
	peakRAMGB := (s.PeakMem / 100.0) * s.MemTotalGB
	peakUsedDiskGB := s.DiskTotalGB - s.MinDiskFreeGB
	if peakUsedDiskGB < 0 {
		peakUsedDiskGB = 0
	}
	computeSpecSuggestions(&s, peakCPUCores, peakRAMGB, peakUsedDiskGB)
	return s
}

// computeSpecSuggestions fills in SuggestedMin* and SuggestedRec* using absolute
// peak resource values (not percentages). This is called for both single-server
// and fleet aggregation so that the same math applies in both cases.
//
//   - Minimum = peak observed + 20% safety margin
//   - Recommended = peak observed × 2.0 (growth and burst headroom)
func computeSpecSuggestions(s *Summary, peakCPUCores, peakRAMGB, peakUsedDiskGB float64) {
	// vCPU
	s.SuggestedMinCPU = int(peakCPUCores*1.2 + 0.5)
	if s.SuggestedMinCPU < 1 {
		s.SuggestedMinCPU = 1
	}
	s.SuggestedRecCPU = int(peakCPUCores*2.0 + 0.5)
	if s.SuggestedRecCPU < 2 {
		s.SuggestedRecCPU = 2
	}

	// RAM
	s.SuggestedMinRAM = peakRAMGB * 1.2
	if s.SuggestedMinRAM < 0.25 {
		s.SuggestedMinRAM = 0.25
	}
	s.SuggestedRecRAM = peakRAMGB * 2.0
	if s.SuggestedRecRAM < 0.5 {
		s.SuggestedRecRAM = 0.5
	}

	// Disk
	s.SuggestedMinDiskGB = peakUsedDiskGB * 1.2
	if s.SuggestedMinDiskGB < 10.0 {
		s.SuggestedMinDiskGB = 10.0
	}
	s.SuggestedRecDiskGB = peakUsedDiskGB * 2.0
	if s.SuggestedRecDiskGB < 20.0 {
		s.SuggestedRecDiskGB = 20.0
	}

	// IOPS (uses the pre-populated s.PeakDiskIOPS which is correct for both single/fleet)
	s.SuggestedMinIOPS = int(s.PeakDiskIOPS*1.2 + 0.5)
	if s.SuggestedMinIOPS < 100 {
		s.SuggestedMinIOPS = 100
	}
	s.SuggestedRecIOPS = int(s.PeakDiskIOPS*2.0 + 0.5)
	if s.SuggestedRecIOPS < 300 {
		s.SuggestedRecIOPS = 300
	}

	// Network bandwidth
	s.SuggestedMinNetMBps = s.PeakNetMBps * 1.2
	if s.SuggestedMinNetMBps < 1.0 {
		s.SuggestedMinNetMBps = 1.0
	}
	s.SuggestedRecNetMBps = s.PeakNetMBps * 2.0
	if s.SuggestedRecNetMBps < 5.0 {
		s.SuggestedRecNetMBps = 5.0
	}
}

// ComputePerServerSummaries groups rows by ServerID and computes an independent
// Summary for each server. Rows with an empty ServerID fall back to Hostname
// as the grouping key. The returned slice preserves insertion order (first-seen server first).
func ComputePerServerSummaries(rows []storage.MetricRow, period string) []ServerSummary {
	byServer := make(map[string][]storage.MetricRow)
	order := make([]string, 0)
	for _, r := range rows {
		key := r.ServerID
		if key == "" {
			key = r.Hostname
		}
		if _, seen := byServer[key]; !seen {
			order = append(order, key)
		}
		byServer[key] = append(byServer[key], r)
	}
	out := make([]ServerSummary, 0, len(order))
	for _, key := range order {
		srows := byServer[key]
		s := computeServerSummary(srows, period)
		sid := key
		hn := key
		if len(srows) > 0 {
			if srows[0].ServerID != "" {
				sid = srows[0].ServerID
			}
			if srows[0].Hostname != "" {
				hn = srows[0].Hostname
			}
		}
		out = append(out, ServerSummary{ServerID: sid, Hostname: hn, Summary: s})
	}
	return out
}

// AggregateFleetSummary combines per-server summaries into a single fleet-level Summary.
//
// Key rules:
//   - Hardware totals (CPUCores, MemTotalGB, DiskTotalGB) are summed across servers.
//   - CPU/Mem % are re-expressed relative to the total fleet capacity so they
//     remain meaningful (e.g. "fleet used 45% of total vCPUs on average").
//   - IOPS, Net BW, and concurrent users are summed (servers run concurrently).
//   - Suggested specs are derived from the sum of each server's peak absolute resource
//     usage — NOT from the blended percentage.
func AggregateFleetSummary(servers []ServerSummary, period string) Summary {
	if len(servers) == 0 {
		return Summary{Period: period}
	}
	if len(servers) == 1 {
		s := servers[0].Summary
		s.FleetServerCount = 1
		return s
	}

	fleet := Summary{
		Period:           period,
		FleetServerCount: len(servers),
	}

	var (
		// Absolute resource accumulators for correct spec math
		fleetPeakCPUCores   float64 // Σ (peakCPU%_i / 100 × cores_i)
		fleetAvgCPUCores    float64 // Σ (avgCPU%_i  / 100 × cores_i)
		fleetPeakUsedRAMGB  float64 // Σ (peakMem%_i / 100 × ramGB_i)
		fleetAvgUsedRAMGB   float64 // Σ (avgMem%_i  / 100 × ramGB_i)
		fleetPeakUsedDiskGB float64 // Σ (diskTotal_i - minFree_i)
	)

	for _, sv := range servers {
		s := sv.Summary
		fleet.RowCount    += s.RowCount
		fleet.CPUCores    += s.CPUCores
		fleet.MemTotalGB  += s.MemTotalGB
		fleet.DiskTotalGB += s.DiskTotalGB

		// Absolute peak & average resource usage per server
		fleetPeakCPUCores   += (s.PeakCPU / 100.0) * float64(s.CPUCores)
		fleetAvgCPUCores    += (s.AvgCPU / 100.0) * float64(s.CPUCores)
		fleetPeakUsedRAMGB  += (s.PeakMem / 100.0) * s.MemTotalGB
		fleetAvgUsedRAMGB   += (s.AvgMem / 100.0) * s.MemTotalGB
		peakUsed := s.DiskTotalGB - s.MinDiskFreeGB
		if peakUsed < 0 {
			peakUsed = 0
		}
		fleetPeakUsedDiskGB += peakUsed

		// Free disk: sum of per-server minimums (most conservative estimate of total free space)
		fleet.MinDiskFreeGB += s.MinDiskFreeGB

		// IOPS and Net BW: servers run concurrently, so sum
		fleet.MinDiskIOPS  += s.MinDiskIOPS
		fleet.AvgDiskIOPS  += s.AvgDiskIOPS
		fleet.PeakDiskIOPS += s.PeakDiskIOPS

		fleet.MinNetMBps  += s.MinNetMBps
		fleet.AvgNetMBps  += s.AvgNetMBps
		fleet.PeakNetMBps += s.PeakNetMBps

		// Net traffic totals: additive
		fleet.TotalNetSentBytes += s.TotalNetSentBytes
		fleet.TotalNetRecvBytes += s.TotalNetRecvBytes

		// Concurrent users: additive (different servers serve different sessions)
		fleet.MinConcurrentUsers += s.MinConcurrentUsers
		fleet.MaxConcurrentUsers += s.MaxConcurrentUsers
		fleet.AvgConcurrentUsers += s.AvgConcurrentUsers
	}

	// Re-express CPU/Mem as % of total fleet capacity for display purposes
	if fleet.CPUCores > 0 {
		fleet.PeakCPU = fleetPeakCPUCores / float64(fleet.CPUCores) * 100.0
		fleet.AvgCPU  = fleetAvgCPUCores / float64(fleet.CPUCores) * 100.0
	}
	if fleet.MemTotalGB > 0 {
		fleet.PeakMem = fleetPeakUsedRAMGB / fleet.MemTotalGB * 100.0
		fleet.AvgMem  = fleetAvgUsedRAMGB / fleet.MemTotalGB * 100.0
	}

	// Spec suggestions use the summed absolute peaks — correct for fleet sizing
	computeSpecSuggestions(&fleet, fleetPeakCPUCores, fleetPeakUsedRAMGB, fleetPeakUsedDiskGB)
	return fleet
}

// ComputeSummary calculates fleet-aware aggregate stats from all metric rows.
//
// Rows are automatically grouped by ServerID before any statistics are computed,
// so hardware baselines and utilisation percentages are always calculated per-server
// first, then combined correctly into fleet totals.
//
// For single-server deployments the result is identical to the previous behaviour.
func ComputeSummary(rows []storage.MetricRow, period string) Summary {
	perServer := ComputePerServerSummaries(rows, period)
	return AggregateFleetSummary(perServer, period)
}

// WriteCSV writes a CSV summary of metrics with a header block to the provided writer.
// Pass tenantID="" to include all tenants (admin/CLI use); non-empty filters to one tenant.
func WriteCSV(w io.Writer, db storage.Store, since time.Time, tenantID string) (int, error) {
	rows, err := db.QueryMetrics(since, tenantID)
	if err != nil {
		return 0, fmt.Errorf("query metrics: %w", err)
	}

	cw := csv.NewWriter(w)
	defer cw.Flush()

	period := fmt.Sprintf("%s to %s", since.UTC().Format("2006-01-02 15:04"), time.Now().UTC().Format("2006-01-02 15:04"))
	s := ComputeSummary(rows, period)

	fleetLabel := "System"
	if s.FleetServerCount > 1 {
		fleetLabel = fmt.Sprintf("Fleet (%d servers)", s.FleetServerCount)
	}

	// Summary Block
	if err := cw.WriteAll([][]string{
		{"App", "W-Monitor System Report"},
		{"Period", s.Period},
		{fleetLabel, fmt.Sprintf("%d vCPU | %.1f GB RAM | %.1f GB Disk", s.CPUCores, s.MemTotalGB, s.DiskTotalGB)},
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
		"Date/Time (UTC)", "Server ID", "Hostname", "CPU %", "Memory %", "Disk Free GB", "Net Sent MB", "Net Recv MB", "vCPUs", "Total RAM GB", "Total Disk GB", "Disk IOPS", "Net MB/s", "Concurrent Users",
	}); err != nil {
		return 0, err
	}

	// Data rows
	for _, r := range rows {
		if err := cw.Write([]string{
			r.Timestamp.UTC().Format("2006-01-02 15:04:05"),
			r.ServerID,
			r.Hostname,
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
func CSVReport(db storage.Store, since time.Time, outPath string) (int, error) {
	if err := os.MkdirAll(filepath.Dir(outPath), 0755); err != nil {
		return 0, fmt.Errorf("mkdir: %w", err)
	}

	f, err := os.Create(outPath)
	if err != nil {
		return 0, fmt.Errorf("create file: %w", err)
	}
	defer f.Close()

	return WriteCSV(f, db, since, "")
}

// TextReport writes a human-readable plain-text summary report.
func TextReport(db storage.Store, since time.Time, outPath string) (Summary, error) {
	rows, err := db.QueryMetrics(since, "")
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

	fleetHeader := fmt.Sprintf("%d vCPU | %.1f GB RAM | %.1f GB Disk", s.CPUCores, s.MemTotalGB, s.DiskTotalGB)
	if s.FleetServerCount > 1 {
		fleetHeader = fmt.Sprintf("%d servers | %d vCPU total | %.1f GB RAM total | %.1f GB Disk total",
			s.FleetServerCount, s.CPUCores, s.MemTotalGB, s.DiskTotalGB)
	}

	fmt.Fprintln(f, "===================================================")
	fmt.Fprintln(f, "  W-MONITOR — System Report")
	fmt.Fprintln(f, "===================================================")
	fmt.Fprintln(f)
	fmt.Fprintf(f, "Fleet    : %s\n", fleetHeader)
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
