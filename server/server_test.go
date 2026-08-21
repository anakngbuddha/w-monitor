package server_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"Zeus/server"
	"Zeus/storage"
)

func TestAPIMetrics(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "server_test.db")
	db, err := storage.Open(tmp)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	// Insert 5 rows within the last 24h
	now := time.Now().UTC()
	for i := 0; i < 5; i++ {
		db.InsertMetric(storage.MetricRow{
			Timestamp:       now.Add(-time.Duration(i) * time.Hour),
			CPUPct:          float64(20 + i),
			MemPct:          float64(50 + i),
			DiskFreeGB:      float64(100 - i),
			NetSentBytes:    uint64(1000 * i),
			NetRecvBytes:    uint64(2000 * i),
			DiskIOPS:        float64(80 + i*5),
			NetMBps:         float64(1.2 + float64(i)*0.1),
			ConcurrentUsers: 3,
		})
	}

	srv := server.New(db, "9999")

	// --- Test /api/metrics?range=24h ---
	req := httptest.NewRequest("GET", "/api/metrics?range=24h", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Range string `json:"range"`
		Count int    `json:"count"`
		Data  []struct {
			Ts              int64   `json:"ts"`
			CPUPct          float64 `json:"cpu_pct"`
			DiskIOPS        float64 `json:"disk_iops"`
			NetMBps         float64 `json:"net_mbps"`
			ConcurrentUsers int     `json:"concurrent_users"`
		} `json:"data"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	t.Logf("/api/metrics?range=24h → count=%d, range=%s", resp.Count, resp.Range)
	if resp.Count != 5 {
		t.Errorf("expected 5 data points, got %d", resp.Count)
	}
	if resp.Range != "24h" {
		t.Errorf("expected range=24h, got %s", resp.Range)
	}
	// Rows are ordered ASC by timestamp; Data[last] is the newest (i=0, DiskIOPS=80)
	if len(resp.Data) > 0 && resp.Data[len(resp.Data)-1].DiskIOPS != 80.0 {
		t.Errorf("expected newest disk_iops=80.0, got %v", resp.Data[len(resp.Data)-1].DiskIOPS)
	}

	// Dashboard viewers are tracked per source IP. Note this is deliberately NOT
	// the same thing as the collector's concurrent-user metric.
	if srv.DashboardViewers() < 1 {
		t.Errorf("expected at least 1 dashboard viewer after a request, got %d", srv.DashboardViewers())
	}

	// --- Test empty DB returns empty array, not null ---
	emptyDB, _ := storage.Open(filepath.Join(t.TempDir(), "empty.db"))
	defer emptyDB.Close()
	srvEmpty := server.New(emptyDB, "9998")

	req2 := httptest.NewRequest("GET", "/api/metrics?range=24h", nil)
	w2 := httptest.NewRecorder()
	srvEmpty.Handler().ServeHTTP(w2, req2)

	if w2.Code != http.StatusOK {
		t.Fatalf("empty DB: expected 200, got %d", w2.Code)
	}
	var emptyResp struct {
		Data []interface{} `json:"data"`
	}
	if err := json.NewDecoder(w2.Body).Decode(&emptyResp); err != nil {
		t.Fatalf("decode empty response: %v", err)
	}
	if emptyResp.Data == nil {
		t.Error("expected empty array [], got null for empty DB")
	}
	t.Logf("empty DB returns: data len=%d (should be 0)", len(emptyResp.Data))
}

// A healthy server reports ok with freshness information instead of the old
// unbounded COUNT(*) row totals.
func TestHealthReportsStatusAndFreshness(t *testing.T) {
	db, err := storage.Open(filepath.Join(t.TempDir(), "health_test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	db.InsertMetric(storage.MetricRow{Timestamp: time.Now(), CPUPct: 5, MemPct: 5})

	srv := server.New(db, "9995")
	req := httptest.NewRequest("GET", "/api/health", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var body map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["status"] != "ok" {
		t.Errorf("status = %v, want ok", body["status"])
	}
	if _, ok := body["last_metric_age_seconds"]; !ok {
		t.Error("health response is missing last_metric_age_seconds")
	}
	if _, ok := body["uptime_seconds"]; !ok {
		t.Error("health response is missing uptime_seconds")
	}
}

// A closed database must not be reported as healthy. The old handler discarded
// the error and always answered 200 "ok".
func TestHealthReportsDegradedWhenDBIsDown(t *testing.T) {
	db, err := storage.Open(filepath.Join(t.TempDir(), "health_down_test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	srv := server.New(db, "9994")
	db.Close() // simulate an unreachable backend

	req := httptest.NewRequest("GET", "/api/health", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 for an unreachable database, got %d: %s", w.Code, w.Body.String())
	}

	var body map[string]interface{}
	json.NewDecoder(w.Body).Decode(&body)
	if body["status"] == "ok" {
		t.Error("a dead database was reported as healthy")
	}
}

func TestReadyEndpoint(t *testing.T) {
	db, err := storage.Open(filepath.Join(t.TempDir(), "ready_test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	srv := server.New(db, "9993")
	req := httptest.NewRequest("GET", "/api/ready", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

func TestPrometheusEndpoint(t *testing.T) {
	db, err := storage.Open(filepath.Join(t.TempDir(), "prom_test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	srv := server.New(db, "9992")
	req := httptest.NewRequest("GET", "/metrics", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	body := w.Body.String()
	for _, want := range []string{"wmonitor_up", "wmonitor_uptime_seconds"} {
		if !contains(body, want) {
			t.Errorf("prometheus output missing %q\n%s", want, body)
		}
	}
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (func() bool {
		for i := 0; i+len(needle) <= len(haystack); i++ {
			if haystack[i:i+len(needle)] == needle {
				return true
			}
		}
		return false
	})()
}

// TestAPIServers verifies the /api/servers endpoint returns distinct server IDs.
func TestAPIServers(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "servers_test.db")
	db, err := storage.Open(tmp)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	now := time.Now().UTC()
	for _, sid := range []string{"srv-a", "srv-b", "srv-a"} {
		db.InsertMetric(storage.MetricRow{
			Timestamp: now,
			ServerID:  sid,
			CPUPct:    1.0,
			MemPct:    1.0,
		})
	}

	srv := server.New(db, "9997")
	req := httptest.NewRequest("GET", "/api/servers", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		Servers []string `json:"servers"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	t.Logf("/api/servers → %v", resp.Servers)
	if len(resp.Servers) != 2 {
		t.Errorf("expected 2 distinct servers, got %d: %v", len(resp.Servers), resp.Servers)
	}
}

// Hub ingest, tenant isolation, and key rejection are covered in auth_test.go,
// which replaced the old TestHubIngest. That test asserted the previous
// behaviour where any non-empty X-API-Key was accepted as a valid tenant, which
// is precisely the vulnerability that was fixed.
