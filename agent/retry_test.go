package agent

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// A hub restart returning 502 must be retried, not discarded. The old code broke
// out of its retry loop on any non-network error and dropped the sample.
func TestServerErrorsAreRetryable(t *testing.T) {
	for _, code := range []int{500, 502, 503, 504} {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(code)
		}))
		a := &Agent{hubURL: ts.URL, apiKey: "k", httpClient: ts.Client()}
		err := a.deliver("metric", []byte(`{}`))
		ts.Close()

		if err == nil {
			t.Errorf("HTTP %d: expected an error", code)
			continue
		}
		if !isRetryable(err) {
			t.Errorf("HTTP %d classified as permanent; a hub restart would lose data", code)
		}
	}
}

// A rejected key must NOT be retried or spooled forever.
func TestUnauthorizedIsPermanent(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer ts.Close()

	a := &Agent{hubURL: ts.URL, apiKey: "wrong", httpClient: ts.Client()}
	err := a.deliver("metric", []byte(`{}`))
	if err == nil {
		t.Fatal("expected an error on 401")
	}
	if isRetryable(err) {
		t.Error("401 classified as retryable; the spool would fill with undeliverable rows")
	}
}

func TestBadRequestIsPermanent(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer ts.Close()

	a := &Agent{hubURL: ts.URL, apiKey: "k", httpClient: ts.Client()}
	err := a.deliver("metric", []byte(`{`))
	if err == nil {
		t.Fatal("expected an error on 400")
	}
	if isRetryable(err) {
		t.Error("400 classified as retryable; a malformed row would be retried forever")
	}
}

func TestRateLimitHonoursRetryAfter(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "42")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer ts.Close()

	a := &Agent{hubURL: ts.URL, apiKey: "k", httpClient: ts.Client()}
	err := a.deliver("metric", []byte(`{}`))
	if err == nil {
		t.Fatal("expected an error on 429")
	}

	var re *retryableError
	if !errors.As(err, &re) {
		t.Fatal("429 must be retryable")
	}
	if re.retryAfter.Seconds() != 42 {
		t.Errorf("retryAfter = %v, want 42s from the Retry-After header", re.retryAfter)
	}
}

// A permanently rejected spooled entry must be dropped so it cannot block the
// rest of the backlog.
func TestDeliverOrDropDiscardsPermanentFailures(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer ts.Close()

	a := &Agent{hubURL: ts.URL, apiKey: "wrong", httpClient: ts.Client()}
	if err := a.deliverOrDrop("metric", []byte(`{}`)); err != nil {
		t.Errorf("deliverOrDrop returned %v; a permanent failure must be dropped so the queue keeps moving", err)
	}
}

func TestDeliverOrDropPropagatesRetryable(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer ts.Close()

	a := &Agent{hubURL: ts.URL, apiKey: "k", httpClient: ts.Client()}
	if err := a.deliverOrDrop("metric", []byte(`{}`)); err == nil {
		t.Error("a retryable failure must stop the drain so ordering is preserved")
	}
}

func TestAgentSendsVersionHeader(t *testing.T) {
	var got string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("X-Agent-Version")
		w.WriteHeader(http.StatusAccepted)
	}))
	defer ts.Close()

	a := &Agent{hubURL: ts.URL, apiKey: "k", httpClient: ts.Client()}
	if err := a.deliver("metric", []byte(`{}`)); err != nil {
		t.Fatalf("deliver: %v", err)
	}
	if got == "" {
		t.Error("X-Agent-Version header was not sent; the hub cannot detect stale agents")
	}
}
