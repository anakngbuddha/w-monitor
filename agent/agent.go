// Package agent implements the Zeus agent mode.
//
// In agent mode, the binary collects local system metrics but instead of
// writing to a local database, it POSTs each row as JSON to a central Hub
// over HTTPS, authenticated with a shared API key.
//
// Delivery is durable: a retryable failure is written to a bounded on-disk spool
// and retried with exponential backoff, so a hub outage costs latency rather
// than data.
//
// Agent machines never receive or store Aiven/Postgres credentials.
package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"strconv"
	"sync"
	"time"

	"Zeus/storage"
)

// BuildVersion is set from main via -ldflags so the hub can tell which build a
// client is running.
var BuildVersion = "dev"

// Backoff bounds for the spool drainer.
const (
	minBackoff  = 1 * time.Second
	maxBackoff  = 5 * time.Minute
	postTimeout = 15 * time.Second
)

// retryableError marks a failure worth retrying later.
//
// The distinction is load-bearing: retryable failures get spooled, permanent
// ones do not. Spooling a permanent failure would fill the disk with rows that
// can never be accepted, and because Drain stops at the first failure to
// preserve ordering, a single poison entry would block the whole backlog.
type retryableError struct {
	err        error
	retryAfter time.Duration
}

func (e *retryableError) Error() string { return e.err.Error() }
func (e *retryableError) Unwrap() error { return e.err }

func isRetryable(err error) bool {
	var re *retryableError
	return errors.As(err, &re)
}

// Agent POSTs metric and process rows to a Zeus Hub.
type Agent struct {
	hubURL     string
	apiKey     string
	httpClient *http.Client

	spool     *Spool
	drainOnce sync.Once
	drainWake chan struct{}
}

// New creates an Agent targeting hubURL (e.g. "https://hub.example.com:8080").
// apiKey is sent in the X-API-Key request header.
//
// If a spool directory is available, delivery becomes durable and a background
// drainer can be started with StartDrainer. If it is not, the agent still runs
// but drops rows on failure, exactly as it did before, and says so.
func New(hubURL, apiKey string) *Agent {
	a := &Agent{
		hubURL: hubURL,
		apiKey: apiKey,
		httpClient: &http.Client{
			Timeout: postTimeout,
		},
		drainWake: make(chan struct{}, 1),
	}

	if dir, err := storage.DataDir(); err == nil {
		if sp, err := NewSpool(dir); err == nil {
			a.spool = sp
			if depth, err := sp.Depth(); err == nil && depth > 0 {
				log.Printf("[agent] %d spooled samples pending from a previous run", depth)
			}
		} else {
			log.Printf("[agent] WARNING: could not open spool (%v) — samples will be dropped if the hub is unreachable", err)
		}
	} else {
		log.Printf("[agent] WARNING: no data directory (%v) — samples will be dropped if the hub is unreachable", err)
	}

	return a
}

// StartDrainer launches the background retry loop. Safe to call more than once.
func (a *Agent) StartDrainer(ctx context.Context) {
	if a.spool == nil {
		return
	}
	a.drainOnce.Do(func() { go a.drainLoop(ctx) })
}

// SpoolDepth reports how many samples are waiting for delivery.
func (a *Agent) SpoolDepth() int {
	if a.spool == nil {
		return 0
	}
	depth, err := a.spool.Depth()
	if err != nil {
		return 0
	}
	return depth
}

// drainLoop retries spooled samples with exponential backoff and jitter.
//
// Jitter matters at scale: without it, a fleet of agents that all failed during
// the same hub outage would retry in lockstep and stampede the hub the moment it
// came back, knocking it over again.
func (a *Agent) drainLoop(ctx context.Context) {
	backoff := minBackoff

	for {
		if depth, err := a.spool.Depth(); err == nil && depth > 0 {
			delivered, drainErr := a.spool.Drain(a.deliverOrDrop)
			if delivered > 0 {
				log.Printf("[agent] delivered %d spooled sample(s)", delivered)
			}
			if drainErr == nil {
				backoff = minBackoff
			} else {
				var re *retryableError
				if errors.As(drainErr, &re) && re.retryAfter > 0 {
					// The hub told us how long to wait.
					backoff = re.retryAfter
				} else {
					backoff *= 2
				}
				if backoff > maxBackoff {
					backoff = maxBackoff
				}
				log.Printf("[agent] spool drain failed (%v); retrying in %s", drainErr, backoff.Round(time.Second))
			}
		}

		wait := backoff
		if jitter := backoff / 4; jitter > 0 {
			wait = backoff - jitter + time.Duration(rand.Int63n(int64(2*jitter)))
		}

		select {
		case <-ctx.Done():
			return
		case <-a.drainWake:
		case <-time.After(wait):
		}
	}
}

// deliverOrDrop delivers a spooled entry, discarding it if the hub rejects it
// permanently.
//
// Returning nil for a permanent rejection looks odd but is correct here:
// Drain halts at the first error to preserve ordering, so returning the error
// would park the queue behind an entry that can never succeed.
func (a *Agent) deliverOrDrop(payloadType string, body []byte) error {
	err := a.deliver(payloadType, body)
	if err == nil || isRetryable(err) {
		return err
	}
	log.Printf("[agent] discarding spooled %s: hub rejected it permanently (%v)", payloadType, err)
	return nil
}

func (a *Agent) wakeDrainer() {
	select {
	case a.drainWake <- struct{}{}:
	default: // already pending
	}
}

// InsertMetric POSTs a MetricRow to the Hub's /api/ingest?type=metric endpoint.
// Implements storage.Store so Agent can be used as the collector's store.
func (a *Agent) InsertMetric(m storage.MetricRow) error {
	return a.enqueue("metric", m)
}

// InsertProcess POSTs a ProcessRow to the Hub's /api/ingest?type=process endpoint.
func (a *Agent) InsertProcess(p storage.ProcessRow) error {
	return a.enqueue("process", p)
}

// QueryMetrics is not supported in agent mode — agents are write-only.
func (a *Agent) QueryMetrics(since time.Time, tenantID string) ([]storage.MetricRow, error) {
	return nil, fmt.Errorf("agent: QueryMetrics not supported in agent mode")
}

// QueryProcesses is not supported in agent mode.
func (a *Agent) QueryProcesses(since time.Time, tenantID string) ([]storage.ProcessRow, error) {
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
func (a *Agent) QueryServers(tenantID string) ([]string, error) {
	return nil, fmt.Errorf("agent: QueryServers not supported in agent mode")
}

// Close reports any undelivered backlog and releases resources.
func (a *Agent) Close() error {
	if a.spool != nil {
		if depth, err := a.spool.Depth(); err == nil && depth > 0 {
			log.Printf("[agent] %d sample(s) remain spooled; they will be sent on next start", depth)
		}
	}
	return nil
}

// enqueue attempts immediate delivery, spooling the payload on a retryable
// failure.
//
// A retryable failure returns nil: the sample is safely queued, and reporting an
// error would make the collector log a loss that did not happen. A permanent
// failure is returned, because the operator needs to know their key is wrong.
func (a *Agent) enqueue(payloadType string, v interface{}) error {
	body, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("agent: marshal %s: %w", payloadType, err)
	}

	// With a backlog present, append instead of jumping the queue: delivering the
	// newest sample first would reorder the client's history.
	if a.spool != nil {
		if depth, err := a.spool.Depth(); err == nil && depth > 0 {
			a.wakeDrainer()
			return a.spool.Append(payloadType, body)
		}
	}

	deliverErr := a.deliver(payloadType, body)
	if deliverErr == nil {
		return nil
	}

	if !isRetryable(deliverErr) {
		// Bad key or malformed payload. Retrying cannot help, and spooling would
		// fill the disk with rows the hub will never accept.
		return deliverErr
	}

	if a.spool == nil {
		return fmt.Errorf("agent: %s dropped (no spool available): %w", payloadType, deliverErr)
	}
	if appendErr := a.spool.Append(payloadType, body); appendErr != nil {
		return fmt.Errorf("agent: %s lost — delivery failed (%v) and spooling failed: %w", payloadType, deliverErr, appendErr)
	}
	log.Printf("[agent] hub unreachable (%v); sample spooled for retry", deliverErr)
	a.wakeDrainer()
	return nil
}

// deliver performs a single POST attempt.
//
// Retry classification is deliberate. The previous implementation retried
// network errors but broke out of its loop on any HTTP error status, so a hub
// restart returning 502 was treated as permanent and the sample was discarded.
// 5xx and 429 are transient; 400 and 401 are not.
func (a *Agent) deliver(payloadType string, body []byte) error {
	url := fmt.Sprintf("%s/api/ingest?type=%s", a.hubURL, payloadType)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("agent: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-Key", a.apiKey)
	req.Header.Set("X-Agent-Version", BuildVersion)

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return &retryableError{err: fmt.Errorf("agent: POST %s: %w", payloadType, err)}
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusAccepted || resp.StatusCode == http.StatusOK:
		return nil

	case resp.StatusCode == http.StatusUnauthorized:
		// Permanent: retrying a rejected credential cannot succeed.
		return fmt.Errorf("agent: hub rejected API key — check that this client's key is registered on the hub")

	case resp.StatusCode == http.StatusBadRequest:
		// Permanent: the payload itself is wrong.
		return fmt.Errorf("agent: hub rejected %s payload as malformed", payloadType)

	case resp.StatusCode == http.StatusRequestEntityTooLarge:
		return fmt.Errorf("agent: hub rejected %s payload as too large", payloadType)

	case resp.StatusCode == http.StatusTooManyRequests:
		wait := minBackoff
		if v := resp.Header.Get("Retry-After"); v != "" {
			if secs, err := strconv.Atoi(v); err == nil && secs > 0 {
				wait = time.Duration(secs) * time.Second
			}
		}
		return &retryableError{
			err:        fmt.Errorf("agent: hub rate limited this client"),
			retryAfter: wait,
		}

	case resp.StatusCode >= 500:
		return &retryableError{err: fmt.Errorf("agent: hub returned HTTP %d for %s", resp.StatusCode, payloadType)}

	default:
		return fmt.Errorf("agent: hub returned HTTP %d for %s", resp.StatusCode, payloadType)
	}
}
