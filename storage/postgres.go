// Package storage — Postgres backend implementing the Store interface.
// Uses jackc/pgx/v5 (pure Go, no CGO, actively maintained).
package storage

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// PostgresDB wraps a pgx connection pool and satisfies storage.Store.
type PostgresDB struct {
	pool *pgxpool.Pool
}

// OpenPostgres opens a Postgres connection pool using the given DSN and runs migrations.
// DSN format: "postgres://user:password@host:port/dbname?sslmode=require"
func OpenPostgres(dsn string) (*PostgresDB, error) {
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		return nil, fmt.Errorf("open postgres: %w", err)
	}
	// Test connectivity
	if err := pool.Ping(context.Background()); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}

	pg := &PostgresDB{pool: pool}
	if err := pg.migrate(); err != nil {
		pool.Close()
		return nil, fmt.Errorf("migrate postgres: %w", err)
	}
	return pg, nil
}

// Close releases all pool connections.
func (pg *PostgresDB) Close() error {
	pg.pool.Close()
	return nil
}

// migrate creates or updates the Postgres schema.
func (pg *PostgresDB) migrate() error {
	tables := `
CREATE TABLE IF NOT EXISTS metrics (
    id                  SERIAL PRIMARY KEY,
    timestamp           BIGINT  NOT NULL,
    tenant_id           TEXT    NOT NULL DEFAULT '',
    server_id           TEXT    NOT NULL DEFAULT '',
    hostname            TEXT    NOT NULL DEFAULT '',
    cpu_pct             DOUBLE PRECISION NOT NULL,
    mem_pct             DOUBLE PRECISION NOT NULL,
    disk_free_gb        DOUBLE PRECISION NOT NULL,
    net_sent_bytes      BIGINT  NOT NULL DEFAULT 0,
    net_recv_bytes      BIGINT  NOT NULL DEFAULT 0,
    cpu_cores           INT     NOT NULL DEFAULT 0,
    mem_total_gb        DOUBLE PRECISION NOT NULL DEFAULT 0,
    disk_total_gb       DOUBLE PRECISION NOT NULL DEFAULT 0,
    disk_read_ops       BIGINT  NOT NULL DEFAULT 0,
    disk_write_ops      BIGINT  NOT NULL DEFAULT 0,
    disk_iops           DOUBLE PRECISION NOT NULL DEFAULT 0,
    net_mbps            DOUBLE PRECISION NOT NULL DEFAULT 0,
    concurrent_users    INT     NOT NULL DEFAULT 0,
    net_sent_external   BIGINT  NOT NULL DEFAULT 0,
    net_recv_external   BIGINT  NOT NULL DEFAULT 0,
    net_sent_internal   BIGINT  NOT NULL DEFAULT 0,
    net_recv_internal   BIGINT  NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS processes (
    id        SERIAL PRIMARY KEY,
    timestamp BIGINT NOT NULL,
    tenant_id TEXT   NOT NULL DEFAULT '',
    server_id TEXT   NOT NULL DEFAULT '',
    hostname  TEXT   NOT NULL DEFAULT '',
    pid       INT    NOT NULL,
    name      TEXT   NOT NULL,
    cpu_pct   DOUBLE PRECISION NOT NULL,
    mem_mb    DOUBLE PRECISION NOT NULL
);

CREATE TABLE IF NOT EXISTS audit_events (
    id           BIGSERIAL PRIMARY KEY,
    ts           BIGINT NOT NULL,
    tenant_id    TEXT NOT NULL,
    actor_kind   TEXT NOT NULL,
    actor_prefix TEXT NOT NULL,
    action       TEXT NOT NULL,
    target_type  TEXT NOT NULL DEFAULT '',
    target_id    TEXT NOT NULL DEFAULT ''
);
`
	ctx := context.Background()
	if _, err := pg.pool.Exec(ctx, tables); err != nil {
		return err
	}

	// Add columns if they don't exist yet (safe ALTER TABLE pattern matching SQLite)
	alters := []string{
		"ALTER TABLE metrics ADD COLUMN IF NOT EXISTS tenant_id TEXT NOT NULL DEFAULT ''",
		"ALTER TABLE metrics ADD COLUMN IF NOT EXISTS server_id TEXT NOT NULL DEFAULT ''",
		"ALTER TABLE metrics ADD COLUMN IF NOT EXISTS hostname TEXT NOT NULL DEFAULT ''",
		"ALTER TABLE metrics ADD COLUMN IF NOT EXISTS cpu_cores INT NOT NULL DEFAULT 0",
		"ALTER TABLE metrics ADD COLUMN IF NOT EXISTS mem_total_gb DOUBLE PRECISION NOT NULL DEFAULT 0",
		"ALTER TABLE metrics ADD COLUMN IF NOT EXISTS disk_total_gb DOUBLE PRECISION NOT NULL DEFAULT 0",
		"ALTER TABLE metrics ADD COLUMN IF NOT EXISTS disk_read_ops BIGINT NOT NULL DEFAULT 0",
		"ALTER TABLE metrics ADD COLUMN IF NOT EXISTS disk_write_ops BIGINT NOT NULL DEFAULT 0",
		"ALTER TABLE metrics ADD COLUMN IF NOT EXISTS disk_iops DOUBLE PRECISION NOT NULL DEFAULT 0",
		"ALTER TABLE metrics ADD COLUMN IF NOT EXISTS net_mbps DOUBLE PRECISION NOT NULL DEFAULT 0",
		"ALTER TABLE metrics ADD COLUMN IF NOT EXISTS concurrent_users INT NOT NULL DEFAULT 0",
		"ALTER TABLE metrics ADD COLUMN IF NOT EXISTS net_sent_external BIGINT NOT NULL DEFAULT 0",
		"ALTER TABLE metrics ADD COLUMN IF NOT EXISTS net_recv_external BIGINT NOT NULL DEFAULT 0",
		"ALTER TABLE metrics ADD COLUMN IF NOT EXISTS net_sent_internal BIGINT NOT NULL DEFAULT 0",
		"ALTER TABLE metrics ADD COLUMN IF NOT EXISTS net_recv_internal BIGINT NOT NULL DEFAULT 0",
		"ALTER TABLE processes ADD COLUMN IF NOT EXISTS tenant_id TEXT NOT NULL DEFAULT ''",
		"ALTER TABLE processes ADD COLUMN IF NOT EXISTS server_id TEXT NOT NULL DEFAULT ''",
		"ALTER TABLE processes ADD COLUMN IF NOT EXISTS hostname TEXT NOT NULL DEFAULT ''",
	}
	for _, alter := range alters {
		pg.pool.Exec(ctx, alter) // intentionally ignore errors (column may already exist)
	}

	indexes := `
CREATE INDEX IF NOT EXISTS idx_metrics_ts ON metrics(timestamp);
CREATE INDEX IF NOT EXISTS idx_metrics_server ON metrics(server_id);
CREATE INDEX IF NOT EXISTS idx_metrics_tenant ON metrics(tenant_id);
CREATE INDEX IF NOT EXISTS idx_processes_ts ON processes(timestamp);
CREATE INDEX IF NOT EXISTS idx_audit_tenant_ts ON audit_events(tenant_id, ts);
`
	if _, err := pg.pool.Exec(ctx, indexes); err != nil {
		return err
	}

	return nil
}

// InsertMetric writes one metrics row. Empty TenantID becomes LocalTenantID.
func (pg *PostgresDB) InsertMetric(m MetricRow) error {
	m.TenantID = normalizeInsertTenant(m.TenantID)
	_, err := pg.pool.Exec(context.Background(),
		`INSERT INTO metrics(timestamp, tenant_id, server_id, hostname, cpu_pct, mem_pct, disk_free_gb, net_sent_bytes, net_recv_bytes, cpu_cores, mem_total_gb, disk_total_gb, disk_read_ops, disk_write_ops, disk_iops, net_mbps, concurrent_users, net_sent_external, net_recv_external, net_sent_internal, net_recv_internal)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21)`,
		m.Timestamp.Unix(), m.TenantID, m.ServerID, m.Hostname, m.CPUPct, m.MemPct, m.DiskFreeGB,
		int64(m.NetSentBytes), int64(m.NetRecvBytes),
		m.CPUCores, m.MemTotalGB, m.DiskTotalGB,
		int64(m.DiskReadOps), int64(m.DiskWriteOps),
		m.DiskIOPS, m.NetMBps, m.ConcurrentUsers,
		int64(m.NetSentExternal), int64(m.NetRecvExternal),
		int64(m.NetSentInternal), int64(m.NetRecvInternal),
	)
	return err
}

// InsertProcess writes one process row. Empty TenantID becomes LocalTenantID.
func (pg *PostgresDB) InsertProcess(p ProcessRow) error {
	p.TenantID = normalizeInsertTenant(p.TenantID)
	_, err := pg.pool.Exec(context.Background(),
		`INSERT INTO processes(timestamp, tenant_id, server_id, hostname, pid, name, cpu_pct, mem_mb)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
		p.Timestamp.Unix(), p.TenantID, p.ServerID, p.Hostname, p.PID, p.Name, p.CPUPct, p.MemMB,
	)
	return err
}

func (pg *PostgresDB) QueryMetrics(since time.Time, tenantID string) ([]MetricRow, error) {
	return pg.QueryMetricsQ(MetricQuery{Since: since, TenantID: tenantID})
}

func (pg *PostgresDB) QueryMetricsAllTenants(ctx context.Context, since time.Time, limit int) ([]MetricRow, error) {
	return pg.queryMetrics(queryContext(ctx), since, time.Time{}, "", "", queryLimit(limit), true, 0, 0)
}

func (pg *PostgresDB) QueryMetricsQ(q MetricQuery) ([]MetricRow, error) {
	if err := RequireTenant(q.TenantID); err != nil {
		return nil, err
	}
	return pg.queryMetrics(queryContext(q.Ctx), q.Since, q.Until, q.TenantID, q.ServerID, queryLimit(q.Limit), false, q.AfterUnix, q.AfterID)
}

func (pg *PostgresDB) queryMetrics(ctx context.Context, since, until time.Time, tenantID, serverID string, limit int, allTenants bool, afterUnix, afterID int64) ([]MetricRow, error) {
	query := `SELECT id, timestamp, tenant_id, server_id, hostname, cpu_pct, mem_pct, disk_free_gb, net_sent_bytes, net_recv_bytes, cpu_cores, mem_total_gb, disk_total_gb, disk_read_ops, disk_write_ops, disk_iops, net_mbps, concurrent_users, net_sent_external, net_recv_external, net_sent_internal, net_recv_internal
		 FROM metrics WHERE timestamp >= $1`
	args := []interface{}{since.Unix()}
	n := 2
	if !until.IsZero() {
		query += fmt.Sprintf(` AND timestamp <= $%d`, n)
		args = append(args, until.Unix())
		n++
	}
	if !allTenants {
		query += fmt.Sprintf(` AND tenant_id = $%d`, n)
		args = append(args, tenantID)
		n++
	}
	if serverID != "" {
		query += fmt.Sprintf(` AND server_id = $%d`, n)
		args = append(args, serverID)
		n++
	}
	if afterID > 0 {
		query += fmt.Sprintf(` AND (timestamp > $%d OR (timestamp = $%d AND id > $%d))`, n, n+1, n+2)
		args = append(args, afterUnix, afterUnix, afterID)
		n += 3
	}
	query += fmt.Sprintf(` ORDER BY timestamp ASC, id ASC LIMIT $%d`, n)
	args = append(args, limit)

	rows, err := pg.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []MetricRow
	for rows.Next() {
		var r MetricRow
		var ts int64
		var netSent, netRecv, diskRead, diskWrite int64
		var netSentExt, netRecvExt, netSentInt, netRecvInt int64
		if err := rows.Scan(
			&r.ID, &ts, &r.TenantID, &r.ServerID, &r.Hostname,
			&r.CPUPct, &r.MemPct, &r.DiskFreeGB,
			&netSent, &netRecv,
			&r.CPUCores, &r.MemTotalGB, &r.DiskTotalGB,
			&diskRead, &diskWrite,
			&r.DiskIOPS, &r.NetMBps, &r.ConcurrentUsers,
			&netSentExt, &netRecvExt, &netSentInt, &netRecvInt,
		); err != nil {
			return nil, err
		}
		r.Timestamp = time.Unix(ts, 0)
		r.NetSentBytes = uint64(netSent)
		r.NetRecvBytes = uint64(netRecv)
		r.DiskReadOps = uint64(diskRead)
		r.DiskWriteOps = uint64(diskWrite)
		r.NetSentExternal = uint64(netSentExt)
		r.NetRecvExternal = uint64(netRecvExt)
		r.NetSentInternal = uint64(netSentInt)
		r.NetRecvInternal = uint64(netRecvInt)
		result = append(result, r)
	}
	return result, rows.Err()
}

func (pg *PostgresDB) QueryProcesses(since time.Time, tenantID string) ([]ProcessRow, error) {
	if err := RequireTenant(tenantID); err != nil {
		return nil, err
	}
	rows, err := pg.pool.Query(context.Background(),
		`SELECT id, timestamp, tenant_id, server_id, hostname, pid, name, cpu_pct, mem_mb
		 FROM processes WHERE timestamp >= $1 AND tenant_id = $2 ORDER BY timestamp ASC LIMIT $3`,
		since.Unix(), tenantID, DefaultQueryLimit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []ProcessRow
	for rows.Next() {
		var p ProcessRow
		var ts int64
		if err := rows.Scan(&p.ID, &ts, &p.TenantID, &p.ServerID, &p.Hostname, &p.PID, &p.Name, &p.CPUPct, &p.MemMB); err != nil {
			return nil, err
		}
		p.Timestamp = time.Unix(ts, 0)
		result = append(result, p)
	}
	return result, rows.Err()
}

// CountMetrics returns the total number of metric rows.
func (pg *PostgresDB) CountMetrics() (int, error) {
	var count int
	err := pg.pool.QueryRow(context.Background(), "SELECT COUNT(*) FROM metrics").Scan(&count)
	return count, err
}

// CountProcesses returns the total number of process rows.
func (pg *PostgresDB) CountProcesses() (int, error) {
	var count int
	err := pg.pool.QueryRow(context.Background(), "SELECT COUNT(*) FROM processes").Scan(&count)
	return count, err
}

// QueryServers returns distinct server_id values seen in the metrics table.
// If tenantID is non-empty, only servers for that tenant are returned.
func (pg *PostgresDB) QueryServers(tenantID string) ([]string, error) {
	return pg.QueryServersContext(context.Background(), tenantID, DefaultQueryLimit)
}

// PurgeOld deletes metric and process rows older than cutoff timestamp.
func (pg *PostgresDB) PurgeOld(cutoff time.Time) (int64, int64, error) {
	ts := cutoff.Unix()
	tag1, err := pg.pool.Exec(context.Background(), "DELETE FROM metrics WHERE timestamp < $1", ts)
	if err != nil {
		return 0, 0, fmt.Errorf("purge postgres metrics: %w", err)
	}
	tag2, err := pg.pool.Exec(context.Background(), "DELETE FROM processes WHERE timestamp < $1", ts)
	if err != nil {
		return 0, 0, fmt.Errorf("purge postgres processes: %w", err)
	}
	return tag1.RowsAffected(), tag2.RowsAffected(), nil
}

// MigrateTenantID reassigns all metric and process rows from oldTenant to newTenant.
func (pg *PostgresDB) MigrateTenantID(oldTenant, newTenant string) (int64, error) {
	tag1, err := pg.pool.Exec(context.Background(), "UPDATE metrics SET tenant_id = $1 WHERE tenant_id = $2", newTenant, oldTenant)
	if err != nil {
		return 0, fmt.Errorf("migrate postgres metrics tenant: %w", err)
	}
	tag2, err := pg.pool.Exec(context.Background(), "UPDATE processes SET tenant_id = $1 WHERE tenant_id = $2", newTenant, oldTenant)
	if err != nil {
		return 0, fmt.Errorf("migrate postgres processes tenant: %w", err)
	}
	return tag1.RowsAffected() + tag2.RowsAffected(), nil
}
