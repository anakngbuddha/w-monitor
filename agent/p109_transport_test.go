package agent

import (
	"context"
	"net/http"
	"testing"
	"time"
)

func TestRetryAfterHTTPDateAndLongMinimum(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	if wait := retryAfterDelay("86400", now); wait != 24*time.Hour {
		t.Fatalf("daily retry minimum=%v", wait)
	}
	deadline := now.Add(2 * time.Hour)
	if wait := retryAfterDelay(deadline.Format(http.TimeFormat), now); wait != 2*time.Hour {
		t.Fatalf("HTTP-date retry=%v", wait)
	}
}

func TestCancelledBatchDrainKeepsAllEvidence(t *testing.T) {
	sp, err := NewSpool(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer sp.Close()
	if err := sp.Append("metric", []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := sp.DrainBatches(ctx, 64, func([]spoolEntry) error { t.Fatal("delivery after cancellation"); return nil }); err == nil {
		t.Fatal("cancellation ignored")
	}
	if depth, err := sp.Depth(); err != nil || depth != 1 {
		t.Fatalf("evidence lost: depth=%d err=%v", depth, err)
	}
}
