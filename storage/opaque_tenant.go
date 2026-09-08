package storage

import (
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// OpaqueTenantReport is the result of a one-time plaintext-tenant migration.
type OpaqueTenantReport struct {
	BackupPath       string
	CredentialsMoved int
	MetricsMoved     int64
	ProcessesMoved   int64
}

// migrateOpaqueFailPoint is test-only: when set to "before-commit", the
// transaction is rolled back after applying updates so callers can prove
// atomic failure never leaves a mixed identity state.
var migrateOpaqueFailPoint string

// MigrateOpaqueTenants copies the SQLite file to backupDir, then remaps every
// non-opaque tenant_id on api_keys/metrics/processes to a fresh t_<hex> value
// in one transaction. Revoked credentials stay revoked. On any error the
// transaction is rolled back; the backup file is left for operator restore.
func (db *DB) MigrateOpaqueTenants(backupDir string) (OpaqueTenantReport, error) {
	var report OpaqueTenantReport
	if db == nil || db.conn == nil {
		return report, errors.New("storage: database is not open")
	}
	if err := db.ensureAPIKeys(); err != nil {
		return report, err
	}
	if backupDir == "" {
		return report, errors.New("storage: opaque tenant migration requires a backup directory")
	}
	if err := os.MkdirAll(backupDir, 0o700); err != nil {
		return report, fmt.Errorf("create backup dir: %w", err)
	}
	if db.Path == "" {
		return report, errors.New("storage: sqlite path unknown; cannot backup")
	}

	report.BackupPath = filepath.Join(backupDir, fmt.Sprintf("wmonitor-pre-opaque-tenant-%s.db", time.Now().UTC().Format("20060102T150405Z")))
	if err := copyFile(db.Path, report.BackupPath); err != nil {
		return OpaqueTenantReport{}, fmt.Errorf("backup failed (migration not started): %w", err)
	}

	tx, err := db.conn.Begin()
	if err != nil {
		return report, fmt.Errorf("begin migration: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	oldIDs, err := collectPlaintextTenantIDs(tx)
	if err != nil {
		return report, err
	}

	for _, oldID := range oldIDs {
		newID, err := NewTenantID()
		if err != nil {
			return report, err
		}
		res, err := tx.Exec("UPDATE api_keys SET tenant_id = ? WHERE tenant_id = ?", newID, oldID)
		if err != nil {
			return report, fmt.Errorf("remap api_keys %q: %w", oldID, err)
		}
		n, _ := res.RowsAffected()
		report.CredentialsMoved += int(n)

		res, err = tx.Exec("UPDATE metrics SET tenant_id = ? WHERE tenant_id = ?", newID, oldID)
		if err != nil {
			return report, fmt.Errorf("remap metrics %q: %w", oldID, err)
		}
		m, _ := res.RowsAffected()
		report.MetricsMoved += m

		res, err = tx.Exec("UPDATE processes SET tenant_id = ? WHERE tenant_id = ?", newID, oldID)
		if err != nil {
			return report, fmt.Errorf("remap processes %q: %w", oldID, err)
		}
		p, _ := res.RowsAffected()
		report.ProcessesMoved += p
	}

	if migrateOpaqueFailPoint == "before-commit" {
		return report, errors.New("storage: injected opaque-tenant migration failure")
	}

	if err := tx.Commit(); err != nil {
		return report, fmt.Errorf("commit migration: %w", err)
	}
	return report, nil
}

func collectPlaintextTenantIDs(tx *sql.Tx) ([]string, error) {
	seen := make(map[string]struct{})
	queries := []string{
		"SELECT DISTINCT tenant_id FROM api_keys WHERE tenant_id != ''",
		"SELECT DISTINCT tenant_id FROM metrics WHERE tenant_id != ''",
		"SELECT DISTINCT tenant_id FROM processes WHERE tenant_id != ''",
	}
	for _, q := range queries {
		rows, err := tx.Query(q)
		if err != nil {
			return nil, fmt.Errorf("list tenant ids: %w", err)
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return nil, err
			}
			if !IsOpaqueTenantID(id) {
				seen[id] = struct{}{}
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	out := make([]string, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	return out, nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}
