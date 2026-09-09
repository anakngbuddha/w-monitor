package server_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"Zeus/server"
	"Zeus/storage"
)

func TestAPIMetrics(t *testing.T) {
	db, err := storage.Open(filepath.Join(t.TempDir(), "metrics.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	now := time.Now().UTC()
	for i := 0; i < 5; i++ {
		if err := db.InsertMetric(storage.MetricRow{Timestamp: now.Add(-time.Duration(i) * time.Hour), CPUPct: float64(20 + i), MemPct: float64(50 + i), DiskFreeGB: float64(100 - i), DiskIOPS: float64(80 + i*5), NetMBps: 1.2, ConcurrentUsers: 3}); err != nil {
			t.Fatal(err)
		}
	}
	srv := server.New(db, "9999")
	w := do(srv, "GET", "/api/metrics?range=24h", "", nil)
	if w.Code != 200 {
		t.Fatalf("metrics: %d %s", w.Code, w.Body.String())
	}
	var response struct {
		Range string `json:"range"`
		Count int    `json:"count"`
		Data  []struct {
			DiskIOPS float64 `json:"disk_iops"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Range != "24h" || response.Count != 5 || len(response.Data) != 5 || response.Data[4].DiskIOPS != 80 {
		t.Fatalf("metric response changed: %+v", response)
	}
	if srv.DashboardViewers() != 0 {
		t.Fatal("API polling was counted as a page viewer")
	}
	srv.RegisterStatic(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	do(srv, "GET", "/", "", nil)
	if srv.DashboardViewers() != 1 {
		t.Fatal("page viewer not tracked")
	}
	empty, err := storage.Open(filepath.Join(t.TempDir(), "empty.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer empty.Close()
	w = do(server.New(empty, "9998"), "GET", "/api/metrics", "", nil)
	if w.Code != 200 {
		t.Fatal("empty query failed")
	}
	var body struct {
		Data []interface{} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body.Data == nil || len(body.Data) != 0 {
		t.Fatal("empty result must be []")
	}
}

func TestHealthReportsLivenessWithoutCustomerFreshness(t *testing.T) {
	db, err := storage.Open(filepath.Join(t.TempDir(), "health.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.InsertMetric(storage.MetricRow{Timestamp: time.Now(), TenantID: "private-tenant", CPUPct: 5, MemPct: 5}); err != nil {
		t.Fatal(err)
	}
	w := do(server.New(db, "9995"), "GET", "/api/health", "", nil)
	if w.Code != 200 {
		t.Fatal("liveness failed")
	}
	var body map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["status"] != "ok" || body["uptime_seconds"] == nil {
		t.Fatal("liveness fields missing")
	}
	for _, key := range []string{"last_metric_age_seconds", "active_alerts", "tenant_id"} {
		if _, exists := body[key]; exists {
			t.Fatalf("public health leaks %s", key)
		}
	}
	ret, ok := body["retention"].(map[string]interface{})
	if !ok || ret["downsampling_enabled"] != false || ret["purge_enabled"] != false {
		t.Fatal("unsafe retention containment changed")
	}
}

func TestClosedDatabaseFailsReadinessButNotLiveness(t *testing.T) {
	db, err := storage.Open(filepath.Join(t.TempDir(), "closed.db"))
	if err != nil {
		t.Fatal(err)
	}
	srv := server.New(db, "9994")
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if w := do(srv, "GET", "/api/health", "", nil); w.Code != 200 {
		t.Fatal("liveness must not depend on DB")
	}
	if w := do(srv, "GET", "/api/ready", "", nil); w.Code != 503 {
		t.Fatalf("dead DB reported ready: %d", w.Code)
	}
}

func TestReadyEndpoint(t *testing.T) {
	db, err := storage.Open(filepath.Join(t.TempDir(), "ready.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if w := do(server.New(db, "9993"), "GET", "/api/ready", "", nil); w.Code != 200 {
		t.Fatalf("ready status=%d", w.Code)
	}
}

func TestPrometheusEndpoint(t *testing.T) {
	db, err := storage.Open(filepath.Join(t.TempDir(), "prom.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	w := do(server.New(db, "9992"), "GET", "/metrics", "", nil)
	if w.Code != 200 {
		t.Fatal("local metrics failed")
	}
	for _, metric := range []string{"wmonitor_up", "wmonitor_uptime_seconds"} {
		if !contains(w.Body.String(), metric) {
			t.Errorf("missing %s", metric)
		}
	}
}

func TestPrometheusProxyLoopbackDoesNotBypassAuth(t *testing.T) {
	srv, _, _ := hubFixture(t)
	r := httptest.NewRequest("GET", "/metrics", nil)
	r.RemoteAddr = "127.0.0.1:5555"
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("proxy bypass: %d", w.Code)
	}
}

func contains(haystack, needle string) bool { return strings.Contains(haystack, needle) }

func TestAPIServers(t *testing.T) {
	db, err := storage.Open(filepath.Join(t.TempDir(), "servers.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, id := range []string{"srv-a", "srv-b", "srv-a"} {
		if err := db.InsertMetric(storage.MetricRow{Timestamp: time.Now(), ServerID: id, CPUPct: 1, MemPct: 1}); err != nil {
			t.Fatal(err)
		}
	}
	w := do(server.New(db, "9997"), "GET", "/api/servers", "", nil)
	if w.Code != 200 {
		t.Fatal("server query failed")
	}
	var response struct {
		Servers []string `json:"servers"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || len(response.Servers) != 2 {
		t.Fatal("distinct server result changed")
	}
}
