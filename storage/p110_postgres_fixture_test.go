package storage

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// Only the explicitly enabled, local, disposable CI database is used. This test
// never reads a production DSN. The named fixture role is NOT a production role.
func TestPhase1PostgresFixture(t *testing.T) {
	if os.Getenv("WMONITOR_PHASE1_PG_FIXTURE") != "1" { t.Skip("NOT RUN: disposable PostgreSQL fixture not enabled") }
	const dsn = "postgres://phase1:fixture-only@127.0.0.1:5432/phase1_test?sslmode=disable"
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil { t.Fatal("disposable PostgreSQL fixture unavailable") }
	defer admin.Close(context.Background())
	schema := fmt.Sprintf("phase1_%d", time.Now().UnixNano())
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil { t.Fatal(err) }
	defer func() { if _, err := admin.Exec(context.Background(), "DROP SCHEMA "+quoted+" CASCADE"); err != nil { t.Error("fixture cleanup failed") } }()
	first, err := OpenPostgres(dsn+"&search_path="+schema)
	if err != nil { t.Fatal(err) }
	defer first.Close()
	if err := first.InitializeIngest(ctx); err != nil { t.Fatal(err) }
	second, err := OpenPostgres(dsn+"&search_path="+schema)
	if err != nil { t.Fatal(err) }
	defer second.Close()
	if err := second.InitializeIngest(ctx); err != nil { t.Fatal(err) }
	policy := DefaultIngestPolicy()
	policy.DailyRows, policy.AgentDailyRows = 1, 1
	batch := IngestBatch{SchemaVersion: IngestSchema, Events: []IngestEvent{{EventID: "event-1", BootID: "boot-1", Sequence: 1, Metric: &MetricRow{Timestamp: time.Now(), CPUPct: 25, MemPct: 40}}}}
	out, err := first.AcceptIngest(ctx, "tenant-a", "same-agent", batch, policy)
	if err != nil || len(out) != 1 || out[0].Status != "accepted" { t.Fatalf("initial acceptance: %v %v", out, err) }
	out, err = second.AcceptIngest(ctx, "tenant-a", "same-agent", batch, policy)
	if err != nil || len(out) != 1 || out[0].Status != "duplicate" { t.Fatalf("replica lost-ACK replay: %v %v", out, err) }
	batch.Events[0].Metric.CPUPct = 26
	if _, err := second.AcceptIngest(ctx, "tenant-a", "same-agent", batch, policy); !errors.Is(err, ErrEventConflict) { t.Fatalf("changed replay: %v", err) }
	batch.Events[0].EventID, batch.Events[0].Sequence = "event-2", 2
	if _, err := second.AcceptIngest(ctx, "tenant-a", "same-agent", batch, policy); !errors.Is(err, ErrAcceptedBudget) { t.Fatalf("shared budget: %v", err) }
	rows, err := first.QueryMetricsQ(MetricQuery{Ctx: ctx, TenantID: "tenant-a", Since: time.Now().Add(-time.Hour)})
	if err != nil || len(rows) != 1 || rows[0].CPUPct != 25 { t.Fatal("replay or rejected transaction changed evidence") }
	foreign, err := first.QueryMetricsQ(MetricQuery{Ctx: ctx, TenantID: "tenant-b", Since: time.Now().Add(-time.Hour)})
	if err != nil || len(foreign) != 0 { t.Fatal("foreign tenant saw evidence") }
}
