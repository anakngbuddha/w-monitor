// Package dashboard serves embedded assets with a strict browser boundary.
package dashboard

import (
	"embed"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"Zeus/server"
)

//go:embed static
var staticFiles embed.FS

// The embedded legacy HTML is a source template, never a public asset. Checked
// transformations retain the layout/calculations while replacing authentication
// and unsafe browser behavior. Source drift fails closed at registration.
func browserAssets() (string, string, error) {
	raw, err := staticFiles.ReadFile("static/index.html")
	if err != nil { return "", "", err }
	auth, err := staticFiles.ReadFile("static/session.js")
	if err != nil { return "", "", err }
	html := string(raw)
	start, end := strings.Index(html, "<script>"), strings.LastIndex(html, "</script>")
	if start < 0 || end <= start || strings.Count(html, "<script>") != 1 { return "", "", fmt.Errorf("dashboard: unexpected script structure") }
	js := html[start+len("<script>"):end]
	html = html[:start]+`<script src="/dashboard.js"></script>`+html[end+len("</script>"):]
	a, b := strings.Index(js, "const API_KEY_STORAGE ="), strings.Index(js, "async function exportCSV()")
	if a < 0 || b <= a { return "", "", fmt.Errorf("dashboard: missing legacy authentication boundary") }
	js = js[:a]+string(auth)+"\n"+js[b:]
	patches := [][2]string{
		{"const serverMap = {};", "const serverMap = Object.create(null);"},
		{"const lookup = {};", "const lookup = Object.create(null);"},
		{"const latestPerServer = {};", "const latestPerServer = Object.create(null);"},
		{"const byKey = {};", "const byKey = Object.create(null);"},
		{"async function fetchServers() {", "async function fetchServers(requestID) {"},
		{"const json = await resp.json();", "const json = await resp.json();\n    if (requestID !== requestGeneration) return;"},
		{"async function fetchData() {\n  try {", "async function fetchData() {\n  if (!sessionReady) return;\n  invalidateRequests();\n  const requestID = requestGeneration;\n  activeController = new AbortController();\n  try {"},
		{"    fetchServers();", "    await fetchServers(requestID);\n    if (requestID !== requestGeneration) return;"},
		{"const mJson = await mResp.json();", "const mJson = await mResp.json();\n    if (requestID !== requestGeneration) return;"},
		{"const data = mJson.data || [];", "const data = (mJson.data || []).sort((a,b) => a.ts-b.ts);"},
		{"const pJson = await pResp.json();", "const pJson = await pResp.json();\n      if (requestID !== requestGeneration) return;"},
		{"  } catch(e) {\n    console.error(e);", "  } catch(e) {\n    if (requestID !== requestGeneration || e.name === 'AbortError') return;\n    console.error('Dashboard request failed');"},
		{"function showNoData() {", "function showNoData() {\n  clearDashboard();"},
		{"const allTimestamps = [...new Set(data.map(d => d.ts))].sort((a, b) => a - b);", "const observed = [...new Set(data.map(d => d.ts))].sort((a, b) => a - b);\n    const allTimestamps = [];\n    observed.forEach((ts,i) => { if (i && ts-observed[i-1]>120) allTimestamps.push(observed[i-1]+1); allTimestamps.push(ts); });"},
		{"document.getElementById('statusLabel').textContent = 'Live';", "const oldestLatest = Object.values(latestPerServer).reduce((oldest,p) => Math.min(oldest,p.ts), Infinity);\n    const stale = Date.now()/1000-oldestLatest>120;\n    document.getElementById('statusLabel').textContent = data.length>=100000 ? 'Result limit reached (incomplete)' : (stale ? 'Stale data (over 2 minutes)' : 'Recent samples');"},
		{"document.getElementById('statusDot').style.background = 'var(--accent-green)';", "document.getElementById('statusDot').style.background = stale ? 'var(--accent-orange)' : 'var(--accent-green)';"},
		{"fetchData();\nsetInterval(fetchData, 15000);", "bootstrapSession();\nsetInterval(() => { if (sessionReady) fetchData(); }, 15000);"},
	}
	for _, patch := range patches {
		if strings.Count(js, patch[0]) != 1 { return "", "", fmt.Errorf("dashboard: security patch anchor changed") }
		js = strings.Replace(js, patch[0], patch[1], 1)
	}
	js = strings.ReplaceAll(js, "spanGaps: true", "spanGaps: false")
	// Do not consume the next link's leading whitespace with an anchored
	// pattern: adjacent external links must all be removed, not every other one.
	html = regexp.MustCompile(`\s*<link[^>]+https://fonts\.[^>]+>`).ReplaceAllString(html, "")
	html = strings.ReplaceAll(html, `https://cdn.jsdelivr.net/npm/chart.js@4.4.0/dist/chart.umd.min.js`, `/chart.umd.min.js`)
	html = regexp.MustCompile(`\s+on(?:click|change)="[^"]*"`).ReplaceAllString(html, "")
	html = strings.ReplaceAll(html, "Enter the API key associated with your monitoring agents to view your servers.", "Enter your dashboard read token. Agent tokens cannot sign in.")
	html = strings.ReplaceAll(html, "Run <code>wmonitor.exe -show-key</code> on any of your monitored servers to retrieve your API key.", "Ask your tenant administrator to issue a dashboard read token. Do not retrieve an agent credential.")
	if strings.Contains(js, "getApiKey(") || strings.Contains(js, "localStorage.setItem") || strings.Contains(html, "https://fonts.") || strings.Contains(html, "https://cdn.") { return "", "", fmt.Errorf("dashboard: unsafe legacy asset survived migration") }
	return html, js, nil
}

func Register(srv *server.Server) error {
	html, js, err := browserAssets()
	if err != nil { return err }
	chart, err := staticFiles.ReadFile("static/chart.umd.min.js")
	if err != nil { return err }
	srv.RegisterStatic(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; font-src 'self'; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'; connect-src 'self'")
		if r.Method != http.MethodGet && r.Method != http.MethodHead { w.Header().Set("Allow", "GET, HEAD"); http.Error(w, "method not allowed", 405); return }
		var body []byte
		switch r.URL.Path {
		case "/", "/index.html": w.Header().Set("Content-Type", "text/html; charset=utf-8"); body = []byte(html)
		case "/dashboard.js": w.Header().Set("Content-Type", "text/javascript; charset=utf-8"); body = []byte(js)
		case "/chart.umd.min.js": w.Header().Set("Content-Type", "text/javascript; charset=utf-8"); body = chart
		default: http.NotFound(w, r); return
		}
		if r.Method == http.MethodGet { w.Write(body) }
	}))
	return nil
}
