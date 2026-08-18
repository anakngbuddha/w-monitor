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

// TestHubIngest verifies the /api/ingest endpoint accepts metrics with an API key
// and isolates metrics between different tenants.
func TestHubIngest(t *testing.T) {
	const key1 = "client-alpha-key"
	const key2 = "client-beta-key"

	tmp := filepath.Join(t.TempDir(), "hub_test.db")
	db, err := storage.Open(tmp)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	srv := server.New(db, "9996")
	srv.EnableHubMode("")

	// 1. Ingest metric for tenant Alpha
	m1 := storage.MetricRow{
		Timestamp:  time.Now(),
		ServerID:   "agent-alpha",
		CPUPct:     77.5,
		MemPct:     50.0,
		DiskFreeGB: 50.0,
	}
	body1, _ := json.Marshal(m1)
	req1 := httptest.NewRequest("POST", "/api/ingest?type=metric", bytes.NewReader(body1))
	req1.Header.Set("Content-Type", "application/json")
	req1.Header.Set("X-API-Key", key1)
	w1 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w1, req1)

	if w1.Code != http.StatusAccepted {
		t.Fatalf("expected 202 Accepted, got %d: %s", w1.Code, w1.Body.String())
	}

	// 2. Ingest metric for tenant Beta
	m2 := storage.MetricRow{
		Timestamp:  time.Now(),
		ServerID:   "agent-beta",
		CPUPct:     33.2,
		MemPct:     25.0,
		DiskFreeGB: 80.0,
	}
	body2, _ := json.Marshal(m2)
	req2 := httptest.NewRequest("POST", "/api/ingest?type=metric", bytes.NewReader(body2))
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("X-API-Key", key2)
	w2 := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w2, req2)

	if w2.Code != http.StatusAccepted {
		t.Fatalf("expected 202 Accepted, got %d: %s", w2.Code, w2.Body.String())
	}

	// 3. Query as tenant Alpha via API — should only see Alpha
	reqAlpha := httptest.NewRequest("GET", "/api/metrics?range=24h", nil)
	reqAlpha.Header.Set("X-API-Key", key1)
	wAlpha := httptest.NewRecorder()
	srv.Handler().ServeHTTP(wAlpha, reqAlpha)

	if wAlpha.Code != http.StatusOK {
		t.Fatalf("Alpha query failed with code %d: %s", wAlpha.Code, wAlpha.Body.String())
	}
	var respAlpha struct {
		Count int `json:"count"`
		Data  []struct {
			ServerID string  `json:"server_id"`
			CPUPct   float64 `json:"cpu_pct"`
		} `json:"data"`
	}
	json.NewDecoder(wAlpha.Body).Decode(&respAlpha)
	if respAlpha.Count != 1 || respAlpha.Data[0].ServerID != "agent-alpha" {
		t.Errorf("expected only Alpha's metric, got: %+v", respAlpha)
	}

	// 4. Query as tenant Beta via API — should only see Beta
	reqBeta := httptest.NewRequest("GET", "/api/metrics?range=24h", nil)
	reqBeta.Header.Set("X-API-Key", key2)
	wBeta := httptest.NewRecorder()
	srv.Handler().ServeHTTP(wBeta, reqBeta)

	if wBeta.Code != http.StatusOK {
		t.Fatalf("Beta query failed with code %d: %s", wBeta.Code, wBeta.Body.String())
	}
	var respBeta struct {
		Count int `json:"count"`
		Data  []struct {
			ServerID string  `json:"server_id"`
			CPUPct   float64 `json:"cpu_pct"`
		} `json:"data"`
	}
	json.NewDecoder(wBeta.Body).Decode(&respBeta)
	if respBeta.Count != 1 || respBeta.Data[0].ServerID != "agent-beta" {
		t.Errorf("expected only Beta's metric, got: %+v", respBeta)
	}

	// 5. Query without X-API-Key in hub mode → 401
	reqNoKey := httptest.NewRequest("GET", "/api/metrics?range=24h", nil)
	wNoKey := httptest.NewRecorder()
	srv.Handler().ServeHTTP(wNoKey, reqNoKey)
	if wNoKey.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for missing key, got %d", wNoKey.Code)
	}

	// 6. Ingest without X-API-Key in hub mode → 401
	reqIngestNoKey := httptest.NewRequest("POST", "/api/ingest?type=metric", bytes.NewReader(body1))
	reqIngestNoKey.Header.Set("Content-Type", "application/json")
	wIngestNoKey := httptest.NewRecorder()
	srv.Handler().ServeHTTP(wIngestNoKey, reqIngestNoKey)
	if wIngestNoKey.Code != http.StatusUnauthorized {
		t.Errorf("expected 401 for ingest without key, got %d", wIngestNoKey.Code)
	}

	t.Logf("Multi-tenant isolation and API key enforcement verified ✓")
}
