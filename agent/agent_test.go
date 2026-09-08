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
	if err != nil { t.Fatal(err) }
	t.Cleanup(func() { if err := a.Close(); err != nil { t.Error(err) } })
	return a
}

type receivedRequest struct { payloadType string; key string; body []byte; err error }

func acknowledgeBatch(w http.ResponseWriter, body []byte) {
	var batch storage.IngestBatch
	if err := json.Unmarshal(body, &batch); err != nil { http.Error(w, "invalid fixture batch", 400); return }
	out := make([]storage.IngestOutcome, len(batch.Events))
	for i, event := range batch.Events { out[i] = storage.IngestOutcome{EventID: event.EventID, Status: "accepted"} }
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]interface{}{"status": "accepted", "outcomes": out})
}

func TestAgentIngestMetric(t *testing.T) {
	const key = "fixture-only-agent-token"
	received := make(chan receivedRequest, 4)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, storage.MaxBatchBytes+1))
		received <- receivedRequest{payloadType: r.URL.Path, key: r.Header.Get("X-API-Key"), body: body, err: err}
		acknowledgeBatch(w, body)
	}))
	defer ts.Close()
	a := newAgentFixture(t, ts.URL, key)
	m := storage.MetricRow{Timestamp: time.Now(), ServerID: "srv-001", Hostname: "testhost", CPUPct: 42.5, MemPct: 65, DiskFreeGB: 100}
	if err := a.InsertMetric(m); err != nil { t.Fatal(err) }
	select {
	case got := <-received:
		if got.err != nil || got.payloadType != "/api/v1/ingest/batches" || got.key != key { t.Fatalf("incorrect request: %+v", got) }
		var batch storage.IngestBatch
		if err := json.Unmarshal(got.body, &batch); err != nil { t.Fatal(err) }
		if batch.SchemaVersion != storage.IngestSchema || len(batch.Events) != 1 { t.Fatal("invalid batch envelope") }
		event := batch.Events[0]
		if event.EventID == "" || event.BootID == "" || event.Sequence == 0 || event.Metric == nil || event.Metric.ServerID != m.ServerID || event.Metric.CPUPct != m.CPUPct { t.Fatal("event identity or metric changed") }
	case <-time.After(3*time.Second): t.Fatal("no metric delivered")
	}
}

func TestAgentIngestProcess(t *testing.T) {
	received := make(chan storage.IngestBatch, 4)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(io.LimitReader(r.Body, storage.MaxBatchBytes+1))
		var batch storage.IngestBatch
		json.Unmarshal(body, &batch)
		received <- batch
		acknowledgeBatch(w, body)
	}))
	defer ts.Close()
	a := newAgentFixture(t, ts.URL, "fixture-key")
	p := storage.ProcessRow{Timestamp: time.Now(), ServerID: "srv-001", PID: 1234, Name: "nginx", CPUPct: 5, MemMB: 128}
	if err := a.InsertProcess(p); err != nil { t.Fatal(err) }
	select {
	case batch := <-received: if len(batch.Events) != 1 || batch.Events[0].Process == nil || batch.Events[0].Process.PID != p.PID || batch.Events[0].Process.Name != p.Name { t.Fatal("process batch changed") }
	case <-time.After(3*time.Second): t.Fatal("no process delivered")
	}
}

func TestAgentUnauthorizedRetainsDurableEvidence(t *testing.T) {
	attempted := make(chan struct{}, 4)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { attempted <- struct{}{}; w.WriteHeader(http.StatusUnauthorized) }))
	defer ts.Close()
	a := newAgentFixture(t, ts.URL, "wrong-fixture-key")
	if err := a.InsertMetric(storage.MetricRow{Timestamp: time.Now(), ServerID: "fixture-agent"}); err != nil { t.Fatal("durable enqueue failed:", err) }
	select { case <-attempted: case <-time.After(3*time.Second): t.Fatal("no delivery attempt") }
	if depth := a.SpoolDepth(); depth != 1 { t.Fatalf("401 lost evidence: depth=%d", depth) }
}

func TestAgentExplicitDirectoryPersistsOnlyItsBacklog(t *testing.T) {
	root := t.TempDir()
	canary := filepath.Join(root, "outside-canary")
	if err := os.WriteFile(canary, []byte("unchanged"), 0600); err != nil { t.Fatal(err) }
	dir := filepath.Join(root, "agent")
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) }))
	defer ts.Close()
	a, err := agent.NewWithDataDir(ts.URL, "fixture-key", dir)
	if err != nil { t.Fatal(err) }
	if err := a.InsertMetric(storage.MetricRow{Timestamp: time.Now(), ServerID: "fixture-agent"}); err != nil { a.Close(); t.Fatal(err) }
	if a.SpoolDepth() != 1 { a.Close(); t.Fatal("durable enqueue missing") }
	if err := a.Close(); err != nil { t.Fatal(err) }
	b, err := agent.NewWithDataDir(ts.URL, "fixture-key", dir)
	if err != nil { t.Fatal(err) }
	defer b.Close()
	if b.SpoolDepth() != 1 { t.Fatal("backlog did not survive reopen") }
	got, err := os.ReadFile(canary)
	if err != nil || string(got) != "unchanged" { t.Fatal("outside canary changed") }
}

func TestAgentExplicitDirectoryRejectsFallback(t *testing.T) {
	for _, dir := range []string{"", ".", "relative"} { if a, err := agent.NewWithDataDir("https://hub.example.com", "fixture-key", dir); err == nil { a.Close(); t.Errorf("accepted implicit directory %q", dir) } }
	file := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(file, []byte("canary"), 0600); err != nil { t.Fatal(err) }
	if a, err := agent.NewWithDataDir("https://hub.example.com", "fixture-key", file); err == nil { a.Close(); t.Fatal("filesystem error disabled spooling") }
}
