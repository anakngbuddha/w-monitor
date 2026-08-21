// Package alerting evaluates threshold rules against collected metrics and
// dispatches notifications.
//
// Design note: alerts fire only after a breach has been sustained for a
// configured duration, and resolve only after an equal period below the
// threshold. A monitor that fires the instant a metric crosses a line produces
// flapping alerts, and flapping alerts get muted, which leaves the system
// unmonitored while appearing to be monitored.
package alerting

import (
	"fmt"
	"strings"
	"time"
)

// Severity ranks an alert.
type Severity string

const (
	SeverityInfo     Severity = "info"
	SeverityWarning  Severity = "warning"
	SeverityCritical Severity = "critical"
)

// State is where a rule currently sits for a given server.
type State string

const (
	StateOK      State = "ok"
	StatePending State = "pending" // breaching, but not yet for long enough
	StateFiring  State = "firing"
)

// Alert is a single alert occurrence.
type Alert struct {
	Rule      string    `json:"rule"`
	ServerID  string    `json:"server_id"`
	TenantID  string    `json:"tenant_id,omitempty"`
	Metric    string    `json:"metric"`
	Severity  Severity  `json:"severity"`
	State     State     `json:"state"`
	Value     float64   `json:"value"`
	Threshold float64   `json:"threshold"`
	Message   string    `json:"message"`
	FiredAt   time.Time `json:"fired_at"`
	Resolved  bool      `json:"resolved"`
}

// Title is a short one-line summary.
func (a Alert) Title() string {
	verb := "FIRING"
	if a.Resolved {
		verb = "RESOLVED"
	}
	server := a.ServerID
	if server == "" {
		server = "local"
	}
	return fmt.Sprintf("[%s] %s on %s: %s", verb, strings.ToUpper(string(a.Severity)), server, a.Rule)
}

// key identifies a rule/server pair for state tracking.
func alertKey(rule, serverID string) string {
	return rule + "\x00" + serverID
}
