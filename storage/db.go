package storage

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	_ "modernc.org/sqlite"
)

// DB wraps a SQLite database connection.
type DB struct {
	conn *sql.DB
	Path string
}

// MetricRow holds a single metrics snapshot.
type MetricRow struct {
	ID               int64
	Timestamp        time.Time
	ServerID         string
	Hostname         string
	CPUPct           float64
	MemPct           float64
	DiskFreeGB       float64
	NetSentBytes     uint64
	NetRecvBytes     uint64
	CPUCores         int
	MemTotalGB       float64
	DiskTotalGB      float64
	DiskReadOps      uint64
	DiskWriteOps     uint64
	DiskIOPS         float64
	NetMBps          float64
	ConcurrentUsers  int
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
	ServerID  string
	Hostname  string
	PID       int32
	Name      string
	CPUPct    float64
	MemMB     float64
}

// DataDir returns the OS-appropriate data directory for sysmon.
func DataDir() (string, error) {
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
			return "", fmt.Errorf("cannot determine home dir: %w", err)
		}
		base = filepath.Join(home, ".local", "share")
	}
	dir := filepath.Join(base, "sysmon")
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
		"ALTER TABLE processes ADD COLUMN server_id TEXT DEFAULT ''",
		"ALTER TABLE processes ADD COLUMN hostname TEXT DEFAULT ''",
	}
	for _, alter := range alters {
		db.conn.Exec(alter)
	}

	indexes := `
CREATE INDEX IF NOT EXISTS idx_metrics_ts ON metrics(timestamp);
CREATE INDEX IF NOT EXISTS idx_metrics_server ON metrics(server_id);
CREATE INDEX IF NOT EXISTS idx_processes_ts ON processes(timestamp);
`
	if _, err := db.conn.Exec(indexes); err != nil {
		return err
	}

	return nil
}

// InsertMetric writes one metrics row.
func (db *DB) InsertMetric(m MetricRow) error {
	_, err := db.conn.Exec(
		`INSERT INTO metrics(timestamp, server_id, hostname, cpu_pct, mem_pct, disk_free_gb, net_sent_bytes, net_recv_bytes, cpu_cores, mem_total_gb, disk_total_gb, disk_read_ops, disk_write_ops, disk_iops, net_mbps, concurrent_users, net_sent_external, net_recv_external, net_sent_internal, net_recv_internal)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.Timestamp.Unix(), m.ServerID, m.Hostname, m.CPUPct, m.MemPct, m.DiskFreeGB, m.NetSentBytes, m.NetRecvBytes, m.CPUCores, m.MemTotalGB, m.DiskTotalGB, m.DiskReadOps, m.DiskWriteOps, m.DiskIOPS, m.NetMBps, m.ConcurrentUsers, m.NetSentExternal, m.NetRecvExternal, m.NetSentInternal, m.NetRecvInternal,
	)
	return err
}

// InsertProcess writes one process row.
func (db *DB) InsertProcess(p ProcessRow) error {
	_, err := db.conn.Exec(
		`INSERT INTO processes(timestamp, server_id, hostname, pid, name, cpu_pct, mem_mb)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		p.Timestamp.Unix(), p.ServerID, p.Hostname, p.PID, p.Name, p.CPUPct, p.MemMB,
	)
	return err
}

// QueryMetrics returns rows within the given time window, oldest first.
func (db *DB) QueryMetrics(since time.Time) ([]MetricRow, error) {
	rows, err := db.conn.Query(
		`SELECT id, timestamp, server_id, hostname, cpu_pct, mem_pct, disk_free_gb, net_sent_bytes, net_recv_bytes, cpu_cores, mem_total_gb, disk_total_gb, disk_read_ops, disk_write_ops, disk_iops, net_mbps, concurrent_users, net_sent_external, net_recv_external, net_sent_internal, net_recv_internal
		 FROM metrics WHERE timestamp >= ? ORDER BY timestamp ASC`,
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
		if err := rows.Scan(&r.ID, &ts, &r.ServerID, &r.Hostname, &r.CPUPct, &r.MemPct, &r.DiskFreeGB, &r.NetSentBytes, &r.NetRecvBytes, &r.CPUCores, &r.MemTotalGB, &r.DiskTotalGB, &r.DiskReadOps, &r.DiskWriteOps, &r.DiskIOPS, &r.NetMBps, &r.ConcurrentUsers, &r.NetSentExternal, &r.NetRecvExternal, &r.NetSentInternal, &r.NetRecvInternal); err != nil {
			return nil, err
		}
		r.Timestamp = time.Unix(ts, 0)
		result = append(result, r)
	}
	return result, rows.Err()
}

// QueryProcesses returns process rows within the given time window.
func (db *DB) QueryProcesses(since time.Time) ([]ProcessRow, error) {
	rows, err := db.conn.Query(
		`SELECT id, timestamp, server_id, hostname, pid, name, cpu_pct, mem_mb
		 FROM processes WHERE timestamp >= ? ORDER BY timestamp ASC`,
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
func (db *DB) QueryServers() ([]string, error) {
	rows, err := db.conn.Query("SELECT DISTINCT server_id FROM metrics WHERE server_id != '' ORDER BY server_id")
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
