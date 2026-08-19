// Package export — assessment report generator (Phase 12).
// Produces a self-contained HTML report that can be printed to PDF from any browser.
package export

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"Zeus/storage"
)

// AssessmentReport holds the full computed assessment data.
type AssessmentReport struct {
	Summary
	Start       time.Time
	End         time.Time
	SampleCount int
	Servers     []ServerSummary // per-server breakdown (len ≥ 1)
}

// GenerateAssessmentReport aggregates data over the period and writes an HTML report to outPath.
// This is the primary deliverable for a cloud migration/sizing proposal.
func GenerateAssessmentReport(db storage.Store, start, end time.Time, outPath string) error {
	rows, err := db.QueryMetrics(start, "")
	if err != nil {
		return fmt.Errorf("query metrics: %w", err)
	}

	// Filter to end time
	var filtered []storage.MetricRow
	for _, r := range rows {
		if !r.Timestamp.After(end) {
			filtered = append(filtered, r)
		}
	}

	period := fmt.Sprintf("%s to %s", start.UTC().Format("2006-01-02 15:04"), end.UTC().Format("2006-01-02 15:04"))

	// Compute per-server summaries first, then aggregate into fleet totals
	servers := ComputePerServerSummaries(filtered, period)
	fleet := AggregateFleetSummary(servers, period)

	report := AssessmentReport{
		Summary:     fleet,
		Start:       start,
		End:         end,
		SampleCount: len(filtered),
		Servers:     servers,
	}

	if err := os.MkdirAll(filepath.Dir(outPath), 0755); err != nil {
		return fmt.Errorf("mkdir: %w", err)
	}

	f, err := os.Create(outPath)
	if err != nil {
		return fmt.Errorf("create: %w", err)
	}
	defer f.Close()

	return writeHTMLReport(f, report)
}

func writeHTMLReport(f *os.File, r AssessmentReport) error {
	// Decide labels based on fleet size
	baselineTitle := "System Baseline"
	baselineFleetLine := fmt.Sprintf(`<div class="metric"><span class="label">vCPUs</span><span class="value">%d</span></div>
    <div class="metric"><span class="label">Total RAM</span><span class="value">%.1f GB</span></div>
    <div class="metric"><span class="label">Total Disk</span><span class="value">%.1f GB</span></div>`,
		r.CPUCores, r.MemTotalGB, r.DiskTotalGB)

	if r.FleetServerCount > 1 {
		baselineTitle = fmt.Sprintf("Fleet Baseline (%d Servers)", r.FleetServerCount)
		baselineFleetLine = fmt.Sprintf(`<div class="metric"><span class="label">Servers</span><span class="value">%d</span></div>
    <div class="metric"><span class="label">Total vCPUs</span><span class="value">%d</span></div>
    <div class="metric"><span class="label">Total RAM</span><span class="value">%.1f GB</span></div>
    <div class="metric"><span class="label">Total Disk</span><span class="value">%.1f GB</span></div>`,
			r.FleetServerCount, r.CPUCores, r.MemTotalGB, r.DiskTotalGB)
	}

	cpuLabel := "CPU Usage"
	memLabel := "Memory Usage"
	if r.FleetServerCount > 1 {
		cpuLabel = "CPU Usage (Fleet %)"
		memLabel = "Memory Usage (Fleet %)"
	}

	// Build per-server breakdown table (shown only when monitoring multiple servers)
	perServerSection := ""
	if r.FleetServerCount > 1 {
		var sb strings.Builder
		sb.WriteString(`<div class="section">
  <h2>🖥️ Per-Server Breakdown</h2>
  <table class="specs-table">
    <thead>
      <tr>
        <th>Server ID</th>
        <th>Hostname</th>
        <th>vCPU</th>
        <th>RAM (GB)</th>
        <th>Disk (GB)</th>
        <th>Avg CPU%</th>
        <th>Peak CPU%</th>
        <th>Avg Mem%</th>
        <th>Peak Mem%</th>
        <th>Peak IOPS</th>
        <th>Peak BW (MB/s)</th>
        <th>Min Specs</th>
        <th>Rec Specs</th>
      </tr>
    </thead>
    <tbody>
`)
		for _, sv := range r.Servers {
			s := sv.Summary
			sb.WriteString(fmt.Sprintf(`      <tr>
        <td>%s</td>
        <td>%s</td>
        <td>%d</td>
        <td>%.1f</td>
        <td>%.1f</td>
        <td>%.1f%%</td>
        <td>%.1f%%</td>
        <td>%.1f%%</td>
        <td>%.1f%%</td>
        <td>%.0f</td>
        <td>%.2f</td>
        <td>%d vCPU / %.1f GB RAM</td>
        <td>%d vCPU / %.1f GB RAM</td>
      </tr>
`,
				sv.ServerID, sv.Hostname,
				s.CPUCores, s.MemTotalGB, s.DiskTotalGB,
				s.AvgCPU, s.PeakCPU,
				s.AvgMem, s.PeakMem,
				s.PeakDiskIOPS, s.PeakNetMBps,
				s.SuggestedMinCPU, s.SuggestedMinRAM,
				s.SuggestedRecCPU, s.SuggestedRecRAM,
			))
		}
		sb.WriteString(`    </tbody>
  </table>
</div>
`)
		perServerSection = sb.String()
	}

	html := fmt.Sprintf(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>W-Monitor Assessment Report — %s</title>
<style>
  :root {
    --primary: #1e40af;
    --accent:  #3b82f6;
    --bg:      #f8fafc;
    --card:    #ffffff;
    --border:  #e2e8f0;
    --text:    #1e293b;
    --muted:   #64748b;
    --green:   #16a34a;
    --amber:   #d97706;
    --red:     #dc2626;
  }
  * { box-sizing: border-box; margin: 0; padding: 0; }
  body { font-family: 'Segoe UI', system-ui, sans-serif; background: var(--bg); color: var(--text); padding: 2rem; }
  .header { background: linear-gradient(135deg, var(--primary), var(--accent)); color: #fff; border-radius: 12px; padding: 2rem; margin-bottom: 2rem; }
  .header h1 { font-size: 1.75rem; font-weight: 700; }
  .header p  { opacity: 0.85; margin-top: 0.5rem; }
  .grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(280px, 1fr)); gap: 1rem; margin-bottom: 2rem; }
  .card { background: var(--card); border: 1px solid var(--border); border-radius: 10px; padding: 1.5rem; }
  .card h2 { font-size: 0.75rem; font-weight: 600; color: var(--muted); text-transform: uppercase; letter-spacing: .05em; margin-bottom: 1rem; }
  .metric { display: flex; justify-content: space-between; align-items: center; padding: 0.35rem 0; border-bottom: 1px solid var(--border); }
  .metric:last-child { border-bottom: none; }
  .metric .label { color: var(--muted); font-size: 0.875rem; }
  .metric .value { font-weight: 600; }
  .specs-table { width: 100%%; border-collapse: collapse; margin-top: 1rem; overflow-x: auto; }
  .specs-table th { background: var(--primary); color: #fff; padding: 0.75rem 1rem; text-align: left; font-size: 0.8rem; white-space: nowrap; }
  .specs-table td { padding: 0.65rem 1rem; border-bottom: 1px solid var(--border); font-size: 0.875rem; white-space: nowrap; }
  .specs-table tr:nth-child(even) td { background: var(--bg); }
  .section { background: var(--card); border: 1px solid var(--border); border-radius: 10px; padding: 1.5rem; margin-bottom: 1.5rem; overflow-x: auto; }
  .section h2 { font-size: 1rem; font-weight: 700; margin-bottom: 1rem; color: var(--primary); }
  .footer { text-align: center; color: var(--muted); font-size: 0.8rem; margin-top: 2rem; }
  @media print { body { padding: 0; } .header { border-radius: 0; } }
</style>
</head>
<body>
<div class="header">
  <h1>📊 W-Monitor — Pre-Migration Assessment Report</h1>
  <p>Period: %s &nbsp;|&nbsp; Samples: %d &nbsp;|&nbsp; Generated: %s</p>
</div>

<div class="grid">
  <div class="card">
    <h2>%s</h2>
    %s
  </div>
  <div class="card">
    <h2>%s</h2>
    <div class="metric"><span class="label">Average</span><span class="value">%.2f%%</span></div>
    <div class="metric"><span class="label">Peak</span><span class="value">%.2f%%</span></div>
  </div>
  <div class="card">
    <h2>%s</h2>
    <div class="metric"><span class="label">Average</span><span class="value">%.2f%%</span></div>
    <div class="metric"><span class="label">Peak</span><span class="value">%.2f%%</span></div>
    <div class="metric"><span class="label">Min Free Disk</span><span class="value">%.2f GB</span></div>
  </div>
  <div class="card">
    <h2>Disk IOPS</h2>
    <div class="metric"><span class="label">Minimum</span><span class="value">%.1f</span></div>
    <div class="metric"><span class="label">Average</span><span class="value">%.1f</span></div>
    <div class="metric"><span class="label">Peak</span><span class="value">%.1f</span></div>
  </div>
  <div class="card">
    <h2>Network Bandwidth</h2>
    <div class="metric"><span class="label">Total Sent</span><span class="value">%.2f GB</span></div>
    <div class="metric"><span class="label">Total Recv</span><span class="value">%.2f GB</span></div>
    <div class="metric"><span class="label">Peak Rate</span><span class="value">%.2f MB/s</span></div>
  </div>
  <div class="card">
    <h2>Concurrent Users</h2>
    <div class="metric"><span class="label">Minimum</span><span class="value">%d</span></div>
    <div class="metric"><span class="label">Average</span><span class="value">%.1f</span></div>
    <div class="metric"><span class="label">Peak</span><span class="value">%d</span></div>
  </div>
</div>

%s
<div class="section">
  <h2>🎯 Recommended Target Sizing</h2>
  <table class="specs-table">
    <thead>
      <tr>
        <th>Tier</th>
        <th>vCPU</th>
        <th>RAM (GB)</th>
        <th>Disk (GB)</th>
        <th>IOPS</th>
        <th>Net BW (MB/s)</th>
      </tr>
    </thead>
    <tbody>
      <tr>
        <td><strong>Minimum</strong></td>
        <td>%d</td>
        <td>%.2f</td>
        <td>%.1f</td>
        <td>%d</td>
        <td>%.2f</td>
      </tr>
      <tr>
        <td><strong>Recommended</strong></td>
        <td>%d</td>
        <td>%.2f</td>
        <td>%.1f</td>
        <td>%d</td>
        <td>%.2f</td>
      </tr>
    </tbody>
  </table>
</div>

<div class="footer">
  W-Monitor Assessment Report &mdash; Generated %s
</div>
</body>
</html>`,
		r.Period,
		// Header
		r.Period, r.SampleCount, time.Now().UTC().Format(time.RFC3339),
		// System/Fleet baseline card
		baselineTitle, baselineFleetLine,
		// CPU card
		cpuLabel,
		r.AvgCPU, r.PeakCPU,
		// Memory card
		memLabel,
		r.AvgMem, r.PeakMem, r.MinDiskFreeGB,
		// IOPS card
		r.MinDiskIOPS, r.AvgDiskIOPS, r.PeakDiskIOPS,
		// Network card
		float64(r.TotalNetSentBytes)/(1024*1024*1024),
		float64(r.TotalNetRecvBytes)/(1024*1024*1024),
		r.PeakNetMBps,
		// Users card
		r.MinConcurrentUsers, r.AvgConcurrentUsers, r.MaxConcurrentUsers,
		// Per-server breakdown (empty string for single server)
		perServerSection,
		// Minimum specs
		r.SuggestedMinCPU, r.SuggestedMinRAM, r.SuggestedMinDiskGB, r.SuggestedMinIOPS, r.SuggestedMinNetMBps,
		// Recommended specs
		r.SuggestedRecCPU, r.SuggestedRecRAM, r.SuggestedRecDiskGB, r.SuggestedRecIOPS, r.SuggestedRecNetMBps,
		// Footer
		time.Now().UTC().Format(time.RFC3339),
	)
	_, err := f.WriteString(html)
	return err
}
