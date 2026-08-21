package agent

import (
	"errors"
	"testing"
)

func TestSpoolAppendAndDrain(t *testing.T) {
	sp, err := NewSpool(t.TempDir())
	if err != nil {
		t.Fatalf("NewSpool: %v", err)
	}

	for i := 0; i < 5; i++ {
		if err := sp.Append("metric", []byte(`{"CPUPct":10}`)); err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
	}

	depth, err := sp.Depth()
	if err != nil {
		t.Fatalf("Depth: %v", err)
	}
	if depth != 5 {
		t.Errorf("depth = %d, want 5", depth)
	}

	var got int
	delivered, err := sp.Drain(func(payloadType string, body []byte) error {
		if payloadType != "metric" {
			t.Errorf("payloadType = %q, want metric", payloadType)
		}
		got++
		return nil
	})
	if err != nil {
		t.Fatalf("Drain: %v", err)
	}
	if delivered != 5 || got != 5 {
		t.Errorf("delivered %d (callback saw %d), want 5", delivered, got)
	}

	if depth, _ := sp.Depth(); depth != 0 {
		t.Errorf("depth after full drain = %d, want 0", depth)
	}
}

// The point of the spool: a hub outage must cost latency, not data.
func TestSpoolSurvivesHubOutage(t *testing.T) {
	sp, err := NewSpool(t.TempDir())
	if err != nil {
		t.Fatalf("NewSpool: %v", err)
	}

	for i := 0; i < 3; i++ {
		sp.Append("metric", []byte(`{"CPUPct":1}`))
	}

	// Hub is down: nothing is delivered and nothing is lost.
	delivered, err := sp.Drain(func(string, []byte) error {
		return errors.New("hub unreachable")
	})
	if err == nil {
		t.Fatal("expected the drain to report the delivery failure")
	}
	if delivered != 0 {
		t.Errorf("delivered %d during an outage, want 0", delivered)
	}
	if depth, _ := sp.Depth(); depth != 3 {
		t.Fatalf("depth = %d after a failed drain, want 3 (data was lost)", depth)
	}

	// Hub returns: everything arrives.
	delivered, err = sp.Drain(func(string, []byte) error { return nil })
	if err != nil {
		t.Fatalf("Drain after recovery: %v", err)
	}
	if delivered != 3 {
		t.Errorf("delivered %d after recovery, want 3", delivered)
	}
}

// A failure partway through must not re-deliver what already landed.
func TestSpoolDoesNotDuplicateOnPartialFailure(t *testing.T) {
	sp, err := NewSpool(t.TempDir())
	if err != nil {
		t.Fatalf("NewSpool: %v", err)
	}

	for i := 0; i < 5; i++ {
		sp.Append("metric", []byte(`{"CPUPct":1}`))
	}

	attempt := 0
	sp.Drain(func(string, []byte) error {
		attempt++
		if attempt > 2 {
			return errors.New("hub died mid-drain")
		}
		return nil
	})

	if depth, _ := sp.Depth(); depth != 3 {
		t.Errorf("depth = %d after 2 of 5 delivered, want 3", depth)
	}

	total := 0
	sp.Drain(func(string, []byte) error {
		total++
		return nil
	})
	if total != 3 {
		t.Errorf("redelivered %d entries, want exactly the 3 undelivered ones", total)
	}
}

func TestSpoolPersistsAcrossRestart(t *testing.T) {
	dir := t.TempDir()

	sp1, _ := NewSpool(dir)
	sp1.Append("metric", []byte(`{"CPUPct":42}`))

	// Simulate a process restart by opening the same directory again.
	sp2, err := NewSpool(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if depth, _ := sp2.Depth(); depth != 1 {
		t.Errorf("depth after restart = %d, want 1", depth)
	}
}

func TestSpoolPreservesPayload(t *testing.T) {
	sp, _ := NewSpool(t.TempDir())
	const payload = `{"ServerID":"web-01","CPUPct":93.5}`
	sp.Append("metric", []byte(payload))

	var seen string
	sp.Drain(func(_ string, body []byte) error {
		seen = string(body)
		return nil
	})
	if seen != payload {
		t.Errorf("payload round-trip = %s, want %s", seen, payload)
	}
}

func TestSpoolSizeReporting(t *testing.T) {
	sp, _ := NewSpool(t.TempDir())
	if size, _ := sp.SizeBytes(); size != 0 {
		t.Errorf("empty spool size = %d, want 0", size)
	}
	sp.Append("metric", []byte(`{"CPUPct":1}`))
	if size, _ := sp.SizeBytes(); size == 0 {
		t.Error("spool size still 0 after an append")
	}
}
