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

	// QueryMetrics returns all metric rows at or after `since`, ordered ascending.
	QueryMetrics(since time.Time) ([]MetricRow, error)

	// QueryProcesses returns all process rows at or after `since`, ordered ascending.
	QueryProcesses(since time.Time) ([]ProcessRow, error)

	// CountMetrics returns the total number of metric rows.
	CountMetrics() (int, error)

	// CountProcesses returns the total number of process rows.
	CountProcesses() (int, error)

	// QueryServers returns distinct server_id values seen in the metrics table.
	QueryServers() ([]string, error)

	// Close releases the database connection.
	Close() error
}
