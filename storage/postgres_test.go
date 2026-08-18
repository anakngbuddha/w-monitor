package storage_test

import (
	"testing"

	"Zeus/storage"
)

// TestPostgresStoreInterface verifies that *storage.PostgresDB implements storage.Store at compile time.
func TestPostgresStoreInterface(t *testing.T) {
	var _ storage.Store = (*storage.PostgresDB)(nil)
	var _ storage.Store = (*storage.DB)(nil)
}

// TestPostgresOpenInvalidDSN tests that OpenPostgres returns an error for invalid/unreachable DSN.
func TestPostgresOpenInvalidDSN(t *testing.T) {
	_, err := storage.OpenPostgres("postgres://invalid_user:invalid_pass@127.0.0.1:54321/nonexistent?sslmode=disable&connect_timeout=1")
	if err == nil {
		t.Error("expected error for unreachable postgres instance, got nil")
	}
	t.Logf("OpenPostgres error as expected: %v", err)
}
