// Package agent implements outbound, durable, authenticated collection.
package agent

import (
	"bytes"
	"context"
	crand "crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"strconv"
	"sync"
	"time"

	"Zeus/internal/fsroot"
	"Zeus/storage"
	"github.com/shirou/gopsutil/v3/host"
)

var BuildVersion = "dev"

const (
	minBackoff = time.Second
	maxBackoff = 5 * time.Minute
	postTimeout = 15 * time.Second
)

type retryableError struct { err error; retryAfter time.Duration }
func (e *retryableError) Error() string { return e.err.Error() }
func (e *retryableError) Unwrap() error { return e.err }
func isRetryable(err error) bool { var re *retryableError; return errors.As(err, &re) }

type Agent struct {
	hubURL string
	apiKey string
	httpClient *http.Client
	spool *Spool
	drainOnce sync.Once
	drainWake chan struct{}
	reauth func() error
	mu sync.Mutex
	ctx context.Context
	cancel context.CancelFunc
	workers sync.WaitGroup
	initErr error
	closed bool
	bound bool
	bootID string
	sequence uint64
}

func (a *Agent) SetReauth(fn func() error) { a.mu.Lock(); a.reauth = fn; a.mu.Unlock() }
func (a *Agent) SetAPIKey(key string) { a.mu.Lock(); a.apiKey = key; a.mu.Unlock() }

func New(hubURL, apiKey string) *Agent {
	dir, err := storage.DataDir()
	if err != nil { a := newAgent(hubURL, apiKey, ""); a.initErr = err; return a }
	return newAgent(hubURL, apiKey, dir)
}

func NewWithSpoolRoot(hubURL, apiKey, spoolRoot string) *Agent {
	if err := fsroot.RejectProductionPath(spoolRoot); err != nil {
		a := newAgent(hubURL, apiKey, "")
		a.initErr = err
		return a
	}
	return newAgent(hubURL, apiKey, spoolRoot)
}

func newAgent(hubURL, apiKey, dataDir string) *Agent {
	ctx, cancel := context.WithCancel(context.Background())
	a := &Agent{hubURL: CanonicalHubURL(hubURL), apiKey: apiKey, httpClient: newHubHTTPClient(postTimeout), drainWake: make(chan struct{}, 1), ctx: ctx, cancel: cancel}
	if err := RequireHTTPSHub(hubURL); err != nil { a.initErr = err; return a }
	if dataDir == "" { a.initErr = errors.New("agent: durable spool is required"); return a }
	sp, err := NewSpool(dataDir)
	if err != nil { a.initErr = err; return a }
	a.spool = sp
	boot, err := host.BootTime()
	if err != nil || boot == 0 { a.initErr = errors.New("agent: OS boot identity unavailable"); return a }
	a.bootID = fmt.Sprintf("boot-%d", boot)
	var seed [8]byte
	if _, err := crand.Read(seed[:]); err != nil { a.initErr = err; return a }
	// Preserve the OS boot identity across process restarts without restarting
	// sequence at one. Event IDs are independently random and durably spooled.
	a.sequence = binary.BigEndian.Uint64(seed[:]) & ((1<<62)-1)
	return a
}

// InitializationError lets service startup fail before a collection loop runs.
func (a *Agent) InitializationError() error { a.mu.Lock(); defer a.mu.Unlock(); return a.initErr }

func (a *Agent) BindIdentity(tenant, server string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.spool == nil || tenant == "" || server == "" { a.initErr = errors.New("agent: complete spool identity is required"); return }
	if err := a.spool.BindDestination(a.hubURL, tenant, server); err != nil { a.initErr = err; return }
	a.bound = true
}

func (a *Agent) StartDrainer(parent context.Context) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed || a.spool == nil || a.initErr != nil || !a.bound { return }
	a.drainOnce.Do(func() {
		a.workers.Add(1)
		go func() { defer a.workers.Done(); a.drainLoop(a.ctx) }()
		if parent != nil {
			a.workers.Add(1)
			go func() { defer a.workers.Done(); select { case <-parent.Done(): a.cancel(); case <-a.ctx.Done(): } }()
		}
	})
}

func (a *Agent) SpoolDepth() int {
	if a.spool == nil { return 0 }
	depth, err := a.spool.Depth()
	if err != nil { return -1 }
	return depth
}

func (a *Agent) drainLoop(ctx context.Context) {
	backoff := minBackoff
	for {
		if ctx.Err() != nil { return }
		_, err := a.spool.DrainBatches(ctx, storage.MaxBatchEvents, a.deliverBatch)
		wait := 250*time.Millisecond
		failed := err != nil
		if failed {
			backoff *= 2
			if backoff > maxBackoff { backoff = maxBackoff }
			wait = backoff
			var retry *retryableError
			if errors.As(err, &retry) && retry.retryAfter > wait { wait = retry.retryAfter }
			// Jitter is nonnegative: never violate the server's minimum wait.
			wait += time.Duration(rand.Int63n(int64(time.Second)))
			log.Print("[agent] delivery blocked; queued evidence retained for retry or operator review")
		} else { backoff = minBackoff }
		timer := time.NewTimer(wait)
		if failed {
			select { case <-ctx.Done(): timer.Stop(); return; case <-timer.C: }
		} else {
			select { case <-ctx.Done(): timer.Stop(); return; case <-timer.C: case <-a.drainWake: timer.Stop() }
		}
	}
}

func (a *Agent) wakeDrainer() { select { case a.drainWake <- struct{}{}: default: } }
func (a *Agent) InsertMetric(m storage.MetricRow) error { m.TenantID = ""; return a.enqueue("metric", m) }
func (a *Agent) InsertProcess(p storage.ProcessRow) error { p.TenantID = ""; return a.enqueue("process", p) }
func (a *Agent) QueryMetrics(time.Time, string) ([]storage.MetricRow, error) { return nil, errors.New("agent: read unsupported") }
func (a *Agent) QueryProcesses(time.Time, string) ([]storage.ProcessRow, error) { return nil, errors.New("agent: read unsupported") }
func (a *Agent) CountMetrics() (int, error) { return 0, errors.New("agent: read unsupported") }
func (a *Agent) CountProcesses() (int, error) { return 0, errors.New("agent: read unsupported") }
func (a *Agent) QueryServers(string) ([]string, error) { return nil, errors.New("agent: read unsupported") }

func (a *Agent) Close() error {
	a.mu.Lock()
	if a.closed { a.mu.Unlock(); return nil }
	a.closed = true
	if a.cancel != nil { a.cancel() }
	a.mu.Unlock()
	a.workers.Wait()
	if a.spool != nil { return a.spool.Close() }
	return nil
}

// enqueue promises success only after a complete event is durably spooled.
// No HTTP call runs on the collection goroutine. When unavailable/full, it
// returns an explicit error instead of falling back to lossy synchronous sends.
func (a *Agent) enqueue(payloadType string, value interface{}) error {
	a.mu.Lock()
	if a.closed { a.mu.Unlock(); return errors.New("agent: closed") }
	if a.initErr != nil { err := a.initErr; a.mu.Unlock(); return err }
	var server string
	event := storage.IngestEvent{BootID: a.bootID}
	switch row := value.(type) {
	case storage.MetricRow: row.TenantID = ""; event.Metric = &row; server = row.ServerID
	case storage.ProcessRow: row.TenantID = ""; event.Process = &row; server = row.ServerID
	default: a.mu.Unlock(); return errors.New("agent: unsupported event type")
	}
	if !a.bound {
		// Explicit-token deployments without enrollment metadata are bound to
		// the credential fingerprint. Changing it quarantines previous backlog
		// rather than assuming that an identical hostname is the same tenant.
		fingerprint := sha256.Sum256([]byte(a.apiKey))
		if a.spool == nil || server == "" { a.mu.Unlock(); return errors.New("agent: spool and stable server identity required") }
		if err := a.spool.BindDestination(a.hubURL, "credential:"+hex.EncodeToString(fingerprint[:]), server); err != nil { a.mu.Unlock(); return err }
		a.bound = true
	}
	if a.sequence >= (1<<63)-1 { a.mu.Unlock(); return errors.New("agent: sequence exhausted") }
	a.sequence++
	event.Sequence = a.sequence
	var id [16]byte
	if _, err := crand.Read(id[:]); err != nil { a.mu.Unlock(); return err }
	event.EventID = hex.EncodeToString(id[:])
	body, err := json.Marshal(event)
	if err == nil { err = a.spool.Append("event", body) }
	a.mu.Unlock()
	if err != nil { return err }
	a.StartDrainer(nil)
	a.wakeDrainer()
	return nil
}

func (a *Agent) deliverBatch(entries []spoolEntry) error {
	batch := storage.IngestBatch{SchemaVersion: storage.IngestSchema}
	for _, entry := range entries {
		var event storage.IngestEvent
		if entry.PayloadType == "event" {
			if err := storage.DecodeIngest(entry.Body, &event); err != nil { return errors.New("agent: invalid spooled event") }
		} else {
			// Old queue records acquire a stable legacy identity, identical to
			// the Hub compatibility endpoint, so lost ACKs are deduplicated.
			event.BootID = "legacy"
			var identity string
			switch entry.PayloadType {
			case "metric": event.Metric = &storage.MetricRow{}; if err := storage.DecodeIngest(entry.Body, event.Metric); err != nil { return err }; event.Metric.TenantID = ""; identity = "metric:"+event.Metric.Timestamp.UTC().Format(time.RFC3339Nano)
			case "process": event.Process = &storage.ProcessRow{}; if err := storage.DecodeIngest(entry.Body, event.Process); err != nil { return err }; event.Process.TenantID = ""; identity = fmt.Sprintf("process:%s:%d", event.Process.Timestamp.UTC().Format(time.RFC3339Nano), event.Process.PID)
			default: return errors.New("agent: unsupported spooled event")
			}
			hash := sha256.Sum256([]byte(identity))
			event.EventID = "legacy-"+hex.EncodeToString(hash[:])
			event.Sequence = (binary.BigEndian.Uint64(hash[:8]) & ((1<<63)-1)) | 1
		}
		batch.Events = append(batch.Events, event)
	}
	body, err := json.Marshal(batch)
	if err != nil { return err }
	if len(body) > storage.MaxBatchBytes { return errors.New("agent: batch exceeds byte limit") }
	return a.deliver("batch", body)
}

// Compatibility helper for old fixtures. Production draining never calls this
// lossy function: even a rejected credential leaves the durable queue intact.
func (a *Agent) deliverOrDrop(payloadType string, body []byte) error {
	err := a.deliver(payloadType, body)
	if err == nil || isRetryable(err) { return err }
	return nil
}

func (a *Agent) deliver(payloadType string, body []byte) error {
	ctx := a.ctx
	if ctx == nil { ctx = context.Background() }
	path := "/api/ingest?type="+payloadType
	if payloadType == "batch" { path = "/api/v1/ingest/batches" }
	for attempt := 0; attempt < 2; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.hubURL+path, bytes.NewReader(body))
		if err != nil { return errors.New("agent: invalid destination") }
		a.mu.Lock()
		key, reauth := a.apiKey, a.reauth
		a.mu.Unlock()
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-API-Key", key)
		req.Header.Set("X-Agent-Version", BuildVersion)
		resp, err := a.httpClient.Do(req)
		if err != nil { return &retryableError{err: errors.New("agent: transport failed")} }
		responseBody, readErr := io.ReadAll(io.LimitReader(resp.Body, storage.MaxBatchBytes+1))
		resp.Body.Close()
		if readErr != nil || len(responseBody) > storage.MaxBatchBytes { return &retryableError{err: errors.New("agent: incomplete or oversized acknowledgement")} }
		switch {
		case resp.StatusCode == http.StatusAccepted || resp.StatusCode == http.StatusOK:
			if payloadType != "batch" { return nil }
			var sent storage.IngestBatch
			var ack struct { Status string `json:"status"`; Outcomes []storage.IngestOutcome `json:"outcomes"` }
			if json.Unmarshal(body, &sent) != nil || storage.DecodeIngest(responseBody, &ack) != nil || ack.Status != "accepted" || len(ack.Outcomes) != len(sent.Events) { return &retryableError{err: errors.New("agent: invalid acknowledgement")} }
			for i, outcome := range ack.Outcomes { if outcome.EventID != sent.Events[i].EventID || (outcome.Status != "accepted" && outcome.Status != "duplicate") { return &retryableError{err: errors.New("agent: acknowledgement identity mismatch")} } }
			return nil
		case resp.StatusCode == http.StatusUnauthorized:
			if attempt == 0 && reauth != nil { if err := reauth(); err == nil { continue } }
			return errors.New("agent: credential rejected; queued evidence retained")
		case resp.StatusCode == http.StatusTooManyRequests:
			return &retryableError{err: errors.New("agent: rate or accepted-data budget exceeded"), retryAfter: retryAfterDelay(resp.Header.Get("Retry-After"), time.Now())}
		case resp.StatusCode >= 500:
			return &retryableError{err: fmt.Errorf("agent: hub returned HTTP %d", resp.StatusCode)}
		default:
			return fmt.Errorf("agent: hub rejected event with HTTP %d; operator review required", resp.StatusCode)
		}
	}
	return errors.New("agent: authentication retry limit reached")
}

func retryAfterDelay(raw string, now time.Time) time.Duration {
	if seconds, err := strconv.ParseInt(raw, 10, 64); err == nil && seconds > 0 && seconds <= int64((1<<63-1)/int64(time.Second)) { return time.Duration(seconds)*time.Second }
	if deadline, err := http.ParseTime(raw); err == nil && deadline.After(now) { return deadline.Sub(now) }
	return minBackoff
}
