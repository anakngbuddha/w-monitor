// Package retention implements the hourly cleanup job:
//   - Deletes rows older than 30 days
//   - Collapses raw rows older than 24h into hourly averages
package retention

import (
	"database/sql"
	"fmt"
	"log"
	"time"
)

const (
	retentionDays  = 30
	downsampleAfter = 24 * time.Hour
)

// Job holds a reference to the underlying *sql.DB for direct queries.
type Job struct {
	conn *sql.DB
}

// New creates a retention Job.
func New(conn *sql.DB) *Job {
	return &Job{conn: conn}
}

// Run executes one retention pass: purge old rows then downsample.
func (j *Job) Run() error {
	cutoff30d := time.Now().Add(-retentionDays * 24 * time.Hour)
	cutoff24h := time.Now().Add(-downsampleAfter)

	if err := j.purgeOld(cutoff30d); err != nil {
		return fmt.Errorf("purge: %w", err)
	}
	if err := j.downsampleMetrics(cutoff24h); err != nil {
		return fmt.Errorf("downsample metrics: %w", err)
	}
	if err := j.downsampleProcesses(cutoff24h); err != nil {
		return fmt.Errorf("downsample processes: %w", err)
	}
	return nil
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

// downsampleMetrics collapses raw metric rows older than cutoff into hourly averages.
// It computes averages per hour bucket, inserts them, then deletes the originals.
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
