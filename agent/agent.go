// Package agent implements the Zeus agent mode.
// In agent mode, the binary collects local system metrics but instead of
// writing to a local database, it POSTs each row as JSON to a central Hub
// over HTTPS, authenticated with a shared API key.
//
// Agent machines never receive or store Aiven/Postgres credentials.
package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"Zeus/storage"
)

// Agent POSTs metric and process rows to a Zeus Hub.
type Agent struct {
	hubURL     string
	apiKey     string
	httpClient *http.Client
}

// New creates an Agent targeting hubURL (e.g. "https://hub.example.com:8080").
// apiKey is sent in the X-API-Key request header.
func New(hubURL, apiKey string) *Agent {
	return &Agent{
		hubURL: hubURL,
		apiKey: apiKey,
		httpClient: &http.Client{
			Timeout: 15 * time.Second,
		},
	}
}

// InsertMetric POSTs a MetricRow to the Hub's /api/ingest?type=metric endpoint.
// Implements storage.Store so Agent can be used as the collector's store.
func (a *Agent) InsertMetric(m storage.MetricRow) error {
	return a.post("metric", m)
}

// InsertProcess POSTs a ProcessRow to the Hub's /api/ingest?type=process endpoint.
func (a *Agent) InsertProcess(p storage.ProcessRow) error {
	return a.post("process", p)
}

// QueryMetrics is not supported in agent mode — agents are write-only.
func (a *Agent) QueryMetrics(since time.Time) ([]storage.MetricRow, error) {
	return nil, fmt.Errorf("agent: QueryMetrics not supported in agent mode")
}

// QueryProcesses is not supported in agent mode.
func (a *Agent) QueryProcesses(since time.Time) ([]storage.ProcessRow, error) {
	return nil, fmt.Errorf("agent: QueryProcesses not supported in agent mode")
}

// CountMetrics is not supported in agent mode.
func (a *Agent) CountMetrics() (int, error) {
	return 0, fmt.Errorf("agent: CountMetrics not supported in agent mode")
}

// CountProcesses is not supported in agent mode.
func (a *Agent) CountProcesses() (int, error) {
	return 0, fmt.Errorf("agent: CountProcesses not supported in agent mode")
}

// QueryServers is not supported in agent mode.
func (a *Agent) QueryServers() ([]string, error) {
	return nil, fmt.Errorf("agent: QueryServers not supported in agent mode")
}

// Close is a no-op for the agent (no local connection to close).
func (a *Agent) Close() error {
	return nil
}

// post marshals v as JSON and POSTs it to /api/ingest?type=<payloadType>.
// It retries once on network errors.
func (a *Agent) post(payloadType string, v interface{}) error {
	body, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("agent: marshal %s: %w", payloadType, err)
	}

	url := fmt.Sprintf("%s/api/ingest?type=%s", a.hubURL, payloadType)

	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			return fmt.Errorf("agent: build request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-API-Key", a.apiKey)

		resp, err := a.httpClient.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("agent: POST %s: %w", payloadType, err)
			log.Printf("[agent] attempt %d failed: %v — retrying", attempt+1, err)
			time.Sleep(2 * time.Second)
			continue
		}
		resp.Body.Close()

		if resp.StatusCode == http.StatusAccepted || resp.StatusCode == http.StatusOK {
			return nil
		}
		if resp.StatusCode == http.StatusUnauthorized {
			return fmt.Errorf("agent: hub rejected API key — check WMONITOR_API_KEY on both agent and hub")
		}
		lastErr = fmt.Errorf("agent: hub returned HTTP %d for %s", resp.StatusCode, payloadType)
		log.Printf("[agent] attempt %d: %v", attempt+1, lastErr)
		break // non-network error, don't retry
	}
	return lastErr
}
