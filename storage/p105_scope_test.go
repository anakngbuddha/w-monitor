package storage

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestQueryMetricsRejectsEmptyTenant(t *testing.T) {
	db := testDB(t)
	if err := db.InsertMetric(MetricRow{Timestamp: time.Now(), ServerID: "s", CPUPct: 1}); err != nil {
		t.Fatal(err)
	}
	_, err := db.QueryMetrics(time.Time{}, "")
	if !errors.Is(err, ErrTenantRequired) {
		t.Fatalf("got %v, want ErrTenantRequired", err)
	}
	_, err = db.QueryServers("")
	if !errors.Is(err, ErrTenantRequired) {
		t.Fatalf("QueryServers: got %v", err)
	}
}

func TestTwoTenantsSameServerIDAreIsolated(t *testing.T) {
	db := testDB(t)
	now := time.Now()
	if err := db.InsertMetric(MetricRow{Timestamp: now, TenantID: "t_a", ServerID: "web-01", CPUPct: 11, Hostname: "a"}); err != nil {
		t.Fatal(err)
	}
	if err := db.InsertMetric(MetricRow{Timestamp: now, TenantID: "t_b", ServerID: "web-01", CPUPct: 99, Hostname: "b"}); err != nil {
		t.Fatal(err)
	}
	a, err := db.QueryMetrics(now.Add(-time.Minute), "t_a")
	if err != nil {
		t.Fatal(err)
	}
	b, err := db.QueryMetrics(now.Add(-time.Minute), "t_b")
	if err != nil {
		t.Fatal(err)
	}
	if len(a) != 1 || a[0].CPUPct != 11 || a[0].Hostname != "a" {
		t.Fatalf("tenant A leaked: %+v", a)
	}
	if len(b) != 1 || b[0].CPUPct != 99 || b[0].Hostname != "b" {
		t.Fatalf("tenant B leaked: %+v", b)
	}
	sa, _ := db.QueryServers("t_a")
	if len(sa) != 1 || sa[0] != "web-01" {
		t.Fatalf("servers A = %v", sa)
	}
}

func TestQueryMetricsCanceledContext(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "cancel.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = db.QueryMetricsQ(MetricQuery{Ctx: ctx, Since: time.Unix(0, 0), TenantID: LocalTenantID})
	if err == nil {
		t.Fatal("expected canceled query to fail")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want context.Canceled", err)
	}
}

func TestQueryMetricsLimit(t *testing.T) {
	db := testDB(t)
	now := time.Now()
	for i := 0; i < 5; i++ {
		if err := db.InsertMetric(MetricRow{Timestamp: now.Add(time.Duration(i) * time.Second), TenantID: "t_lim", ServerID: "s", CPUPct: float64(i)}); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := db.QueryMetricsQ(MetricQuery{Since: now.Add(-time.Minute), TenantID: "t_lim", Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("limit: got %d rows", len(rows))
	}
}

func TestQueryMetricsCursor(t *testing.T) {
	db := testDB(t)
	now := time.Now().Truncate(time.Second)
	for i := 0; i < 5; i++ {
		if err := db.InsertMetric(MetricRow{Timestamp: now.Add(time.Duration(i) * time.Second), TenantID: "t_cur", ServerID: "s", CPUPct: float64(i)}); err != nil {
			t.Fatal(err)
		}
	}
	first, err := db.QueryMetricsQ(MetricQuery{Since: now.Add(-time.Minute), TenantID: "t_cur", Limit: 2})
	if err != nil || len(first) != 2 {
		t.Fatalf("first page: %v %#v", err, first)
	}
	rest, err := db.QueryMetricsQ(MetricQuery{Since: now.Add(-time.Minute), TenantID: "t_cur", Limit: 10, AfterUnix: first[1].Timestamp.Unix(), AfterID: first[1].ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(rest) != 3 {
		t.Fatalf("remaining: got %d", len(rest))
	}
	if rest[0].ID == first[0].ID || rest[0].ID == first[1].ID {
		t.Fatal("cursor replayed prior rows")
	}
}

func TestQueryServersPage(t *testing.T) {
	db := testDB(t)
	now := time.Now()
	for _, id := range []string{"a", "b", "c"} {
		if err := db.InsertMetric(MetricRow{Timestamp: now, TenantID: "t_srv", ServerID: id, CPUPct: 1}); err != nil {
			t.Fatal(err)
		}
	}
	first, err := db.QueryServersPage(context.Background(), "t_srv", 2, "")
	if err != nil || len(first) != 2 || first[0] != "a" || first[1] != "b" {
		t.Fatalf("first: %v %v", err, first)
	}
	rest, err := db.QueryServersPage(context.Background(), "t_srv", 2, first[1])
	if err != nil || len(rest) != 1 || rest[0] != "c" {
		t.Fatalf("rest: %v %v", err, rest)
	}
}
