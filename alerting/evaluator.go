package alerting

import (
	"context"
	"fmt"
	"log"
	"sort"
	"sync"
	"time"

	"Zeus/storage"
)

// evalInterval is how often rules are evaluated. Fast enough to notice a
// problem promptly, slow enough not to hammer the database.
const evalInterval = 30 * time.Second

// lookback is how much recent history each evaluation reads.
const lookback = 15 * time.Minute

// ruleState tracks one rule against one server across evaluations.
type ruleState struct {
	state State
	// breachingSince is when the value first crossed the threshold.
	breachingSince time.Time
	// healthySince is when it first came back under. Used so resolution needs
	// the same sustained period as firing, rather than resolving on one good
	// sample and immediately re-firing on the next bad one.
	healthySince time.Time
	lastAlert    Alert
}

// Evaluator periodically checks rules and dispatches notifications.
type Evaluator struct {
	store     storage.Store
	rules     []Rule
	notifiers []Notifier
	interval  time.Duration

	mu     sync.RWMutex
	states map[string]*ruleState
	history []Alert
}

// New creates an Evaluator. A LogNotifier is always included so an alert is
// never silently discarded for want of a configured sink.
func New(store storage.Store, rules []Rule, notifiers ...Notifier) *Evaluator {
	return &Evaluator{
		store:     store,
		rules:     rules,
		notifiers: append([]Notifier{LogNotifier{}}, notifiers...),
		interval:  evalInterval,
		states:    make(map[string]*ruleState),
	}
}

// SetInterval overrides the evaluation cadence (used by tests).
func (e *Evaluator) SetInterval(d time.Duration) {
	if d > 0 {
		e.interval = d
	}
}

// Run evaluates on a ticker until ctx is cancelled.
func (e *Evaluator) Run(ctx context.Context) {
	enabled := 0
	for _, r := range e.rules {
		if r.IsEnabled() {
			enabled++
		}
	}
	log.Printf("[alerting] started with %d enabled rule(s), evaluating every %s", enabled, e.interval)

	ticker := time.NewTicker(e.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Println("[alerting] stopping")
			return
		case <-ticker.C:
			if err := e.EvaluateOnce(time.Now()); err != nil {
				log.Printf("[alerting] evaluation error: %v", err)
			}
		}
	}
}

// EvaluateOnce runs a single evaluation pass.
func (e *Evaluator) EvaluateOnce(now time.Time) error {
	rows, err := e.store.QueryMetrics(now.Add(-lookback), "")
	if err != nil {
		return fmt.Errorf("query metrics: %w", err)
	}
	if len(rows) == 0 {
		return nil
	}

	// Keep only the newest sample per server. Alerting on an average over the
	// lookback window would blunt exactly the spikes we care about.
	latest := make(map[string]storage.MetricRow)
	for _, row := range rows {
		if cur, ok := latest[row.ServerID]; !ok || row.Timestamp.After(cur.Timestamp) {
			latest[row.ServerID] = row
		}
	}

	for serverID, row := range latest {
		for _, rule := range e.rules {
			if !rule.IsEnabled() {
				continue
			}
			value, ok := extractMetric(rule.Metric, row, now)
			if !ok {
				continue
			}
			e.evaluateRule(rule, serverID, row, value, now)
		}
	}
	return nil
}

// evaluateRule advances the state machine for one rule/server pair.
func (e *Evaluator) evaluateRule(rule Rule, serverID string, row storage.MetricRow, value float64, now time.Time) {
	key := alertKey(rule.Name, serverID)

	e.mu.Lock()
	st, ok := e.states[key]
	if !ok {
		st = &ruleState{state: StateOK}
		e.states[key] = st
	}

	breached := rule.Breached(value)
	var toSend *Alert

	switch {
	case breached && st.state == StateOK:
		// Start the clock. Do not alert yet.
		st.state = StatePending
		st.breachingSince = now
		st.healthySince = time.Time{}

	case breached && st.state == StatePending:
		if now.Sub(st.breachingSince) >= rule.ForDuration() {
			st.state = StateFiring
			alert := buildAlert(rule, serverID, row, value, now, false)
			st.lastAlert = alert
			toSend = &alert
		}

	case breached && st.state == StateFiring:
		// Already firing. Deliberately does not re-notify: repeatedly paging for a
		// known ongoing problem is how alerting channels get muted.
		st.healthySince = time.Time{}

	case !breached && st.state == StatePending:
		// Never fired, so nothing to resolve.
		st.state = StateOK
		st.breachingSince = time.Time{}

	case !breached && st.state == StateFiring:
		if st.healthySince.IsZero() {
			st.healthySince = now
		}
		// Require the same sustained period to resolve as to fire, so a metric
		// oscillating around the threshold does not produce a resolve/fire storm.
		if now.Sub(st.healthySince) >= rule.ForDuration() {
			st.state = StateOK
			st.breachingSince = time.Time{}
			alert := buildAlert(rule, serverID, row, value, now, true)
			st.lastAlert = alert
			toSend = &alert
		}
	}

	if toSend != nil {
		e.history = append(e.history, *toSend)
		// Bound history: this is an in-memory convenience view, not the archive.
		if len(e.history) > 500 {
			e.history = e.history[len(e.history)-500:]
		}
	}
	e.mu.Unlock()

	if toSend != nil {
		fanOut(e.notifiers, *toSend)
	}
}

func buildAlert(rule Rule, serverID string, row storage.MetricRow, value float64, now time.Time, resolved bool) Alert {
	unit := ""
	switch rule.Metric {
	case MetricCPUPct, MetricMemPct, MetricDiskFreePct:
		unit = "%"
	case MetricDiskFreeGB:
		unit = " GB"
	case MetricAgentSilentSec:
		unit = "s"
	}

	msg := fmt.Sprintf("%s is %.1f%s (threshold %s %.1f%s)",
		rule.Metric, value, unit, rule.Comparison, rule.Threshold, unit)
	if resolved {
		msg = fmt.Sprintf("%s recovered to %.1f%s", rule.Metric, value, unit)
	}

	state := StateFiring
	if resolved {
		state = StateOK
	}

	return Alert{
		Rule:      rule.Name,
		ServerID:  serverID,
		TenantID:  row.TenantID,
		Metric:    rule.Metric,
		Severity:  rule.Severity,
		State:     state,
		Value:     value,
		Threshold: rule.Threshold,
		Message:   msg,
		FiredAt:   now,
		Resolved:  resolved,
	}
}

// extractMetric pulls the named value out of a sample.
func extractMetric(name string, row storage.MetricRow, now time.Time) (float64, bool) {
	switch name {
	case MetricCPUPct:
		return row.CPUPct, true
	case MetricMemPct:
		return row.MemPct, true
	case MetricDiskFreeGB:
		return row.DiskFreeGB, true
	case MetricDiskFreePct:
		if row.DiskTotalGB <= 0 {
			// Without a total, a percentage is meaningless. Returning 0 would look
			// like a full disk and fire a false critical alert.
			return 0, false
		}
		return (row.DiskFreeGB / row.DiskTotalGB) * 100, true
	case MetricDiskIOPS:
		return row.DiskIOPS, true
	case MetricNetMBps:
		return row.NetMBps, true
	case MetricUsers:
		return float64(row.ConcurrentUsers), true
	case MetricAgentSilentSec:
		return now.Sub(row.Timestamp).Seconds(), true
	default:
		return 0, false
	}
}

// Active returns the currently firing alerts, most severe first.
func (e *Evaluator) Active() []Alert {
	e.mu.RLock()
	defer e.mu.RUnlock()

	var out []Alert
	for _, st := range e.states {
		if st.state == StateFiring {
			out = append(out, st.lastAlert)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Severity != out[j].Severity {
			return severityRank(out[i].Severity) > severityRank(out[j].Severity)
		}
		return out[i].FiredAt.After(out[j].FiredAt)
	})
	return out
}

// History returns recent alert transitions, newest first.
func (e *Evaluator) History() []Alert {
	e.mu.RLock()
	defer e.mu.RUnlock()

	out := make([]Alert, len(e.history))
	for i, a := range e.history {
		out[len(e.history)-1-i] = a
	}
	return out
}

// Rules returns the configured rules.
func (e *Evaluator) Rules() []Rule {
	return e.rules
}

func severityRank(s Severity) int {
	switch s {
	case SeverityCritical:
		return 3
	case SeverityWarning:
		return 2
	default:
		return 1
	}
}
