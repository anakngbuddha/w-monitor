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

	// Verify ConcurrentUsers tracking via request
	if srv.GetConcurrentUsers() < 1 {
		t.Errorf("expected at least 1 concurrent user after request, got %d", srv.GetConcurrentUsers())
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

	// --- Test /api/health ---
	req3 := httptest.NewRequest("GET", "/api/health", nil)
	w3 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w3, req3)
	if w3.Code != http.StatusOK {
		t.Fatalf("health: expected 200, got %d", w3.Code)
	}
	t.Logf("/api/health → %s", w3.Body.String())
}
