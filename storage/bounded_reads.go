package storage

import (
	"context"
	"fmt"
	"time"
)

func boundedLimit(limit int) int {
	if limit < 1 || limit > DefaultQueryLimit {
		return DefaultQueryLimit
	}
	return limit
}

func processQuery(q MetricQuery) (string, []any, error) {
	if err := RequireTenant(q.TenantID); err != nil {
		return "", nil, err
	}
	query := `SELECT id,timestamp,tenant_id,server_id,hostname,pid,name,cpu_pct,mem_mb FROM processes WHERE tenant_id=$1 AND timestamp>=$2`
	args := []any{q.TenantID, q.Since.Unix()}
	if !q.Until.IsZero() {
		args = append(args, q.Until.Unix())
		query += fmt.Sprintf(" AND timestamp<=$%d", len(args))
	}
	if q.ServerID != "" {
		args = append(args, q.ServerID)
		query += fmt.Sprintf(" AND server_id=$%d", len(args))
	}
	if q.AfterID > 0 {
		args = append(args, q.AfterUnix, q.AfterUnix, q.AfterID)
		a := len(args)
		query += fmt.Sprintf(" AND (timestamp>$%d OR (timestamp=$%d AND id>$%d))", a-2, a-1, a)
	}
	args = append(args, boundedLimit(q.Limit))
	query += fmt.Sprintf(" ORDER BY timestamp ASC,id ASC LIMIT $%d", len(args))
	return query, args, nil
}

func (db *DB) QueryProcessesQ(q MetricQuery) ([]ProcessRow, error) {
	query, args, err := processQuery(q)
	if err != nil {
		return nil, err
	}
	rows, err := db.conn.QueryContext(queryContext(q.Ctx), query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ProcessRow
	for rows.Next() {
		var p ProcessRow
		var ts int64
		if err := rows.Scan(&p.ID, &ts, &p.TenantID, &p.ServerID, &p.Hostname, &p.PID, &p.Name, &p.CPUPct, &p.MemMB); err != nil {
			return nil, err
		}
		p.Timestamp = time.Unix(ts, 0)
		out = append(out, p)
	}
	return out, rows.Err()
}

func (pg *PostgresDB) QueryProcessesQ(q MetricQuery) ([]ProcessRow, error) {
	query, args, err := processQuery(q)
	if err != nil {
		return nil, err
	}
	rows, err := pg.pool.Query(queryContext(q.Ctx), query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ProcessRow
	for rows.Next() {
		var p ProcessRow
		var ts int64
		if err := rows.Scan(&p.ID, &ts, &p.TenantID, &p.ServerID, &p.Hostname, &p.PID, &p.Name, &p.CPUPct, &p.MemMB); err != nil {
			return nil, err
		}
		p.Timestamp = time.Unix(ts, 0)
		out = append(out, p)
	}
	return out, rows.Err()
}

func (db *DB) QueryServersContext(ctx context.Context, tenant string, limit int) ([]string, error) {
	return db.QueryServersPage(ctx, tenant, limit, "")
}

func (db *DB) QueryServersPage(ctx context.Context, tenant string, limit int, after string) ([]string, error) {
	if err := RequireTenant(tenant); err != nil {
		return nil, err
	}
	query := `SELECT DISTINCT server_id FROM metrics WHERE tenant_id=$1 AND server_id!=''`
	args := []any{tenant}
	if after != "" {
		args = append(args, after)
		query += fmt.Sprintf(` AND server_id>$%d`, len(args))
	}
	args = append(args, boundedLimit(limit))
	query += fmt.Sprintf(` ORDER BY server_id LIMIT $%d`, len(args))
	rows, err := db.conn.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func (pg *PostgresDB) QueryServersContext(ctx context.Context, tenant string, limit int) ([]string, error) {
	return pg.QueryServersPage(ctx, tenant, limit, "")
}

func (pg *PostgresDB) QueryServersPage(ctx context.Context, tenant string, limit int, after string) ([]string, error) {
	if err := RequireTenant(tenant); err != nil {
		return nil, err
	}
	query := `SELECT DISTINCT server_id FROM metrics WHERE tenant_id=$1 AND server_id!=''`
	args := []any{tenant}
	if after != "" {
		args = append(args, after)
		query += fmt.Sprintf(` AND server_id>$%d`, len(args))
	}
	args = append(args, boundedLimit(limit))
	query += fmt.Sprintf(` ORDER BY server_id LIMIT $%d`, len(args))
	rows, err := pg.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
