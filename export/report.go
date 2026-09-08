// Package export — assessment report generator (Phase 12).
// Produces a self-contained HTML report that can be printed to PDF from any browser.
package export

import (
	"fmt"
	"html/template"
	"io"
	"os"
	"path/filepath"
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
func GenerateAssessmentReport(db storage.Store, start, end time.Time, outPath, tenantID string) error {
	if err := storage.RequireTenant(tenantID); err != nil {
		return err
	}
	rows, err := db.QueryMetrics(start, tenantID)
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
	return writeHTMLReportTo(f, r)
}

func writeHTMLReportTo(w io.Writer, r AssessmentReport) error {
	cpuLabel := "CPU Usage"
	memLabel := "Memory Usage"
	baselineTitle := "System Baseline"
	if r.FleetServerCount > 1 {
		cpuLabel = "CPU Usage (Fleet %)"
		memLabel = "Memory Usage (Fleet %)"
		baselineTitle = fmt.Sprintf("Fleet Baseline (%d Servers)", r.FleetServerCount)
	}

	data := htmlReportData{
		Period:         r.Period,
		SampleCount:    r.SampleCount,
		Generated:      time.Now().UTC().Format(time.RFC3339),
		BaselineTitle:  baselineTitle,
		ShowFleet:      r.FleetServerCount > 1,
		FleetServers:   r.FleetServerCount,
		CPUCores:       r.CPUCores,
		MemTotalGB:     r.MemTotalGB,
		DiskTotalGB:    r.DiskTotalGB,
		CPULabel:       cpuLabel,
		MemLabel:       memLabel,
		AvgCPU:         r.AvgCPU,
		PeakCPU:        r.PeakCPU,
		AvgMem:         r.AvgMem,
		PeakMem:        r.PeakMem,
		MinDiskFreeGB:  r.MinDiskFreeGB,
		MinDiskIOPS:    r.MinDiskIOPS,
		AvgDiskIOPS:    r.AvgDiskIOPS,
		PeakDiskIOPS:   r.PeakDiskIOPS,
		TotalNetSentGB: float64(r.TotalNetSentBytes) / (1024 * 1024 * 1024),
		TotalNetRecvGB: float64(r.TotalNetRecvBytes) / (1024 * 1024 * 1024),
		PeakNetMBps:    r.PeakNetMBps,
		MinUsers:       r.MinConcurrentUsers,
		AvgUsers:       r.AvgConcurrentUsers,
		MaxUsers:       r.MaxConcurrentUsers,
		Servers:        r.Servers,
		MinCPU:         r.SuggestedMinCPU,
		MinRAM:         r.SuggestedMinRAM,
		MinDisk:        r.SuggestedMinDiskGB,
		MinIOPS:        r.SuggestedMinIOPS,
		MinNet:         r.SuggestedMinNetMBps,
		RecCPU:         r.SuggestedRecCPU,
		RecRAM:         r.SuggestedRecRAM,
		RecDisk:        r.SuggestedRecDiskGB,
		RecIOPS:        r.SuggestedRecIOPS,
		RecNet:         r.SuggestedRecNetMBps,
	}
	return assessmentHTML.Execute(w, data)
}

type htmlReportData struct {
	Period, Generated, BaselineTitle, CPULabel, MemLabel string
	SampleCount, FleetServers                            int
	ShowFleet                                            bool
	CPUCores                                             int
	MemTotalGB, DiskTotalGB                              float64
	AvgCPU, PeakCPU, AvgMem, PeakMem, MinDiskFreeGB      float64
	MinDiskIOPS, AvgDiskIOPS, PeakDiskIOPS               float64
	TotalNetSentGB, TotalNetRecvGB, PeakNetMBps          float64
	MinUsers, MaxUsers                                   int
	AvgUsers                                             float64
	Servers                                              []ServerSummary
	MinCPU, RecCPU, MinIOPS, RecIOPS                     int
	MinRAM, MinDisk, MinNet, RecRAM, RecDisk, RecNet     float64
}

var assessmentHTML = template.Must(template.New("assessment").Parse(assessmentHTMLSrc))

const assessmentHTMLSrc = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<meta http-equiv="Content-Security-Policy" content="default-src 'none'; style-src 'unsafe-inline'; img-src 'none'; script-src 'none'; object-src 'none'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'">
<title>W-Monitor Assessment Report — {{.Period}}</title>
<style>
  :root {
    --primary: #1e40af;
    --accent:  #3b82f6;
    --bg:      #f8fafc;
    --card:    #ffffff;
    --border:  #e2e8f0;
    --text:    #1e293b;
    --muted:   #64748b;
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
  .specs-table { width: 100%; border-collapse: collapse; margin-top: 1rem; overflow-x: auto; }
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
  <h1>W-Monitor — Pre-Migration Assessment Report</h1>
  <p>Period: {{.Period}} | Samples: {{.SampleCount}} | Generated: {{.Generated}}</p>
</div>
<div class="grid">
  <div class="card">
    <h2>{{.BaselineTitle}}</h2>
    {{if .ShowFleet}}<div class="metric"><span class="label">Servers</span><span class="value">{{.FleetServers}}</span></div>{{end}}
    <div class="metric"><span class="label">{{if .ShowFleet}}Total vCPUs{{else}}vCPUs{{end}}</span><span class="value">{{.CPUCores}}</span></div>
    <div class="metric"><span class="label">{{if .ShowFleet}}Total RAM{{else}}Total RAM{{end}}</span><span class="value">{{printf "%.1f GB" .MemTotalGB}}</span></div>
    <div class="metric"><span class="label">{{if .ShowFleet}}Total Disk{{else}}Total Disk{{end}}</span><span class="value">{{printf "%.1f GB" .DiskTotalGB}}</span></div>
  </div>
  <div class="card">
    <h2>{{.CPULabel}}</h2>
    <div class="metric"><span class="label">Average</span><span class="value">{{printf "%.2f%%" .AvgCPU}}</span></div>
    <div class="metric"><span class="label">Peak</span><span class="value">{{printf "%.2f%%" .PeakCPU}}</span></div>
  </div>
  <div class="card">
    <h2>{{.MemLabel}}</h2>
    <div class="metric"><span class="label">Average</span><span class="value">{{printf "%.2f%%" .AvgMem}}</span></div>
    <div class="metric"><span class="label">Peak</span><span class="value">{{printf "%.2f%%" .PeakMem}}</span></div>
    <div class="metric"><span class="label">Min Free Disk</span><span class="value">{{printf "%.2f GB" .MinDiskFreeGB}}</span></div>
  </div>
  <div class="card">
    <h2>Disk IOPS</h2>
    <div class="metric"><span class="label">Minimum</span><span class="value">{{printf "%.1f" .MinDiskIOPS}}</span></div>
    <div class="metric"><span class="label">Average</span><span class="value">{{printf "%.1f" .AvgDiskIOPS}}</span></div>
    <div class="metric"><span class="label">Peak</span><span class="value">{{printf "%.1f" .PeakDiskIOPS}}</span></div>
  </div>
  <div class="card">
    <h2>Network Bandwidth</h2>
    <div class="metric"><span class="label">Total Sent</span><span class="value">{{printf "%.2f GB" .TotalNetSentGB}}</span></div>
    <div class="metric"><span class="label">Total Recv</span><span class="value">{{printf "%.2f GB" .TotalNetRecvGB}}</span></div>
    <div class="metric"><span class="label">Peak Rate</span><span class="value">{{printf "%.2f MB/s" .PeakNetMBps}}</span></div>
  </div>
  <div class="card">
    <h2>Concurrent Users</h2>
    <div class="metric"><span class="label">Minimum</span><span class="value">{{.MinUsers}}</span></div>
    <div class="metric"><span class="label">Average</span><span class="value">{{printf "%.1f" .AvgUsers}}</span></div>
    <div class="metric"><span class="label">Peak</span><span class="value">{{.MaxUsers}}</span></div>
  </div>
</div>
{{if .ShowFleet}}
<div class="section">
  <h2>Per-Server Breakdown</h2>
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
      {{range .Servers}}
      <tr>
        <td>{{.ServerID}}</td>
        <td>{{.Hostname}}</td>
        <td>{{.CPUCores}}</td>
        <td>{{printf "%.1f" .MemTotalGB}}</td>
        <td>{{printf "%.1f" .DiskTotalGB}}</td>
        <td>{{printf "%.1f%%" .AvgCPU}}</td>
        <td>{{printf "%.1f%%" .PeakCPU}}</td>
        <td>{{printf "%.1f%%" .AvgMem}}</td>
        <td>{{printf "%.1f%%" .PeakMem}}</td>
        <td>{{printf "%.0f" .PeakDiskIOPS}}</td>
        <td>{{printf "%.2f" .PeakNetMBps}}</td>
        <td>{{printf "%d vCPU / %.1f GB RAM" .SuggestedMinCPU .SuggestedMinRAM}}</td>
        <td>{{printf "%d vCPU / %.1f GB RAM" .SuggestedRecCPU .SuggestedRecRAM}}</td>
      </tr>
      {{end}}
    </tbody>
  </table>
</div>
{{end}}
<div class="section">
  <h2>Recommended Target Sizing</h2>
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
        <td>{{.MinCPU}}</td>
        <td>{{printf "%.2f" .MinRAM}}</td>
        <td>{{printf "%.1f" .MinDisk}}</td>
        <td>{{.MinIOPS}}</td>
        <td>{{printf "%.2f" .MinNet}}</td>
      </tr>
      <tr>
        <td><strong>Recommended</strong></td>
        <td>{{.RecCPU}}</td>
        <td>{{printf "%.2f" .RecRAM}}</td>
        <td>{{printf "%.1f" .RecDisk}}</td>
        <td>{{.RecIOPS}}</td>
        <td>{{printf "%.2f" .RecNet}}</td>
      </tr>
    </tbody>
  </table>
</div>
<div class="footer">
  W-Monitor Assessment Report — Generated {{.Generated}}
</div>
</body>
</html>
`
