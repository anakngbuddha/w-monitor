package alerting

import (
	"testing"
	"time"
)

func TestAlertsToJSONIncludesTenantForScoping(t *testing.T) {
	a := Alert{
		Rule:     "High CPU",
		ServerID: "web-01",
		TenantID: "t_abc",
		Metric:   MetricCPUPct,
		Severity: SeverityWarning,
		Value:    95,
		FiredAt:  time.Now(),
	}

	out := alertsToJSON([]Alert{a})
	if len(out) != 1 {
		t.Fatalf("got %d entries, want 1", len(out))
	}

	// tenant_id must be present or the HTTP layer cannot scope alerts and would
	// leak one client's hostnames to another.
	if out[0]["tenant_id"] != "t_abc" {
		t.Errorf("tenant_id = %v, want t_abc", out[0]["tenant_id"])
	}
	if out[0]["server_id"] != "web-01" {
		t.Errorf("server_id = %v, want web-01", out[0]["server_id"])
	}
	if out[0]["severity"] != "warning" {
		t.Errorf("severity = %v, want warning", out[0]["severity"])
	}
}

func TestEmptyAlertsRenderAsEmptySliceNotNil(t *testing.T) {
	if out := alertsToJSON(nil); out == nil {
		t.Error("want an empty slice so the JSON is [] rather than null")
	}
}

func TestTitleReflectsResolution(t *testing.T) {
	firing := Alert{Rule: "High CPU", ServerID: "web-01", Severity: SeverityCritical}
	if got := firing.Title(); got == "" {
		t.Fatal("empty title")
	}

	resolved := firing
	resolved.Resolved = true
	if firing.Title() == resolved.Title() {
		t.Error("firing and resolved alerts render identically")
	}
}
