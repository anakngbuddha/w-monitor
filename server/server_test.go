package server_test

import (
	"bytes"
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

// TestHubIngest verifies the /api/ingest endpoint accepts metrics with a valid API key
// and rejects requests with a wrong key.
func TestHubIngest(t *testing.T) {
	const apiKey = "supersecret"

	tmp := filepath.Join(t.TempDir(), "hub_test.db")
	db, err := storage.Open(tmp)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	srv := server.New(db, "9996")
	srv.EnableHubMode(apiKey)

	// Insert via /api/ingest
	m := storage.MetricRow{
		Timestamp: time.Now(),
		ServerID:  "agent-01",
		CPUPct:    77.5,
		MemPct:    50.0,
		DiskFreeGB: 50.0,
	}
	body, _ := json.Marshal(m)

	req := httptest.NewRequest("POST", "/api/ingest?type=metric", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", apiKey)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusAccepted {
		t.Fatalf("expected 202 Accepted, got %d: %s", w.Code, w.Body.String())
	}

	// Verify row was inserted
	rows, err := db.QueryMetrics(time.Now().Add(-time.Minute))
	if err != nil {
		t.Fatalf("QueryMetrics: %v", err)
	}
	if len(rows) != 1 {
		t.Errorf("expected 1 row after ingest, got %d", len(rows))
	} else if rows[0].ServerID != "agent-01" {
		t.Errorf("expected ServerID=agent-01, got %q", rows[0].ServerID)
	} else if rows[0].CPUPct != 77.5 {
		t.Errorf("expected CPUPct=77.5, got %v", rows[0].CPUPct)
	}

	// Wrong key → 401
	req2 := httptest.NewRequest("POST", "/api/ingest?type=metric", bytes.NewReader(body))
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("X-API-Key", "wrong-key")
	w2 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w2, req2)
	if w2.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for wrong key, got %d", w2.Code)
	}

	t.Logf("Hub ingest: POST /api/ingest → 202; wrong key → 401 ✓")
}
