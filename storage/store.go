// Package storage defines the Store interface implemented by both SQLite and Postgres backends.
package storage

import "time"

// Store is the database abstraction used by collector, server, export, and retention.
// Both *DB (SQLite) and *PostgresDB (Postgres) implement this interface.
type Store interface {
	// InsertMetric writes one metrics snapshot row.
	InsertMetric(m MetricRow) error

	// InsertProcess writes one process snapshot row.
	InsertProcess(p ProcessRow) error

	// QueryMetrics returns metric rows at or after `since` for the given tenant.
	// tenantID is required; empty is never a global read (use AllTenantsQuerier for privileged jobs).
	QueryMetrics(since time.Time, tenantID string) ([]MetricRow, error)

	// QueryProcesses returns process rows at or after `since` for the given tenant.
	// tenantID is required.
	QueryProcesses(since time.Time, tenantID string) ([]ProcessRow, error)

	// CountMetrics returns the total number of metric rows.
	CountMetrics() (int, error)

	// CountProcesses returns the total number of process rows.
	CountProcesses() (int, error)

	// QueryServers returns distinct server_id values seen in the metrics table for the given tenant.
	// tenantID is required.
	QueryServers(tenantID string) ([]string, error)

	// Close releases the database connection.
	Close() error
}
