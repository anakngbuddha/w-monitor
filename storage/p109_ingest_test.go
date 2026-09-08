package storage

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func ingestFixture(t *testing.T) (*DB, IngestBatch) {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "events.db"))
	if err != nil { t.Fatal(err) }
	t.Cleanup(func() { db.Close() })
	if err := db.InitializeIngest(context.Background()); err != nil { t.Fatal(err) }
	batch := IngestBatch{SchemaVersion: IngestSchema, Events: []IngestEvent{{EventID: "event-1", BootID: "boot-1", Sequence: 1, Metric: &MetricRow{Timestamp: time.Now().UTC(), CPUPct: 20, MemPct: 30}}}}
	return db, batch
}

func TestIngestReplayConflictAndAcceptedBudget(t *testing.T) {
	db, batch := ingestFixture(t)
	policy := DefaultIngestPolicy()
	policy.DailyRows, policy.AgentDailyRows = 1, 1
	ctx := context.Background()
	out, err := db.AcceptIngest(ctx, "tenant-a", "agent-a", batch, policy)
	if err != nil || out[0].Status != "accepted" { t.Fatalf("first: %v %v", out, err) }
	out, err = db.AcceptIngest(ctx, "tenant-a", "agent-a", batch, policy)
	if err != nil || out[0].Status != "duplicate" { t.Fatalf("replay: %v %v", out, err) }
	batch.Events[0].Metric.CPUPct = 21
	if _, err := db.AcceptIngest(ctx, "tenant-a", "agent-a", batch, policy); !errors.Is(err, ErrEventConflict) { t.Fatalf("changed replay: %v", err) }
	batch.Events[0].EventID, batch.Events[0].Sequence = "event-2", 2
	if _, err := db.AcceptIngest(ctx, "tenant-a", "agent-a", batch, policy); !errors.Is(err, ErrAcceptedBudget) { t.Fatalf("budget: %v", err) }
	n, err := db.CountMetrics()
	if err != nil || n != 1 { t.Fatalf("metric count=%d err=%v", n, err) }
}

func TestIngestInvalidBatchMakesNoWrites(t *testing.T) {
	db, batch := ingestFixture(t)
	batch.Events = append(batch.Events, IngestEvent{EventID: "event-2", BootID: "boot-1", Sequence: 2, Metric: &MetricRow{Timestamp: time.Now().Add(time.Hour)}})
	if _, err := db.AcceptIngest(context.Background(), "tenant-a", "agent-a", batch, DefaultIngestPolicy()); err == nil { t.Fatal("future timestamp accepted") }
	n, err := db.CountMetrics()
	if err != nil || n != 0 { t.Fatalf("partial write: %d %v", n, err) }
}

func TestIngestStrictJSON(t *testing.T) {
	for _, raw := range []string{`{"schema_version":"x","schema_version":"y"}`, `{} {}`, `{"unknown":1}`} {
		var batch IngestBatch
		if err := DecodeIngest([]byte(raw), &batch); err == nil { t.Errorf("accepted %s", raw) }
	}
}
