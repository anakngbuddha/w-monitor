package agent_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"Zeus/agent"
	"Zeus/storage"
)

func newAgentFixture(t *testing.T, hubURL, key string) *agent.Agent {
	t.Helper()
	a, err := agent.NewWithDataDir(hubURL, key, t.TempDir())
	if err != nil {
		t.Fatalf("construct isolated agent: %v", err)
	}
	t.Cleanup(func() {
		if err := a.Close(); err != nil {
			t.Errorf("close isolated agent: %v", err)
		}
	})
	return a
}

type receivedRequest struct {
	payloadType string
	key         string
	body        []byte
	err         error
}

func TestAgentIngestMetric(t *testing.T) {
	const testKey = "test-api-key-12345"
	received := make(chan receivedRequest, 1)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		received <- receivedRequest{r.URL.Query().Get("type"), r.Header.Get("X-API-Key"), body, err}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer ts.Close()
	ag := newAgentFixture(t, ts.URL, testKey)
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
	var got receivedRequest
	select {
	case got = <-received:
	case <-time.After(time.Second):
		t.Fatal("hub did not receive metric")
	}
	if got.err != nil {
		t.Fatal(got.err)
	}
	if got.payloadType != "metric" || got.key != testKey {
		t.Fatal("incorrect request type or credential")
	}
	var decoded storage.MetricRow
	if err := json.Unmarshal(got.body, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.ServerID != m.ServerID || decoded.CPUPct != m.CPUPct {
		t.Fatal("metric payload changed")
	}
}

func TestAgentIngestProcess(t *testing.T) {
	received := make(chan string, 1)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received <- r.URL.Query().Get("type")
		w.WriteHeader(http.StatusAccepted)
	}))
	defer ts.Close()
	ag := newAgentFixture(t, ts.URL, "test-key")
	p := storage.ProcessRow{
		Timestamp: time.Now(),
		ServerID:  "srv-001",
		PID:       1234,
		Name:      "nginx",
		CPUPct:    5.0,
		MemMB:     128.0,
	}
	if err := ag.InsertProcess(p); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-received:
		if got != "process" {
			t.Fatalf("request type = %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("hub did not receive process")
	}
}

func TestAgentUnauthorized(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer ts.Close()
	ag := newAgentFixture(t, ts.URL, "wrong-key")
	if err := ag.InsertMetric(storage.MetricRow{Timestamp: time.Now()}); err == nil {
		t.Fatal("expected error on 401")
	}
}

func TestAgentExplicitDirectoryPersistsOnlyItsBacklog(t *testing.T) {
	root := t.TempDir()
	canary := filepath.Join(root, "outside-canary")
	if err := os.WriteFile(canary, []byte("unchanged"), 0600); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "agent")
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer ts.Close()
	a, err := agent.NewWithDataDir(ts.URL, "test-key", dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close() })
	if err := a.InsertMetric(storage.MetricRow{Timestamp: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if a.SpoolDepth() != 1 {
		t.Fatal("failed delivery not present in isolated spool")
	}
	if err := a.Close(); err != nil {
		t.Fatal(err)
	}
	b, err := agent.NewWithDataDir(ts.URL, "test-key", dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.Close() })
	if b.SpoolDepth() != 1 {
		t.Fatal("isolated backlog did not survive reopen")
	}
	got, err := os.ReadFile(canary)
	if err != nil || string(got) != "unchanged" {
		t.Fatal("agent changed outside canary")
	}
}

func TestAgentExplicitDirectoryRejectsFallback(t *testing.T) {
	for _, dir := range []string{"", ".", "relative"} {
		if a, err := agent.NewWithDataDir("https://hub.example.com", "test-key", dir); err == nil {
			_ = a.Close()
			t.Errorf("accepted implicit directory %q", dir)
		}
	}
	file := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(file, []byte("canary"), 0600); err != nil {
		t.Fatal(err)
	}
	if a, err := agent.NewWithDataDir("https://hub.example.com", "test-key", file); err == nil {
		_ = a.Close()
		t.Fatal("filesystem error silently disabled spooling")
	}
}
