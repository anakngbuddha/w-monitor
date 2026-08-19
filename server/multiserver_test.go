package server_test

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"Zeus/collector"
	"Zeus/dashboard"
	"Zeus/server"
	"Zeus/storage"
)

func TestMultiServerDashboardAndMetrics(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "multiserver_test.db")
	db, err := storage.Open(tmp)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	now := time.Now().UTC()

	// Insert data for 3 distinct servers across 3 time points
	servers := []string{"web-prod-01", "web-prod-02", "db-prod-01"}
	for _, s := range servers {
		for i := 0; i < 3; i++ {
			db.InsertMetric(storage.MetricRow{
				Timestamp:       now.Add(-time.Duration(i*10) * time.Second),
				ServerID:        s,
				Hostname:        s + ".internal",
				CPUPct:          float64(20 + len(s)*2 + i),
				MemPct:          float64(40 + len(s)*3 + i),
				DiskFreeGB:      float64(100 - i*5),
				NetSentBytes:    uint64(5000 * (i + 1)),
				NetRecvBytes:    uint64(10000 * (i + 1)),
				CPUCores:        4,
				MemTotalGB:      16.0,
				DiskTotalGB:     200.0,
				DiskIOPS:        float64(150 + i*10),
				NetMBps:         float64(2.5 + float64(i)*0.2),
				ConcurrentUsers: 5 + i,
			})
			db.InsertProcess(storage.ProcessRow{
				Timestamp: now.Add(-time.Duration(i*10) * time.Second),
				ServerID:  s,
				Hostname:  s + ".internal",
				PID:       int32(1000 + i),
				Name:      "nginx",
				CPUPct:    float64(15 + i),
				MemMB:     128.0,
			})
		}
	}

	srv := server.New(db, "9990")
	if err := dashboard.Register(srv); err != nil {
		t.Fatalf("dashboard register: %v", err)
	}

	// 1. Verify /api/servers returns all 3 distinct servers
	reqSrv := httptest.NewRequest("GET", "/api/servers", nil)
	wSrv := httptest.NewRecorder()
	srv.Handler().ServeHTTP(wSrv, reqSrv)

	if wSrv.Code != http.StatusOK {
		t.Fatalf("/api/servers expected 200, got %d", wSrv.Code)
	}
	var srvListResp struct {
		Servers []string `json:"servers"`
	}
	if err := json.NewDecoder(wSrv.Body).Decode(&srvListResp); err != nil {
		t.Fatalf("decode /api/servers: %v", err)
	}
	t.Logf("Returned servers: %v", srvListResp.Servers)
	if len(srvListResp.Servers) != 3 {
		t.Errorf("expected 3 servers, got %d", len(srvListResp.Servers))
	}

	// 2. Verify /api/metrics without server_id returns all 9 records
	reqAll := httptest.NewRequest("GET", "/api/metrics?range=24h", nil)
	wAll := httptest.NewRecorder()
	srv.Handler().ServeHTTP(wAll, reqAll)

	if wAll.Code != http.StatusOK {
		t.Fatalf("/api/metrics expected 200, got %d", wAll.Code)
	}
	var allMetricsResp struct {
		Count int `json:"count"`
		Data  []struct {
			ServerID        string  `json:"server_id"`
			CPUPct          float64 `json:"cpu_pct"`
			ConcurrentUsers int     `json:"concurrent_users"`
		} `json:"data"`
	}
	if err := json.NewDecoder(wAll.Body).Decode(&allMetricsResp); err != nil {
		t.Fatalf("decode all metrics: %v", err)
	}
	if allMetricsResp.Count != 9 {
		t.Errorf("expected 9 metric data points for all servers, got %d", allMetricsResp.Count)
	}

	// 3. Verify /api/metrics?server_id=web-prod-01 filters specifically to that server
	reqSingle := httptest.NewRequest("GET", "/api/metrics?range=24h&server_id=web-prod-01", nil)
	wSingle := httptest.NewRecorder()
	srv.Handler().ServeHTTP(wSingle, reqSingle)

	var singleMetricsResp struct {
		Count int `json:"count"`
		Data  []struct {
			ServerID string `json:"server_id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(wSingle.Body).Decode(&singleMetricsResp); err != nil {
		t.Fatalf("decode single metrics: %v", err)
	}
	if singleMetricsResp.Count != 3 {
		t.Errorf("expected 3 metric data points for web-prod-01, got %d", singleMetricsResp.Count)
	}
	for _, pt := range singleMetricsResp.Data {
		if pt.ServerID != "web-prod-01" {
			t.Errorf("expected server_id 'web-prod-01', got %q", pt.ServerID)
		}
	}
}

func TestAppUserCollectorIntegration(t *testing.T) {
	// Start a sample web application server on localhost
	testMux := http.NewServeMux()
	testMux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("Hello from user web app!"))
	})
	testAppServer := httptest.NewServer(testMux)
	defer testAppServer.Close()

	appAddr := testAppServer.Listener.Addr().(*net.TCPAddr)
	appPort := uint32(appAddr.Port)

	// Create a storage DB and collector targeting this app port
	tmp := filepath.Join(t.TempDir(), "app_user_col_test.db")
	db, err := storage.Open(tmp)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	col := collector.NewWithInterval(db, 1*time.Second)
	if tracker, ok := col.UserTracker().(*collector.TCPUserTracker); ok {
		tracker.SetAppPorts(appPort)
	}

	// Dial the web application server to simulate active client usage
	conn, err := net.Dial("tcp", testAppServer.Listener.Addr().String())
	if err != nil {
		t.Fatalf("Dial app server: %v", err)
	}
	defer conn.Close()

	// Send an HTTP request and keep connection open
	conn.Write([]byte("GET / HTTP/1.1\r\nHost: localhost\r\n\r\n"))
	time.Sleep(100 * time.Millisecond)

	// Verify the collector's tracker detected the active app user
	activeUsers := col.UserTracker().GetConcurrentUsers()
	t.Logf("Active users detected on app port %d: %d", appPort, activeUsers)
	if activeUsers < 1 {
		t.Errorf("expected at least 1 active user connecting to app port %d, got %d", appPort, activeUsers)
	}
}
