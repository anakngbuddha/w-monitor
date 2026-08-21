package alerting

import (
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// Comparison is how a measured value is tested against a threshold.
type Comparison string

const (
	GreaterThan Comparison = ">"
	LessThan    Comparison = "<"
)

// Metric names a field on a metric sample.
const (
	MetricCPUPct         = "cpu_pct"
	MetricMemPct         = "mem_pct"
	MetricDiskFreePct    = "disk_free_pct"
	MetricDiskFreeGB     = "disk_free_gb"
	MetricDiskIOPS       = "disk_iops"
	MetricNetMBps        = "net_mbps"
	MetricUsers          = "concurrent_users"
	MetricAgentSilentSec = "agent_silent_seconds"
)

// Rule is one threshold condition.
type Rule struct {
	Name string `json:"name"`
	// Metric is one of the Metric* constants.
	Metric     string     `json:"metric"`
	Comparison Comparison `json:"comparison"`
	Threshold  float64    `json:"threshold"`
	// For is how long the breach must persist before the alert fires.
	// Expressed as a duration string, e.g. "5m".
	For      string   `json:"for"`
	Severity Severity `json:"severity"`
	// Enabled defaults to true; present so a rule can be switched off without
	// deleting it.
	Enabled *bool `json:"enabled,omitempty"`
}

// ForDuration parses For, defaulting to 5 minutes.
//
// A zero-duration rule fires on a single sample, which for a 10s poll interval
// means one transient spike pages someone. The default is deliberately not zero.
func (r Rule) ForDuration() time.Duration {
	if r.For == "" {
		return 5 * time.Minute
	}
	d, err := time.ParseDuration(r.For)
	if err != nil || d < 0 {
		return 5 * time.Minute
	}
	return d
}

// IsEnabled reports whether the rule should be evaluated.
func (r Rule) IsEnabled() bool {
	return r.Enabled == nil || *r.Enabled
}

// Breached reports whether value violates the rule.
func (r Rule) Breached(value float64) bool {
	switch r.Comparison {
	case LessThan:
		return value < r.Threshold
	default: // GreaterThan
		return value > r.Threshold
	}
}

// Validate checks a rule is usable.
func (r Rule) Validate() error {
	if r.Name == "" {
		return fmt.Errorf("rule needs a name")
	}
	switch r.Metric {
	case MetricCPUPct, MetricMemPct, MetricDiskFreePct, MetricDiskFreeGB,
		MetricDiskIOPS, MetricNetMBps, MetricUsers, MetricAgentSilentSec:
	default:
		return fmt.Errorf("rule %q: unknown metric %q", r.Name, r.Metric)
	}
	if r.Comparison != GreaterThan && r.Comparison != LessThan {
		return fmt.Errorf("rule %q: comparison must be > or <", r.Name)
	}
	if r.For != "" {
		if _, err := time.ParseDuration(r.For); err != nil {
			return fmt.Errorf("rule %q: invalid duration %q", r.Name, r.For)
		}
	}
	return nil
}

// DefaultRules is a useful starting set so alerting works with no config.
//
// Thresholds sit at 90% rather than 80%: a server running at 85% memory is often
// perfectly healthy, and an alert that fires on healthy systems is an alert that
// gets ignored.
func DefaultRules() []Rule {
	return []Rule{
		{
			Name:       "High CPU",
			Metric:     MetricCPUPct,
			Comparison: GreaterThan,
			Threshold:  90,
			For:        "5m",
			Severity:   SeverityWarning,
		},
		{
			Name:       "High Memory",
			Metric:     MetricMemPct,
			Comparison: GreaterThan,
			Threshold:  90,
			For:        "5m",
			Severity:   SeverityWarning,
		},
		{
			Name:       "Low Disk Space",
			Metric:     MetricDiskFreePct,
			Comparison: LessThan,
			Threshold:  10,
			For:        "5m",
			Severity:   SeverityWarning,
		},
		{
			Name:       "Critically Low Disk Space",
			Metric:     MetricDiskFreePct,
			Comparison: LessThan,
			Threshold:  5,
			For:        "1m",
			Severity:   SeverityCritical,
		},
		{
			// D3: without this, a crashed agent is indistinguishable from an idle
			// server. 35s is three 10s poll intervals plus slack.
			Name:       "Agent Not Reporting",
			Metric:     MetricAgentSilentSec,
			Comparison: GreaterThan,
			Threshold:  35,
			For:        "1m",
			Severity:   SeverityCritical,
		},
	}
}

// LoadRules reads rules from a JSON file, falling back to defaults when the path
// is empty.
//
// JSON rather than YAML: encoding/json is in the standard library, and an agent
// that runs on client machines should not carry a third-party parser for a
// config file this small.
func LoadRules(path string) ([]Rule, error) {
	if path == "" {
		return DefaultRules(), nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read rules %s: %w", path, err)
	}

	var rules []Rule
	if err := json.Unmarshal(data, &rules); err != nil {
		return nil, fmt.Errorf("parse rules %s: %w", path, err)
	}
	if len(rules) == 0 {
		return nil, fmt.Errorf("rules file %s contains no rules", path)
	}

	for _, r := range rules {
		if err := r.Validate(); err != nil {
			// Fail loudly at startup. A typo silently disabling an alert is worse
			// than refusing to start, because the operator believes they are covered.
			return nil, fmt.Errorf("invalid rules in %s: %w", path, err)
		}
	}
	return rules, nil
}

// WriteDefaultRules writes the default rule set to path as a starting point.
func WriteDefaultRules(path string) error {
	data, err := json.MarshalIndent(DefaultRules(), "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o644)
}
