package agent_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"Zeus/agent"
	"Zeus/storage"
)

// TestAgentIngestMetric starts a mock hub server and verifies the agent
// correctly POSTs a MetricRow with the right API key and payload.
func TestAgentIngestMetric(t *testing.T) {
	const testKey = "test-api-key-12345"

	var receivedType string
	var receivedKey string
	var receivedBody []byte

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedType = r.URL.Query().Get("type")
		receivedKey = r.Header.Get("X-API-Key")
		var buf [4096]byte
		n, _ := r.Body.Read(buf[:])
		receivedBody = buf[:n]
		w.WriteHeader(http.StatusAccepted)
		w.Write([]byte(`{"status":"accepted"}`))
	}))
	defer ts.Close()

	ag := agent.New(ts.URL, testKey)

	m := storage.MetricRow{
		Timestamp:       time.Now(),
		ServerID:        "srv-001",
		Hostname:        "testhost",
		CPUPct:          42.5,
		MemPct:          65.0,
		DiskFreeGB:      100.0,
		ConcurrentUsers: 3,
	}
	if err := ag.InsertMetric(m); err != nil {
		t.Fatalf("InsertMetric: %v", err)
	}

	if receivedType != "metric" {
		t.Errorf("expected type=metric, got %q", receivedType)
	}
	if receivedKey != testKey {
		t.Errorf("expected X-API-Key=%q, got %q", testKey, receivedKey)
	}

	var decoded storage.MetricRow
	if err := json.Unmarshal(receivedBody, &decoded); err != nil {
		t.Fatalf("unmarshal body: %v", err)
	}
	if decoded.ServerID != "srv-001" {
		t.Errorf("expected ServerID=srv-001, got %q", decoded.ServerID)
	}
	if decoded.CPUPct != 42.5 {
		t.Errorf("expected CPUPct=42.5, got %v", decoded.CPUPct)
	}
}

// TestAgentIngestProcess verifies process rows are POSTed correctly.
func TestAgentIngestProcess(t *testing.T) {
	const testKey = "test-key"

	var receivedType string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedType = r.URL.Query().Get("type")
		w.WriteHeader(http.StatusAccepted)
		w.Write([]byte(`{"status":"accepted"}`))
	}))
	defer ts.Close()

	ag := agent.New(ts.URL, testKey)

	p := storage.ProcessRow{
		Timestamp: time.Now(),
		ServerID:  "srv-001",
		PID:       1234,
		Name:      "nginx",
		CPUPct:    5.0,
		MemMB:     128.0,
	}
	if err := ag.InsertProcess(p); err != nil {
		t.Fatalf("InsertProcess: %v", err)
	}
	if receivedType != "process" {
		t.Errorf("expected type=process, got %q", receivedType)
	}
}

// TestAgentUnauthorized verifies the agent returns an error on 401.
func TestAgentUnauthorized(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":"unauthorized"}`))
	}))
	defer ts.Close()

	ag := agent.New(ts.URL, "wrong-key")
	err := ag.InsertMetric(storage.MetricRow{Timestamp: time.Now()})
	if err == nil {
		t.Fatal("expected error on 401, got nil")
	}
	t.Logf("Got expected error: %v", err)
}
