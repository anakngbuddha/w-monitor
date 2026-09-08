// Package retention implements the hourly cleanup job.
//
// P1.02 containment (V07): destructive SQLite downsampling and automatic
// 30-day purge are disabled until P2.03. Run() reports status and enforces a
// disk budget instead of merging tenants/servers into unowned hourly averages.
package retention

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"
	"sync"
	"time"
)

const (
	retentionDays   = 30
	downsampleAfter = 24 * time.Hour
	// DefaultDiskBudgetBytes is 10 GiB. Override with SetDiskBudget or
	// WMONITOR_DISK_BUDGET_BYTES. Zero means no budget check.
	DefaultDiskBudgetBytes int64 = 10 << 30
	DisabledReason               = "destructive downsampling and automatic purge are disabled until P2.03 (V07 containment); original tenant/server rows are preserved"
)

// ErrDiskPressure is returned when the data file exceeds the configured budget.
var ErrDiskPressure = errors.New("retention: disk budget exhausted")

// Pruner is implemented by storage backends that can delete rows older than a cutoff (e.g. PostgresDB).
type Pruner interface {
	PurgeOld(cutoff time.Time) (mDel, pDel int64, err error)
}

// Status is the operator-visible retention containment state.
type Status struct {
	DownsamplingEnabled bool   `json:"downsampling_enabled"`
	PurgeEnabled        bool   `json:"purge_enabled"`
	Reason              string `json:"reason"`
	DiskUsedBytes       int64  `json:"disk_used_bytes"`
	DiskBudgetBytes     int64  `json:"disk_budget_bytes"`
	DiskPressure        bool   `json:"disk_pressure"`
}

// Job holds a reference to the underlying *sql.DB for SQLite queries or a Pruner for Postgres.
type Job struct {
	conn   *sql.DB
	pruner Pruner

	dataPath        string
	diskBudgetBytes int64

	mu         sync.Mutex
	lastStatus Status
}

// New creates a retention Job for SQLite.
func New(conn *sql.DB) *Job {
	j := &Job{conn: conn, diskBudgetBytes: DefaultDiskBudgetBytes}
	j.lastStatus = Status{Reason: DisabledReason, DiskBudgetBytes: DefaultDiskBudgetBytes}
	return j
}

// NewWithPruner creates a retention Job for any backend implementing Pruner.
func NewWithPruner(pruner Pruner) *Job {
	j := &Job{pruner: pruner, diskBudgetBytes: DefaultDiskBudgetBytes}
	j.lastStatus = Status{Reason: DisabledReason, DiskBudgetBytes: DefaultDiskBudgetBytes}
	return j
}

// SetDataPath is the SQLite file or data directory used for disk-budget accounting.
func (j *Job) SetDataPath(path string) {
	j.mu.Lock()
	j.dataPath = path
	j.mu.Unlock()
}

// SetDiskBudget sets the maximum allowed size of the data path. 0 disables the check.
func (j *Job) SetDiskBudget(bytes int64) {
	j.mu.Lock()
	j.diskBudgetBytes = bytes
	j.mu.Unlock()
}

// Status returns the last computed containment state.
func (j *Job) Status() Status {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.lastStatus
}

// Run records containment status and enforces the disk budget. It does not
// downsample or purge (V07).
func (j *Job) Run() error {
	used := j.diskUsed()
	j.mu.Lock()
	budget := j.diskBudgetBytes
	j.mu.Unlock()

	st := Status{
		DownsamplingEnabled: false,
		PurgeEnabled:        false,
		Reason:              DisabledReason,
		DiskUsedBytes:       used,
		DiskBudgetBytes:     budget,
	}
	if budget > 0 && used > budget {
		st.DiskPressure = true
		j.setStatus(st)
		log.Printf("[retention] disk pressure: used=%d budget=%d — refusing destructive cleanup; %s", used, budget, DisabledReason)
		return fmt.Errorf("%w: used %d bytes, budget %d", ErrDiskPressure, used, budget)
	}
	j.setStatus(st)
	log.Printf("[retention] skipped destructive downsampling/purge: %s", DisabledReason)
	return nil
}

func (j *Job) setStatus(st Status) {
	j.mu.Lock()
	j.lastStatus = st
	j.mu.Unlock()
}

func (j *Job) diskUsed() int64 {
	j.mu.Lock()
	path := j.dataPath
	j.mu.Unlock()
	if path == "" {
		return 0
	}
	fi, err := os.Stat(path)
	if err != nil {
		return 0
	}
	if fi.IsDir() {
		return 0
	}
	return fi.Size()
}

// purgeOld deletes all rows older than cutoff.
func (j *Job) purgeOld(cutoff time.Time) error {
	ts := cutoff.Unix()
	res, err := j.conn.Exec("DELETE FROM metrics WHERE timestamp < ?", ts)
	if err != nil {
		return err
	}
	mDel, _ := res.RowsAffected()

	res, err = j.conn.Exec("DELETE FROM processes WHERE timestamp < ?", ts)
	if err != nil {
		return err
	}
	pDel, _ := res.RowsAffected()

	log.Printf("[retention] purged %d metric rows, %d process rows older than %s", mDel, pDel, cutoff.Format(time.RFC3339))
	return nil
}

// unsafeDownsampleMetrics is the pre-P1.02 implementation. It MUST NOT be
// called: grouping by hour without tenant/server destroys ownership (V07).
// P2.03 will replace it with typed rollups. Kept only as a reference for that work.
func (j *Job) downsampleMetrics(cutoff time.Time) error {
	tx, err := j.conn.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Compute hourly averages for rows older than cutoff
	// Hour bucket = floor(timestamp / 3600) * 3600
	rows, err := tx.Query(`
		SELECT
			(timestamp / 3600) * 3600  AS hour_ts,
			AVG(cpu_pct),
			AVG(mem_pct),
			AVG(disk_free_gb),
			CAST(AVG(net_sent_bytes) AS INTEGER),
			CAST(AVG(net_recv_bytes) AS INTEGER),
			AVG(disk_iops),
			AVG(net_mbps),
			CAST(AVG(concurrent_users) AS INTEGER),
			COUNT(*)
		FROM metrics
		WHERE timestamp < ?
		GROUP BY hour_ts
		HAVING COUNT(*) > 1
	`, cutoff.Unix())
	if err != nil {
		return err
	}

	type avg struct {
		hourTS     int64
		cpu, mem   float64
		diskFreeGB float64
		netSent    int64
		netRecv    int64
		diskIOPS   float64
		netMBps    float64
		users      int
		count      int
	}
	var avgs []avg
	for rows.Next() {
		var a avg
		if err := rows.Scan(&a.hourTS, &a.cpu, &a.mem, &a.diskFreeGB, &a.netSent, &a.netRecv, &a.diskIOPS, &a.netMBps, &a.users, &a.count); err != nil {
			rows.Close()
			return err
		}
		avgs = append(avgs, a)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	collapsed := 0
	for _, a := range avgs {
		hourEnd := a.hourTS + 3600

		// Delete the individual rows FIRST (before inserting the average),
		// so the freshly-inserted averaged row is never caught by the DELETE.
		res, err := tx.Exec(
			`DELETE FROM metrics WHERE timestamp >= ? AND timestamp < ? AND timestamp < ?`,
			a.hourTS, hourEnd, cutoff.Unix(),
		)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		collapsed += int(n)

		// Insert the averaged row after deletion
		_, err = tx.Exec(
			`INSERT INTO metrics(timestamp, cpu_pct, mem_pct, disk_free_gb, net_sent_bytes, net_recv_bytes, disk_iops, net_mbps, concurrent_users)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			a.hourTS, a.cpu, a.mem, a.diskFreeGB, a.netSent, a.netRecv, a.diskIOPS, a.netMBps, a.users,
		)
		if err != nil {
			return err
		}
	}

	if err := tx.Commit(); err != nil {
		return err
	}

	log.Printf("[retention] metrics: collapsed %d raw rows into %d hourly averages", collapsed, len(avgs))
	return nil
}

// downsampleProcesses collapses process rows older than cutoff into hourly averages per process name.
func (j *Job) downsampleProcesses(cutoff time.Time) error {
	tx, err := j.conn.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	rows, err := tx.Query(`
		SELECT
			(timestamp / 3600) * 3600  AS hour_ts,
			name,
			CAST(AVG(pid) AS INTEGER),
			AVG(cpu_pct),
			AVG(mem_mb),
			COUNT(*)
		FROM processes
		WHERE timestamp < ?
		GROUP BY hour_ts, name
		HAVING COUNT(*) > 1
	`, cutoff.Unix())
	if err != nil {
		return err
	}

	type pavg struct {
		hourTS int64
		name   string
		pid    int64
		cpu    float64
		mem    float64
		count  int
	}
	var avgs []pavg
	for rows.Next() {
		var a pavg
		if err := rows.Scan(&a.hourTS, &a.name, &a.pid, &a.cpu, &a.mem, &a.count); err != nil {
			rows.Close()
			return err
		}
		avgs = append(avgs, a)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	collapsed := 0
	for _, a := range avgs {
		hourEnd := a.hourTS + 3600

		// Delete originals first, then insert the average.
		res, err := tx.Exec(
			`DELETE FROM processes WHERE timestamp >= ? AND timestamp < ? AND timestamp < ? AND name = ?`,
			a.hourTS, hourEnd, cutoff.Unix(), a.name,
		)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		collapsed += int(n)

		_, err = tx.Exec(
			`INSERT INTO processes(timestamp, pid, name, cpu_pct, mem_mb)
			 VALUES (?, ?, ?, ?, ?)`,
			a.hourTS, a.pid, a.name, a.cpu, a.mem,
		)
		if err != nil {
			return err
		}
	}

	if err := tx.Commit(); err != nil {
		return err
	}

	log.Printf("[retention] processes: collapsed %d raw rows into %d hourly averages", collapsed, len(avgs))
	return nil
}

// RunScheduled blocks, running the retention job once per hour.
func (j *Job) RunScheduled() {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for range ticker.C {
		if err := j.Run(); err != nil {
			log.Printf("[retention] error: %v", err)
		}
	}
}
