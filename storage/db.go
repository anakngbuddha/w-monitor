package storage

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"Zeus/internal/fsroot"

	_ "modernc.org/sqlite"
)

// DB wraps a SQLite database connection.
type DB struct {
	conn *sql.DB
	Path string
}

// MetricRow holds a single metrics snapshot.
type MetricRow struct {
	ID              int64
	Timestamp       time.Time
	TenantID        string // API key used as tenant identifier (hub mode)
	ServerID        string
	Hostname        string
	CPUPct          float64
	MemPct          float64
	DiskFreeGB      float64
	NetSentBytes    uint64
	NetRecvBytes    uint64
	CPUCores        int
	MemTotalGB      float64
	DiskTotalGB     float64
	DiskReadOps     uint64
	DiskWriteOps    uint64
	DiskIOPS        float64
	NetMBps         float64
	ConcurrentUsers int
	// Per-interface network split (Phase 10)
	NetSentExternal uint64
	NetRecvExternal uint64
	NetSentInternal uint64
	NetRecvInternal uint64
}

// ProcessRow holds a single process snapshot.
type ProcessRow struct {
	ID        int64
	Timestamp time.Time
	TenantID  string // API key used as tenant identifier (hub mode)
	ServerID  string
	Hostname  string
	PID       int32
	Name      string
	CPUPct    float64
	MemMB     float64
}

// EnvDataDir overrides the process data directory (spool, sqlite, agent_id).
const EnvDataDir = "WMONITOR_DATA_DIR"

var (
	dataDirMu       sync.RWMutex
	dataDirOverride string
)

func init() {
	fsroot.Register(DefaultDataDirPath())
}

// SetDataDir injects the data directory for tests. Production paths are rejected.
func SetDataDir(dir string) error {
	if dir != "" {
		if err := fsroot.RejectProductionPath(dir); err != nil {
			return err
		}
	}
	dataDirMu.Lock()
	dataDirOverride = dir
	dataDirMu.Unlock()
	return nil
}

// DataDirOverride returns the injected data directory, or empty if unset.
func DataDirOverride() string {
	dataDirMu.RLock()
	defer dataDirMu.RUnlock()
	return dataDirOverride
}

// DefaultDataDirPath is the OS-default data directory. It does not create it.
func DefaultDataDirPath() string {
	var base string
	switch runtime.GOOS {
	case "windows":
		base = os.Getenv("LOCALAPPDATA")
		if base == "" {
			base = os.Getenv("PROGRAMDATA")
		}
		if base == "" {
			base = os.Getenv("APPDATA")
		}
		if base == "" {
			if home, err := os.UserHomeDir(); err == nil {
				base = filepath.Join(home, "AppData", "Local")
			} else {
				base = `C:\ProgramData`
			}
		}
	default: // linux and others
		home, err := os.UserHomeDir()
		if err != nil {
			return filepath.Join("/tmp", "sysmon")
		}
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, "sysmon")
}

// DataDir returns the OS-appropriate data directory for wmonitor.
func DataDir() (string, error) {
	dataDirMu.RLock()
	override := dataDirOverride
	dataDirMu.RUnlock()
	if override != "" {
		if err := os.MkdirAll(override, 0755); err != nil {
			return "", fmt.Errorf("cannot create data dir %s: %w", override, err)
		}
		return override, nil
	}
	if dir := os.Getenv(EnvDataDir); dir != "" {
		if fsroot.IsolationEnabled() {
			if err := fsroot.RejectProductionPath(dir); err != nil {
				return "", err
			}
		}
		if err := os.MkdirAll(dir, 0755); err != nil {
			return "", fmt.Errorf("cannot create data dir %s: %w", dir, err)
		}
		return dir, nil
	}
	if fsroot.IsolationEnabled() {
		return "", fmt.Errorf("storage: DataDir called without test override while %s=1", fsroot.EnvTestIsolation)
	}
	dir := DefaultDataDirPath()
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("cannot create data dir %s: %w", dir, err)
	}
	return dir, nil
}

// Open opens (or creates) the SQLite database at the given path and runs migrations.
func Open(dbPath string) (*DB, error) {
	conn, err := sql.Open("sqlite", dbPath+"?_journal=WAL&_timeout=5000&_fk=true")
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	// SQLite works best single-threaded for writes; allow multiple readers.
	conn.SetMaxOpenConns(1)

	db := &DB{conn: conn, Path: dbPath}
	if err := db.migrate(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return db, nil
}

// Close closes the database connection.
func (db *DB) Close() error {
	return db.conn.Close()
}

// migrate creates the schema if it does not exist.
func (db *DB) migrate() error {
	tables := `
CREATE TABLE IF NOT EXISTS metrics (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    timestamp           INTEGER NOT NULL,  -- Unix epoch seconds
    tenant_id           TEXT    DEFAULT '',
    server_id           TEXT    DEFAULT '',
    hostname            TEXT    DEFAULT '',
    cpu_pct             REAL    NOT NULL,
    mem_pct             REAL    NOT NULL,
    disk_free_gb        REAL    NOT NULL,
    net_sent_bytes      INTEGER NOT NULL,
    net_recv_bytes      INTEGER NOT NULL,
    cpu_cores           INTEGER DEFAULT 0,
    mem_total_gb        REAL    DEFAULT 0.0,
    disk_total_gb       REAL    DEFAULT 0.0,
    disk_read_ops       INTEGER DEFAULT 0,
    disk_write_ops      INTEGER DEFAULT 0,
    disk_iops           REAL    DEFAULT 0.0,
    net_mbps            REAL    DEFAULT 0.0,
    concurrent_users    INTEGER DEFAULT 0,
    net_sent_external   INTEGER DEFAULT 0,
    net_recv_external   INTEGER DEFAULT 0,
    net_sent_internal   INTEGER DEFAULT 0,
    net_recv_internal   INTEGER DEFAULT 0
);

CREATE TABLE IF NOT EXISTS processes (
    id        INTEGER PRIMARY KEY AUTOINCREMENT,
    timestamp INTEGER NOT NULL,
    tenant_id TEXT    DEFAULT '',
    server_id TEXT    DEFAULT '',
    hostname  TEXT    DEFAULT '',
    pid       INTEGER NOT NULL,
    name      TEXT    NOT NULL,
    cpu_pct   REAL    NOT NULL,
    mem_mb    REAL    NOT NULL
);
`
	if _, err := db.conn.Exec(tables); err != nil {
		return err
	}

	// Safely add new columns to existing databases (ignore errors if columns already exist)
	alters := []string{
		"ALTER TABLE metrics ADD COLUMN tenant_id TEXT DEFAULT ''",
		"ALTER TABLE metrics ADD COLUMN cpu_cores INTEGER DEFAULT 0",
		"ALTER TABLE metrics ADD COLUMN mem_total_gb REAL DEFAULT 0.0",
		"ALTER TABLE metrics ADD COLUMN disk_total_gb REAL DEFAULT 0.0",
		"ALTER TABLE metrics ADD COLUMN disk_read_ops INTEGER DEFAULT 0",
		"ALTER TABLE metrics ADD COLUMN disk_write_ops INTEGER DEFAULT 0",
		"ALTER TABLE metrics ADD COLUMN disk_iops REAL DEFAULT 0.0",
		"ALTER TABLE metrics ADD COLUMN net_mbps REAL DEFAULT 0.0",
		"ALTER TABLE metrics ADD COLUMN concurrent_users INTEGER DEFAULT 0",
		"ALTER TABLE metrics ADD COLUMN server_id TEXT DEFAULT ''",
		"ALTER TABLE metrics ADD COLUMN hostname TEXT DEFAULT ''",
		"ALTER TABLE metrics ADD COLUMN net_sent_external INTEGER DEFAULT 0",
		"ALTER TABLE metrics ADD COLUMN net_recv_external INTEGER DEFAULT 0",
		"ALTER TABLE metrics ADD COLUMN net_sent_internal INTEGER DEFAULT 0",
		"ALTER TABLE metrics ADD COLUMN net_recv_internal INTEGER DEFAULT 0",
		"ALTER TABLE processes ADD COLUMN tenant_id TEXT DEFAULT ''",
		"ALTER TABLE processes ADD COLUMN server_id TEXT DEFAULT ''",
		"ALTER TABLE processes ADD COLUMN hostname TEXT DEFAULT ''",
	}
	for _, alter := range alters {
		db.conn.Exec(alter)
	}

	indexes := `
CREATE INDEX IF NOT EXISTS idx_metrics_ts ON metrics(timestamp);
CREATE INDEX IF NOT EXISTS idx_metrics_server ON metrics(server_id);
CREATE INDEX IF NOT EXISTS idx_metrics_tenant ON metrics(tenant_id);
CREATE INDEX IF NOT EXISTS idx_processes_ts ON processes(timestamp);
`
	if _, err := db.conn.Exec(indexes); err != nil {
		return err
	}

	return nil
}

// InsertMetric writes one metrics row. An empty TenantID is stored as LocalTenantID.
func (db *DB) InsertMetric(m MetricRow) error {
	m.TenantID = normalizeInsertTenant(m.TenantID)
	_, err := db.conn.Exec(
		`INSERT INTO metrics(timestamp, tenant_id, server_id, hostname, cpu_pct, mem_pct, disk_free_gb, net_sent_bytes, net_recv_bytes, cpu_cores, mem_total_gb, disk_total_gb, disk_read_ops, disk_write_ops, disk_iops, net_mbps, concurrent_users, net_sent_external, net_recv_external, net_sent_internal, net_recv_internal)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.Timestamp.Unix(), m.TenantID, m.ServerID, m.Hostname, m.CPUPct, m.MemPct, m.DiskFreeGB, m.NetSentBytes, m.NetRecvBytes, m.CPUCores, m.MemTotalGB, m.DiskTotalGB, m.DiskReadOps, m.DiskWriteOps, m.DiskIOPS, m.NetMBps, m.ConcurrentUsers, m.NetSentExternal, m.NetRecvExternal, m.NetSentInternal, m.NetRecvInternal,
	)
	return err
}

// InsertProcess writes one process row. An empty TenantID is stored as LocalTenantID.
func (db *DB) InsertProcess(p ProcessRow) error {
	p.TenantID = normalizeInsertTenant(p.TenantID)
	_, err := db.conn.Exec(
		`INSERT INTO processes(timestamp, tenant_id, server_id, hostname, pid, name, cpu_pct, mem_mb)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		p.Timestamp.Unix(), p.TenantID, p.ServerID, p.Hostname, p.PID, p.Name, p.CPUPct, p.MemMB,
	)
	return err
}

// QueryMetrics returns rows within the given time window for one tenant.
// tenantID must be non-empty; empty is never a global read.
func (db *DB) QueryMetrics(since time.Time, tenantID string) ([]MetricRow, error) {
	return db.QueryMetricsQ(MetricQuery{Since: since, TenantID: tenantID})
}

// QueryMetricsAllTenants is a privileged health/alerting read. Not a customer export.
func (db *DB) QueryMetricsAllTenants(ctx context.Context, since time.Time, limit int) ([]MetricRow, error) {
	return db.queryMetrics(queryContext(ctx), since, time.Time{}, "", "", queryLimit(limit), true)
}

// QueryMetricsQ applies SQL-side tenant/server/time bounds, a limit, and ctx cancellation.
func (db *DB) QueryMetricsQ(q MetricQuery) ([]MetricRow, error) {
	if err := RequireTenant(q.TenantID); err != nil {
		return nil, err
	}
	return db.queryMetrics(queryContext(q.Ctx), q.Since, q.Until, q.TenantID, q.ServerID, queryLimit(q.Limit), false)
}

func (db *DB) queryMetrics(ctx context.Context, since, until time.Time, tenantID, serverID string, limit int, allTenants bool) ([]MetricRow, error) {
	q := `SELECT id, timestamp, tenant_id, server_id, hostname, cpu_pct, mem_pct, disk_free_gb, net_sent_bytes, net_recv_bytes, cpu_cores, mem_total_gb, disk_total_gb, disk_read_ops, disk_write_ops, disk_iops, net_mbps, concurrent_users, net_sent_external, net_recv_external, net_sent_internal, net_recv_internal
		 FROM metrics WHERE timestamp >= ?`
	args := []any{since.Unix()}
	if !until.IsZero() {
		q += ` AND timestamp <= ?`
		args = append(args, until.Unix())
	}
	if !allTenants {
		q += ` AND tenant_id = ?`
		args = append(args, tenantID)
	}
	if serverID != "" {
		q += ` AND server_id = ?`
		args = append(args, serverID)
	}
	q += ` ORDER BY timestamp ASC LIMIT ?`
	args = append(args, limit)

	rows, err := db.conn.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []MetricRow
	for rows.Next() {
		var r MetricRow
		var ts int64
		if err := rows.Scan(&r.ID, &ts, &r.TenantID, &r.ServerID, &r.Hostname, &r.CPUPct, &r.MemPct, &r.DiskFreeGB, &r.NetSentBytes, &r.NetRecvBytes, &r.CPUCores, &r.MemTotalGB, &r.DiskTotalGB, &r.DiskReadOps, &r.DiskWriteOps, &r.DiskIOPS, &r.NetMBps, &r.ConcurrentUsers, &r.NetSentExternal, &r.NetRecvExternal, &r.NetSentInternal, &r.NetRecvInternal); err != nil {
			return nil, err
		}
		r.Timestamp = time.Unix(ts, 0)
		result = append(result, r)
	}
	return result, rows.Err()
}

// QueryProcesses returns process rows within the given time window for one tenant.
func (db *DB) QueryProcesses(since time.Time, tenantID string) ([]ProcessRow, error) {
	if err := RequireTenant(tenantID); err != nil {
		return nil, err
	}
	return db.queryProcesses(context.Background(), since, tenantID, queryLimit(0), false)
}

func (db *DB) queryProcesses(ctx context.Context, since time.Time, tenantID string, limit int, allTenants bool) ([]ProcessRow, error) {
	q := `SELECT id, timestamp, tenant_id, server_id, hostname, pid, name, cpu_pct, mem_mb
		 FROM processes WHERE timestamp >= ?`
	args := []any{since.Unix()}
	if !allTenants {
		q += ` AND tenant_id = ?`
		args = append(args, tenantID)
	}
	q += ` ORDER BY timestamp ASC LIMIT ?`
	args = append(args, limit)
	rows, err := db.conn.QueryContext(ctx, q, args...)
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

// CountMetrics returns the total number of metrics rows.
func (db *DB) CountMetrics() (int, error) {
	var count int
	err := db.conn.QueryRow("SELECT COUNT(*) FROM metrics").Scan(&count)
	return count, err
}

// CountProcesses returns the total number of process rows.
func (db *DB) CountProcesses() (int, error) {
	var count int
	err := db.conn.QueryRow("SELECT COUNT(*) FROM processes").Scan(&count)
	return count, err
}

// QueryServers returns distinct server_id values seen in the metrics table.
// If tenantID is non-empty, only servers for that tenant are returned.
// QueryServers returns distinct server_id values for one tenant.
func (db *DB) QueryServers(tenantID string) ([]string, error) {
	if err := RequireTenant(tenantID); err != nil {
		return nil, err
	}
	rows, err := db.conn.Query(
		"SELECT DISTINCT server_id FROM metrics WHERE server_id != '' AND tenant_id = ? ORDER BY server_id",
		tenantID,
	)
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

// Conn exposes the underlying *sql.DB for use by other packages (retention, etc.).
func (db *DB) Conn() *sql.DB {
	return db.conn
}

// PurgeOld deletes metric and process rows older than cutoff timestamp.
func (db *DB) PurgeOld(cutoff time.Time) (int64, int64, error) {
	ts := cutoff.Unix()
	res1, err := db.conn.Exec("DELETE FROM metrics WHERE timestamp < ?", ts)
	if err != nil {
		return 0, 0, fmt.Errorf("purge metrics: %w", err)
	}
	mDel, _ := res1.RowsAffected()

	res2, err := db.conn.Exec("DELETE FROM processes WHERE timestamp < ?", ts)
	if err != nil {
		return 0, 0, fmt.Errorf("purge processes: %w", err)
	}
	pDel, _ := res2.RowsAffected()

	return mDel, pDel, nil
}

// MigrateTenantID reassigns all metric and process rows from oldTenant to newTenant.
func (db *DB) MigrateTenantID(oldTenant, newTenant string) (int64, error) {
	res1, err := db.conn.Exec("UPDATE metrics SET tenant_id = ? WHERE tenant_id = ?", newTenant, oldTenant)
	if err != nil {
		return 0, fmt.Errorf("migrate metrics tenant: %w", err)
	}
	mAff, _ := res1.RowsAffected()

	res2, err := db.conn.Exec("UPDATE processes SET tenant_id = ? WHERE tenant_id = ?", newTenant, oldTenant)
	if err != nil {
		return 0, fmt.Errorf("migrate processes tenant: %w", err)
	}
	pAff, _ := res2.RowsAffected()

	return mAff + pAff, nil
}
