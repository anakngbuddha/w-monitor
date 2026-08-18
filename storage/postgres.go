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
    server_id TEXT   NOT NULL DEFAULT '',
    hostname  TEXT   NOT NULL DEFAULT '',
    pid       INT    NOT NULL,
    name      TEXT   NOT NULL,
    cpu_pct   DOUBLE PRECISION NOT NULL,
    mem_mb    DOUBLE PRECISION NOT NULL
);
`
	ctx := context.Background()
	if _, err := pg.pool.Exec(ctx, tables); err != nil {
		return err
	}

	// Add columns if they don't exist yet (safe ALTER TABLE pattern matching SQLite)
	alters := []string{
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
		"ALTER TABLE processes ADD COLUMN IF NOT EXISTS server_id TEXT NOT NULL DEFAULT ''",
		"ALTER TABLE processes ADD COLUMN IF NOT EXISTS hostname TEXT NOT NULL DEFAULT ''",
	}
	for _, alter := range alters {
		pg.pool.Exec(ctx, alter) // intentionally ignore errors (column may already exist)
	}

	indexes := `
CREATE INDEX IF NOT EXISTS idx_metrics_ts ON metrics(timestamp);
CREATE INDEX IF NOT EXISTS idx_metrics_server ON metrics(server_id);
CREATE INDEX IF NOT EXISTS idx_processes_ts ON processes(timestamp);
`
	if _, err := pg.pool.Exec(ctx, indexes); err != nil {
		return err
	}

	return nil
}

// InsertMetric writes one metrics row.
func (pg *PostgresDB) InsertMetric(m MetricRow) error {
	_, err := pg.pool.Exec(context.Background(),
		`INSERT INTO metrics(timestamp, server_id, hostname, cpu_pct, mem_pct, disk_free_gb, net_sent_bytes, net_recv_bytes, cpu_cores, mem_total_gb, disk_total_gb, disk_read_ops, disk_write_ops, disk_iops, net_mbps, concurrent_users, net_sent_external, net_recv_external, net_sent_internal, net_recv_internal)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20)`,
		m.Timestamp.Unix(), m.ServerID, m.Hostname, m.CPUPct, m.MemPct, m.DiskFreeGB,
		int64(m.NetSentBytes), int64(m.NetRecvBytes),
		m.CPUCores, m.MemTotalGB, m.DiskTotalGB,
		int64(m.DiskReadOps), int64(m.DiskWriteOps),
		m.DiskIOPS, m.NetMBps, m.ConcurrentUsers,
		int64(m.NetSentExternal), int64(m.NetRecvExternal),
		int64(m.NetSentInternal), int64(m.NetRecvInternal),
	)
	return err
}

// InsertProcess writes one process row.
func (pg *PostgresDB) InsertProcess(p ProcessRow) error {
	_, err := pg.pool.Exec(context.Background(),
		`INSERT INTO processes(timestamp, server_id, hostname, pid, name, cpu_pct, mem_mb)
		 VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		p.Timestamp.Unix(), p.ServerID, p.Hostname, p.PID, p.Name, p.CPUPct, p.MemMB,
	)
	return err
}

// QueryMetrics returns rows within the given time window, oldest first.
func (pg *PostgresDB) QueryMetrics(since time.Time) ([]MetricRow, error) {
	rows, err := pg.pool.Query(context.Background(),
		`SELECT id, timestamp, server_id, hostname, cpu_pct, mem_pct, disk_free_gb, net_sent_bytes, net_recv_bytes, cpu_cores, mem_total_gb, disk_total_gb, disk_read_ops, disk_write_ops, disk_iops, net_mbps, concurrent_users, net_sent_external, net_recv_external, net_sent_internal, net_recv_internal
		 FROM metrics WHERE timestamp >= $1 ORDER BY timestamp ASC`,
		since.Unix(),
	)
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
			&r.ID, &ts, &r.ServerID, &r.Hostname,
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

// QueryProcesses returns process rows within the given time window.
func (pg *PostgresDB) QueryProcesses(since time.Time) ([]ProcessRow, error) {
	rows, err := pg.pool.Query(context.Background(),
		`SELECT id, timestamp, server_id, hostname, pid, name, cpu_pct, mem_mb
		 FROM processes WHERE timestamp >= $1 ORDER BY timestamp ASC`,
		since.Unix(),
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []ProcessRow
	for rows.Next() {
		var p ProcessRow
		var ts int64
		if err := rows.Scan(&p.ID, &ts, &p.ServerID, &p.Hostname, &p.PID, &p.Name, &p.CPUPct, &p.MemMB); err != nil {
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
func (pg *PostgresDB) QueryServers() ([]string, error) {
	rows, err := pg.pool.Query(context.Background(),
		"SELECT DISTINCT server_id FROM metrics WHERE server_id != '' ORDER BY server_id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var servers []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		servers = append(servers, s)
	}
	return servers, rows.Err()
}
