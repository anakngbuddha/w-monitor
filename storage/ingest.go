package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

const ingestDDL = `
CREATE TABLE IF NOT EXISTS ingest_receipts (
 tenant_id TEXT NOT NULL, agent_id TEXT NOT NULL, event_id TEXT NOT NULL,
 boot_id TEXT NOT NULL, sequence BIGINT NOT NULL, content_hash TEXT NOT NULL,
 received_at BIGINT NOT NULL, accepted_bytes BIGINT NOT NULL,
 PRIMARY KEY (tenant_id, agent_id, event_id),
 UNIQUE (tenant_id, agent_id, boot_id, sequence)
);
CREATE INDEX IF NOT EXISTS ingest_receipts_received ON ingest_receipts(received_at);
CREATE TABLE IF NOT EXISTS ingest_budgets (
 tenant_id TEXT NOT NULL, actor_id TEXT NOT NULL, day TEXT NOT NULL,
 accepted_rows BIGINT NOT NULL DEFAULT 0, accepted_bytes BIGINT NOT NULL DEFAULT 0,
 PRIMARY KEY (tenant_id, actor_id, day)
);
CREATE INDEX IF NOT EXISTS metrics_scope_time ON metrics(tenant_id, server_id, timestamp);
CREATE INDEX IF NOT EXISTS processes_scope_time ON processes(tenant_id, server_id, timestamp);
`

type ingestTx struct {
	exec func(context.Context, string, ...any) (int64, error)
	scan func(context.Context, string, []any, ...any) error
	commit func(context.Context) error
	rollback func(context.Context) error
}

func (db *DB) beginIngest(ctx context.Context) (ingestTx, error) {
	tx, err := db.conn.BeginTx(ctx, nil)
	if err != nil {
		return ingestTx{}, err
	}
	return ingestTx{
		exec: func(ctx context.Context, q string, args ...any) (int64, error) {
			result, err := tx.ExecContext(ctx, q, args...)
			if err != nil {
				return 0, err
			}
			return result.RowsAffected()
		},
		scan: func(ctx context.Context, q string, args []any, dest ...any) error { return tx.QueryRowContext(ctx, q, args...).Scan(dest...) },
		commit: func(context.Context) error { return tx.Commit() },
		rollback: func(context.Context) error { return tx.Rollback() },
	}, nil
}

func (pg *PostgresDB) beginIngest(ctx context.Context) (ingestTx, error) {
	tx, err := pg.pool.Begin(ctx)
	if err != nil {
		return ingestTx{}, err
	}
	return ingestTx{
		exec: func(ctx context.Context, q string, args ...any) (int64, error) {
			result, err := tx.Exec(ctx, q, args...)
			return result.RowsAffected(), err
		},
		scan: func(ctx context.Context, q string, args []any, dest ...any) error { return tx.QueryRow(ctx, q, args...).Scan(dest...) },
		commit: tx.Commit,
		rollback: tx.Rollback,
	}, nil
}

func initializeIngest(ctx context.Context, tx ingestTx) error {
	defer tx.rollback(ctx)
	for _, statement := range strings.Split(ingestDDL, ";") {
		if strings.TrimSpace(statement) == "" {
			continue
		}
		if _, err := tx.exec(ctx, statement); err != nil {
			return err
		}
	}
	return tx.commit(ctx)
}

// InitializeIngest is an additive, transactional startup migration. PG replicas
// serialize DDL. It is not a replacement for the versioned P2 migration system.
func (db *DB) InitializeIngest(ctx context.Context) error {
	tx, err := db.beginIngest(ctx)
	if err != nil {
		return err
	}
	return initializeIngest(ctx, tx)
}

func (pg *PostgresDB) InitializeIngest(ctx context.Context) error {
	tx, err := pg.beginIngest(ctx)
	if err != nil {
		return err
	}
	if _, err := tx.exec(ctx, "SELECT pg_advisory_xact_lock(7364109201)"); err != nil {
		tx.rollback(ctx)
		return err
	}
	return initializeIngest(ctx, tx)
}

func (db *DB) Ping(ctx context.Context) error { return db.conn.PingContext(ctx) }
func (pg *PostgresDB) Ping(ctx context.Context) error { return pg.pool.Ping(ctx) }

func (db *DB) AcceptIngest(ctx context.Context, tenant, agent string, batch IngestBatch, policy IngestPolicy) ([]IngestOutcome, error) {
	now := time.Now().UTC()
	if err := NormalizeIngest(&batch, tenant, agent, now); err != nil {
		return nil, err
	}
	if err := policy.Validate(); err != nil {
		return nil, err
	}
	tx, err := db.beginIngest(ctx)
	if err != nil {
		return nil, err
	}
	return acceptIngest(ctx, tx, tenant, agent, batch, policy, now)
}

func (pg *PostgresDB) AcceptIngest(ctx context.Context, tenant, agent string, batch IngestBatch, policy IngestPolicy) ([]IngestOutcome, error) {
	now := time.Now().UTC()
	if err := NormalizeIngest(&batch, tenant, agent, now); err != nil {
		return nil, err
	}
	if err := policy.Validate(); err != nil {
		return nil, err
	}
	tx, err := pg.beginIngest(ctx)
	if err != nil {
		return nil, err
	}
	return acceptIngest(ctx, tx, tenant, agent, batch, policy, now)
}

func acceptIngest(ctx context.Context, tx ingestTx, tenant, agent string, batch IngestBatch, policy IngestPolicy, now time.Time) ([]IngestOutcome, error) {
	defer tx.rollback(ctx)
	out := make([]IngestOutcome, 0, len(batch.Events))
	day := now.Format("2006-01-02")
	for _, event := range batch.Events {
		body, err := json.Marshal(event)
		if err != nil {
			return nil, err
		}
		hash := sha256.Sum256(body)
		contentHash := hex.EncodeToString(hash[:])
		n, err := tx.exec(ctx, `INSERT INTO ingest_receipts(tenant_id,agent_id,event_id,boot_id,sequence,content_hash,received_at,accepted_bytes) VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT DO NOTHING`, tenant, agent, event.EventID, event.BootID, int64(event.Sequence), contentHash, now.Unix(), len(body))
		if err != nil {
			return nil, err
		}
		if n == 0 {
			var existing string
			if err := tx.scan(ctx, `SELECT content_hash FROM ingest_receipts WHERE tenant_id=$1 AND agent_id=$2 AND event_id=$3`, []any{tenant, agent, event.EventID}, &existing); err != nil || existing != contentHash {
				return nil, ErrEventConflict
			}
			out = append(out, IngestOutcome{EventID: event.EventID, Status: "duplicate"})
			continue
		}
		// The conditional UPDATE acquires the shared row lock and checks limits
		// in the database, not against a process-local counter. A failed write
		// rolls back both budgets, all receipts and every event in this batch.
		for _, quota := range []struct{ actor string; rows, bytes int64 }{{"", policy.DailyRows, policy.DailyBytes}, {agent, policy.AgentDailyRows, policy.AgentDailyBytes}} {
			if _, err := tx.exec(ctx, `INSERT INTO ingest_budgets(tenant_id,actor_id,day) VALUES($1,$2,$3) ON CONFLICT DO NOTHING`, tenant, quota.actor, day); err != nil {
				return nil, err
			}
			n, err := tx.exec(ctx, `UPDATE ingest_budgets SET accepted_rows=accepted_rows+1, accepted_bytes=accepted_bytes+$1 WHERE tenant_id=$2 AND actor_id=$3 AND day=$4 AND accepted_rows<$5 AND accepted_bytes<=$6-$1`, len(body), tenant, quota.actor, day, quota.rows, quota.bytes)
			if err != nil {
				return nil, err
			}
			if n != 1 {
				return nil, ErrAcceptedBudget
			}
		}
		if m := event.Metric; m != nil {
			_, err = tx.exec(ctx, `INSERT INTO metrics(timestamp,tenant_id,server_id,hostname,cpu_pct,mem_pct,disk_free_gb,net_sent_bytes,net_recv_bytes,cpu_cores,mem_total_gb,disk_total_gb,disk_read_ops,disk_write_ops,disk_iops,net_mbps,concurrent_users,net_sent_external,net_recv_external,net_sent_internal,net_recv_internal) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21)`, m.Timestamp.Unix(), tenant, agent, m.Hostname, m.CPUPct, m.MemPct, m.DiskFreeGB, int64(m.NetSentBytes), int64(m.NetRecvBytes), m.CPUCores, m.MemTotalGB, m.DiskTotalGB, int64(m.DiskReadOps), int64(m.DiskWriteOps), m.DiskIOPS, m.NetMBps, m.ConcurrentUsers, int64(m.NetSentExternal), int64(m.NetRecvExternal), int64(m.NetSentInternal), int64(m.NetRecvInternal))
		} else {
			p := event.Process
			_, err = tx.exec(ctx, `INSERT INTO processes(timestamp,tenant_id,server_id,hostname,pid,name,cpu_pct,mem_mb) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, p.Timestamp.Unix(), tenant, agent, p.Hostname, p.PID, p.Name, p.CPUPct, p.MemMB)
		}
		if err != nil {
			return nil, fmt.Errorf("ingest: insert failed: %w", err)
		}
		out = append(out, IngestOutcome{EventID: event.EventID, Status: "accepted"})
	}
	if err := tx.commit(ctx); err != nil {
		return nil, err
	}
	return out, nil
}

// CleanupIngest retains dedup tombstones longer than the maximum accepted event
// age. Old events are rejected by NormalizeIngest, so cleanup cannot resurrect
// an accepted event. Run with a timeout; metric/evidence retention is untouched.
func (db *DB) CleanupIngest(ctx context.Context) error {
	return cleanupIngest(ctx, func(ctx context.Context, q string, args ...any) error { _, err := db.conn.ExecContext(ctx, q, args...); return err })
}

func (pg *PostgresDB) CleanupIngest(ctx context.Context) error {
	return cleanupIngest(ctx, func(ctx context.Context, q string, args ...any) error { _, err := pg.pool.Exec(ctx, q, args...); return err })
}

func cleanupIngest(ctx context.Context, exec func(context.Context, string, ...any) error) error {
	cutoff := time.Now().UTC().Add(-MaxEventAge - 48*time.Hour)
	if err := exec(ctx, `DELETE FROM ingest_receipts WHERE received_at<$1`, cutoff.Unix()); err != nil {
		return err
	}
	return exec(ctx, `DELETE FROM ingest_budgets WHERE day<$1`, cutoff.Format("2006-01-02"))
}
