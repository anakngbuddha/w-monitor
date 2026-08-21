package alerting

import (
	"sync"
	"testing"
	"time"

	"Zeus/storage"
)

// fakeStore serves a fixed set of rows.
type fakeStore struct {
	mu   sync.Mutex
	rows []storage.MetricRow
}

func (f *fakeStore) set(rows ...storage.MetricRow) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rows = rows
}

func (f *fakeStore) QueryMetrics(since time.Time, tenantID string) ([]storage.MetricRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]storage.MetricRow, len(f.rows))
	copy(out, f.rows)
	return out, nil
}

func (f *fakeStore) InsertMetric(storage.MetricRow) error  { return nil }
func (f *fakeStore) InsertProcess(storage.ProcessRow) error { return nil }
func (f *fakeStore) QueryProcesses(time.Time, string) ([]storage.ProcessRow, error) {
	return nil, nil
}
func (f *fakeStore) CountMetrics() (int, error)             { return len(f.rows), nil }
func (f *fakeStore) CountProcesses() (int, error)           { return 0, nil }
func (f *fakeStore) QueryServers(string) ([]string, error)  { return nil, nil }
func (f *fakeStore) Close() error                           { return nil }

// captureNotifier records what was dispatched.
type captureNotifier struct {
	mu   sync.Mutex
	sent []Alert
}

func (c *captureNotifier) Name() string { return "capture" }
func (c *captureNotifier) Send(a Alert) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sent = append(c.sent, a)
	return nil
}
func (c *captureNotifier) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.sent)
}
func (c *captureNotifier) last() Alert {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.sent) == 0 {
		return Alert{}
	}
	return c.sent[len(c.sent)-1]
}

func cpuRule(threshold float64, forDur string) Rule {
	return Rule{
		Name:       "High CPU",
		Metric:     MetricCPUPct,
		Comparison: GreaterThan,
		Threshold:  threshold,
		For:        forDur,
		Severity:   SeverityWarning,
	}
}

func sample(serverID string, cpu float64, ts time.Time) storage.MetricRow {
	return storage.MetricRow{
		Timestamp:   ts,
		ServerID:    serverID,
		CPUPct:      cpu,
		MemPct:      20,
		DiskFreeGB:  500,
		DiskTotalGB: 1000,
	}
}

// A single spike must not fire: that is the difference between an alert someone
// reads and an alert someone mutes.
func TestTransientSpikeDoesNotFire(t *testing.T) {
	store := &fakeStore{}
	cap := &captureNotifier{}
	e := New(store, []Rule{cpuRule(90, "5m")}, cap)

	now := time.Now()
	store.set(sample("web-01", 99, now))
	e.EvaluateOnce(now)

	if cap.count() != 0 {
		t.Errorf("fired on a single sample; want no alert until the breach is sustained")
	}
}

func TestSustainedBreachFires(t *testing.T) {
	store := &fakeStore{}
	cap := &captureNotifier{}
	e := New(store, []Rule{cpuRule(90, "5m")}, cap)

	start := time.Now()
	store.set(sample("web-01", 99, start))
	e.EvaluateOnce(start)

	// Still breaching, but not yet long enough.
	mid := start.Add(3 * time.Minute)
	store.set(sample("web-01", 97, mid))
	e.EvaluateOnce(mid)
	if cap.count() != 0 {
		t.Fatalf("fired after 3m of a 5m rule")
	}

	late := start.Add(6 * time.Minute)
	store.set(sample("web-01", 96, late))
	e.EvaluateOnce(late)

	if cap.count() != 1 {
		t.Fatalf("got %d alerts after a sustained 6m breach, want 1", cap.count())
	}
	got := cap.last()
	if got.Resolved {
		t.Error("first alert should not be marked resolved")
	}
	if got.ServerID != "web-01" {
		t.Errorf("ServerID = %q, want web-01", got.ServerID)
	}
}

// An ongoing problem must not re-notify every 30 seconds.
func TestFiringDoesNotRenotify(t *testing.T) {
	store := &fakeStore{}
	cap := &captureNotifier{}
	e := New(store, []Rule{cpuRule(90, "1m")}, cap)

	start := time.Now()
	store.set(sample("web-01", 99, start))
	e.EvaluateOnce(start)
	e.EvaluateOnce(start.Add(2 * time.Minute)) // fires here

	if cap.count() != 1 {
		t.Fatalf("expected exactly 1 alert, got %d", cap.count())
	}

	for i := 3; i < 10; i++ {
		ts := start.Add(time.Duration(i) * time.Minute)
		store.set(sample("web-01", 99, ts))
		e.EvaluateOnce(ts)
	}

	if cap.count() != 1 {
		t.Errorf("re-notified %d times for one ongoing problem; channels get muted this way", cap.count())
	}
}

func TestResolutionRequiresSustainedRecovery(t *testing.T) {
	store := &fakeStore{}
	cap := &captureNotifier{}
	e := New(store, []Rule{cpuRule(90, "1m")}, cap)

	start := time.Now()
	store.set(sample("web-01", 99, start))
	e.EvaluateOnce(start)
	e.EvaluateOnce(start.Add(2 * time.Minute)) // fires

	// One good sample must not resolve it.
	t3 := start.Add(3 * time.Minute)
	store.set(sample("web-01", 10, t3))
	e.EvaluateOnce(t3)
	if cap.count() != 1 {
		t.Fatalf("resolved on a single good sample (count=%d)", cap.count())
	}

	// Sustained recovery resolves.
	t5 := start.Add(5 * time.Minute)
	store.set(sample("web-01", 10, t5))
	e.EvaluateOnce(t5)

	if cap.count() != 2 {
		t.Fatalf("got %d notifications, want 2 (fire + resolve)", cap.count())
	}
	if !cap.last().Resolved {
		t.Error("second notification should be the resolution")
	}
}

// Flapping around the threshold must not produce a fire/resolve storm.
func TestFlappingDoesNotStorm(t *testing.T) {
	store := &fakeStore{}
	cap := &captureNotifier{}
	e := New(store, []Rule{cpuRule(90, "5m")}, cap)

	start := time.Now()
	for i := 0; i < 20; i++ {
		ts := start.Add(time.Duration(i*30) * time.Second)
		cpu := 95.0
		if i%2 == 0 {
			cpu = 85.0
		}
		store.set(sample("web-01", cpu, ts))
		e.EvaluateOnce(ts)
	}

	if cap.count() > 1 {
		t.Errorf("a flapping metric produced %d notifications", cap.count())
	}
}

// D3: a silent agent must be detected. Previously indistinguishable from idle.
func TestAgentSilenceIsDetected(t *testing.T) {
	store := &fakeStore{}
	cap := &captureNotifier{}
	rule := Rule{
		Name:       "Agent Not Reporting",
		Metric:     MetricAgentSilentSec,
		Comparison: GreaterThan,
		Threshold:  35,
		For:        "1m",
		Severity:   SeverityCritical,
	}
	e := New(store, []Rule{rule}, cap)

	// The newest sample is 10 minutes old: the agent has gone quiet.
	lastSeen := time.Now().Add(-10 * time.Minute)
	store.set(sample("web-01", 5, lastSeen))

	now := time.Now()
	e.EvaluateOnce(now)
	e.EvaluateOnce(now.Add(2 * time.Minute))

	if cap.count() != 1 {
		t.Fatalf("a silent agent produced %d alerts, want 1", cap.count())
	}
	if cap.last().Severity != SeverityCritical {
		t.Errorf("severity = %q, want critical", cap.last().Severity)
	}
}

// Each server needs its own state, or one hot host masks another.
func TestPerServerStateIsIndependent(t *testing.T) {
	store := &fakeStore{}
	cap := &captureNotifier{}
	e := New(store, []Rule{cpuRule(90, "1m")}, cap)

	start := time.Now()
	store.set(
		sample("web-01", 99, start),
		sample("web-02", 10, start),
	)
	e.EvaluateOnce(start)

	later := start.Add(2 * time.Minute)
	store.set(
		sample("web-01", 99, later),
		sample("web-02", 10, later),
	)
	e.EvaluateOnce(later)

	if cap.count() != 1 {
		t.Fatalf("got %d alerts, want 1 (only web-01 is breaching)", cap.count())
	}
	if cap.last().ServerID != "web-01" {
		t.Errorf("alerted on %q, want web-01", cap.last().ServerID)
	}
}

// A missing disk total must not be read as a full disk.
func TestDiskPercentSkippedWithoutTotal(t *testing.T) {
	store := &fakeStore{}
	cap := &captureNotifier{}
	rule := Rule{
		Name:       "Low Disk",
		Metric:     MetricDiskFreePct,
		Comparison: LessThan,
		Threshold:  10,
		For:        "0s",
		Severity:   SeverityCritical,
	}
	e := New(store, []Rule{rule}, cap)

	row := sample("web-01", 5, time.Now())
	row.DiskTotalGB = 0 // unknown
	row.DiskFreeGB = 0
	store.set(row)

	now := time.Now()
	e.EvaluateOnce(now)
	e.EvaluateOnce(now.Add(time.Minute))

	if cap.count() != 0 {
		t.Errorf("fired a false disk alert on a sample with no disk total")
	}
}

func TestActiveListsFiringAlerts(t *testing.T) {
	store := &fakeStore{}
	e := New(store, []Rule{cpuRule(90, "1m")})

	start := time.Now()
	store.set(sample("web-01", 99, start))
	e.EvaluateOnce(start)
	if len(e.Active()) != 0 {
		t.Error("pending alert should not be listed as active")
	}

	e.EvaluateOnce(start.Add(2 * time.Minute))
	if len(e.Active()) != 1 {
		t.Errorf("Active() returned %d, want 1", len(e.Active()))
	}
}

func TestRuleValidation(t *testing.T) {
	bad := []Rule{
		{Metric: MetricCPUPct, Comparison: GreaterThan},
		{Name: "x", Metric: "not_a_metric", Comparison: GreaterThan},
		{Name: "x", Metric: MetricCPUPct, Comparison: "~="},
		{Name: "x", Metric: MetricCPUPct, Comparison: GreaterThan, For: "forever"},
	}
	for i, r := range bad {
		if err := r.Validate(); err == nil {
			t.Errorf("rule %d should have failed validation", i)
		}
	}

	for _, r := range DefaultRules() {
		if err := r.Validate(); err != nil {
			t.Errorf("default rule %q is invalid: %v", r.Name, err)
		}
	}
}

func TestForDurationDefaultsAreSafe(t *testing.T) {
	if got := (Rule{}).ForDuration(); got != 5*time.Minute {
		t.Errorf("empty For = %v, want 5m (never default to firing on one sample)", got)
	}
	if got := (Rule{For: "nonsense"}).ForDuration(); got != 5*time.Minute {
		t.Errorf("invalid For = %v, want the 5m default", got)
	}
}

func TestDisabledRulesAreSkipped(t *testing.T) {
	store := &fakeStore{}
	cap := &captureNotifier{}
	disabled := false
	rule := cpuRule(1, "0s")
	rule.Enabled = &disabled
	e := New(store, []Rule{rule}, cap)

	now := time.Now()
	store.set(sample("web-01", 99, now))
	e.EvaluateOnce(now)
	e.EvaluateOnce(now.Add(time.Minute))

	if cap.count() != 0 {
		t.Error("a disabled rule fired")
	}
}

// A broken sink must not suppress the working ones.
func TestOneFailingNotifierDoesNotBlockOthers(t *testing.T) {
	store := &fakeStore{}
	cap := &captureNotifier{}
	e := New(store, []Rule{cpuRule(90, "0s")}, failingNotifier{}, cap)

	now := time.Now()
	store.set(sample("web-01", 99, now))
	e.EvaluateOnce(now)
	e.EvaluateOnce(now.Add(time.Second))

	if cap.count() == 0 {
		t.Error("a failing notifier prevented the working notifier from receiving the alert")
	}
}

type failingNotifier struct{}

func (failingNotifier) Name() string      { return "broken" }
func (failingNotifier) Send(Alert) error  { return errBroken }

var errBroken = &notifierError{"sink is down"}

type notifierError struct{ msg string }

func (e *notifierError) Error() string { return e.msg }
