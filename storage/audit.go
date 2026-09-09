package storage

import (
	"context"
	"errors"
	"strings"
	"time"
)

const maxAuditRows = 1000

// AuditEvent is an append-only security/business record. It never stores tokens,
// hashes of secrets, request bodies, or client-supplied foreign tenant fields.
type AuditEvent struct {
	At          time.Time
	TenantID    string
	ActorKind   string
	ActorPrefix string
	Action      string
	TargetType  string
	TargetID    string
}

var allowedAuditActions = map[string]bool{
	"session.login":      true,
	"session.logout":     true,
	"enroll.code_issued": true,
	"client.created":     true,
	"agent.revoked":      true,
	"agent.rotated":      true,
	"enroll.completed":   true,
	"admin.token_issued": true,
	"tenants.migrated":   true,
}

func (e AuditEvent) validate() error {
	if err := RequireTenant(e.TenantID); err != nil {
		return err
	}
	if !allowedAuditActions[e.Action] {
		return errors.New("audit: unsupported action")
	}
	if len(e.ActorKind) > 32 || len(e.ActorPrefix) > 16 || len(e.TargetType) > 32 || len(e.TargetID) > 128 {
		return errors.New("audit: field too long")
	}
	if strings.ContainsAny(e.ActorPrefix, " \t\r\n") {
		return errors.New("audit: invalid actor prefix")
	}
	return nil
}

func (db *DB) AppendAudit(ctx context.Context, ev AuditEvent) error {
	if err := ev.validate(); err != nil {
		return err
	}
	if ev.At.IsZero() {
		ev.At = time.Now().UTC()
	}
	_, err := db.conn.ExecContext(ctx,
		`INSERT INTO audit_events(ts, tenant_id, actor_kind, actor_prefix, action, target_type, target_id) VALUES(?,?,?,?,?,?,?)`,
		ev.At.Unix(), ev.TenantID, ev.ActorKind, ev.ActorPrefix, ev.Action, ev.TargetType, ev.TargetID)
	return err
}

func (db *DB) ListAudit(ctx context.Context, tenantID string, limit int) ([]AuditEvent, error) {
	if err := RequireTenant(tenantID); err != nil {
		return nil, err
	}
	if limit < 1 || limit > maxAuditRows {
		limit = maxAuditRows
	}
	rows, err := db.conn.QueryContext(ctx,
		`SELECT ts, tenant_id, actor_kind, actor_prefix, action, target_type, target_id FROM audit_events WHERE tenant_id = ? ORDER BY ts DESC, id DESC LIMIT ?`,
		tenantID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanAudit(rows.Next, rows.Scan, rows.Err)
}

func (pg *PostgresDB) AppendAudit(ctx context.Context, ev AuditEvent) error {
	if err := ev.validate(); err != nil {
		return err
	}
	if ev.At.IsZero() {
		ev.At = time.Now().UTC()
	}
	_, err := pg.pool.Exec(ctx,
		`INSERT INTO audit_events(ts, tenant_id, actor_kind, actor_prefix, action, target_type, target_id) VALUES($1,$2,$3,$4,$5,$6,$7)`,
		ev.At.Unix(), ev.TenantID, ev.ActorKind, ev.ActorPrefix, ev.Action, ev.TargetType, ev.TargetID)
	return err
}

func (pg *PostgresDB) ListAudit(ctx context.Context, tenantID string, limit int) ([]AuditEvent, error) {
	if err := RequireTenant(tenantID); err != nil {
		return nil, err
	}
	if limit < 1 || limit > maxAuditRows {
		limit = maxAuditRows
	}
	rows, err := pg.pool.Query(ctx,
		`SELECT ts, tenant_id, actor_kind, actor_prefix, action, target_type, target_id FROM audit_events WHERE tenant_id = $1 ORDER BY ts DESC, id DESC LIMIT $2`,
		tenantID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanAudit(rows.Next, rows.Scan, rows.Err)
}

func scanAudit(next func() bool, scan func(...any) error, errFn func() error) ([]AuditEvent, error) {
	var out []AuditEvent
	for next() {
		var ev AuditEvent
		var ts int64
		if err := scan(&ts, &ev.TenantID, &ev.ActorKind, &ev.ActorPrefix, &ev.Action, &ev.TargetType, &ev.TargetID); err != nil {
			return nil, err
		}
		ev.At = time.Unix(ts, 0).UTC()
		out = append(out, ev)
	}
	return out, errFn()
}
