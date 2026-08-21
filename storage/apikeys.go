package storage

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"
)

// ErrAPIKeyNotFound is returned when a presented key is unknown or revoked.
var ErrAPIKeyNotFound = errors.New("storage: api key not found")

// APIKeyRecord is a single registered client credential.
//
// The plaintext key is never stored. Only KeyHash is persisted, so a database
// dump, a leaked backup, or a support engineer reading the table cannot
// authenticate as a client.
type APIKeyRecord struct {
	TenantID   string
	ClientName string
	KeyHash    string
	CreatedAt  time.Time
	LastSeenAt time.Time
	Revoked    bool
}

// HashAPIKey returns the hex-encoded SHA-256 of a plaintext API key.
//
// Lookups are performed on this hash, so comparison is a fixed-length equality
// test on a digest rather than on secret material.
func HashAPIKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

// GenerateAPIKey returns a new cryptographically random API key.
// The caller must show it to the operator immediately: it cannot be recovered.
func GenerateAPIKey() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate api key: %w", err)
	}
	return base64.StdEncoding.EncodeToString(buf), nil
}

// NewTenantID returns a random tenant identifier for a freshly created client.
func NewTenantID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate tenant id: %w", err)
	}
	return "t_" + hex.EncodeToString(buf), nil
}

// ── lazy table creation ──
//
// The api_keys table is created on first use per connection rather than inside
// migrate(). The existing migrate() deliberately swallows ALTER TABLE errors,
// which means a genuinely failed migration is indistinguishable from an
// already-applied one. Rather than add auth-critical DDL to that path, this
// creates its table with a checked error and reports failures honestly.

type guardedInit struct {
	once sync.Once
	err  error
}

func (g *guardedInit) do(fn func() error) error {
	g.once.Do(func() { g.err = fn() })
	return g.err
}

var apiKeyInit sync.Map // connection pointer -> *guardedInit

func ensureAPIKeyTable(key any, fn func() error) error {
	v, _ := apiKeyInit.LoadOrStore(key, &guardedInit{})
	return v.(*guardedInit).do(fn)
}

const sqliteAPIKeySchema = `
CREATE TABLE IF NOT EXISTS api_keys (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    key_hash     TEXT NOT NULL UNIQUE,
    tenant_id    TEXT NOT NULL,
    client_name  TEXT NOT NULL DEFAULT '',
    created_at   INTEGER NOT NULL,
    last_seen_at INTEGER NOT NULL DEFAULT 0,
    revoked_at   INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_api_keys_hash ON api_keys(key_hash);
`

const postgresAPIKeySchema = `
CREATE TABLE IF NOT EXISTS api_keys (
    id           SERIAL PRIMARY KEY,
    key_hash     TEXT NOT NULL UNIQUE,
    tenant_id    TEXT NOT NULL,
    client_name  TEXT NOT NULL DEFAULT '',
    created_at   BIGINT NOT NULL,
    last_seen_at BIGINT NOT NULL DEFAULT 0,
    revoked_at   BIGINT NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_api_keys_hash ON api_keys(key_hash);
`

// ── SQLite implementation ──

func (db *DB) ensureAPIKeys() error {
	return ensureAPIKeyTable(db.conn, func() error {
		_, err := db.conn.Exec(sqliteAPIKeySchema)
		if err != nil {
			return fmt.Errorf("create api_keys table: %w", err)
		}
		return nil
	})
}

// ResolveAPIKey looks up a key by its hash. Revoked keys are treated as unknown.
func (db *DB) ResolveAPIKey(keyHash string) (APIKeyRecord, error) {
	if err := db.ensureAPIKeys(); err != nil {
		return APIKeyRecord{}, err
	}
	var rec APIKeyRecord
	var createdAt, lastSeen, revokedAt int64
	err := db.conn.QueryRow(
		`SELECT key_hash, tenant_id, client_name, created_at, last_seen_at, revoked_at
		 FROM api_keys WHERE key_hash = ?`, keyHash,
	).Scan(&rec.KeyHash, &rec.TenantID, &rec.ClientName, &createdAt, &lastSeen, &revokedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return APIKeyRecord{}, ErrAPIKeyNotFound
	}
	if err != nil {
		return APIKeyRecord{}, err
	}
	if revokedAt != 0 {
		return APIKeyRecord{}, ErrAPIKeyNotFound
	}
	rec.CreatedAt = time.Unix(createdAt, 0)
	rec.LastSeenAt = time.Unix(lastSeen, 0)
	return rec, nil
}

// TouchAPIKey records that a key was just used. Best-effort: a failure here must
// never reject an otherwise valid request.
func (db *DB) TouchAPIKey(keyHash string) error {
	if err := db.ensureAPIKeys(); err != nil {
		return err
	}
	_, err := db.conn.Exec("UPDATE api_keys SET last_seen_at = ? WHERE key_hash = ?", time.Now().Unix(), keyHash)
	return err
}

// UpsertAPIKey registers or updates a client credential.
func (db *DB) UpsertAPIKey(rec APIKeyRecord) error {
	if err := db.ensureAPIKeys(); err != nil {
		return err
	}
	if rec.KeyHash == "" || rec.TenantID == "" {
		return errors.New("storage: api key record needs KeyHash and TenantID")
	}
	created := rec.CreatedAt
	if created.IsZero() {
		created = time.Now()
	}
	_, err := db.conn.Exec(
		`INSERT INTO api_keys(key_hash, tenant_id, client_name, created_at, last_seen_at, revoked_at)
		 VALUES (?, ?, ?, ?, 0, 0)
		 ON CONFLICT(key_hash) DO UPDATE SET tenant_id = excluded.tenant_id, client_name = excluded.client_name, revoked_at = 0`,
		rec.KeyHash, rec.TenantID, rec.ClientName, created.Unix(),
	)
	return err
}

// RevokeAPIKey marks every key belonging to a client as revoked.
func (db *DB) RevokeAPIKey(clientName string) (int64, error) {
	if err := db.ensureAPIKeys(); err != nil {
		return 0, err
	}
	res, err := db.conn.Exec("UPDATE api_keys SET revoked_at = ? WHERE client_name = ? AND revoked_at = 0", time.Now().Unix(), clientName)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ListAPIKeys returns all registered credentials (hashes only, never plaintext).
func (db *DB) ListAPIKeys() ([]APIKeyRecord, error) {
	if err := db.ensureAPIKeys(); err != nil {
		return nil, err
	}
	rows, err := db.conn.Query(
		`SELECT key_hash, tenant_id, client_name, created_at, last_seen_at, revoked_at
		 FROM api_keys ORDER BY client_name, created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []APIKeyRecord
	for rows.Next() {
		var rec APIKeyRecord
		var createdAt, lastSeen, revokedAt int64
		if err := rows.Scan(&rec.KeyHash, &rec.TenantID, &rec.ClientName, &createdAt, &lastSeen, &revokedAt); err != nil {
			return nil, err
		}
		rec.CreatedAt = time.Unix(createdAt, 0)
		rec.LastSeenAt = time.Unix(lastSeen, 0)
		rec.Revoked = revokedAt != 0
		out = append(out, rec)
	}
	return out, rows.Err()
}

// Ping verifies the database is reachable. Used by the health endpoint, which
// previously reported "ok" even with a dead database.
func (db *DB) Ping(ctx context.Context) error {
	return db.conn.PingContext(ctx)
}

// ── Postgres implementation ──

func (pg *PostgresDB) ensureAPIKeys() error {
	return ensureAPIKeyTable(pg.pool, func() error {
		_, err := pg.pool.Exec(context.Background(), postgresAPIKeySchema)
		if err != nil {
			return fmt.Errorf("create api_keys table: %w", err)
		}
		return nil
	})
}

// ResolveAPIKey looks up a key by its hash. Revoked keys are treated as unknown.
func (pg *PostgresDB) ResolveAPIKey(keyHash string) (APIKeyRecord, error) {
	if err := pg.ensureAPIKeys(); err != nil {
		return APIKeyRecord{}, err
	}
	var rec APIKeyRecord
	var createdAt, lastSeen, revokedAt int64
	err := pg.pool.QueryRow(context.Background(),
		`SELECT key_hash, tenant_id, client_name, created_at, last_seen_at, revoked_at
		 FROM api_keys WHERE key_hash = $1`, keyHash,
	).Scan(&rec.KeyHash, &rec.TenantID, &rec.ClientName, &createdAt, &lastSeen, &revokedAt)
	if err != nil {
		// pgx returns pgx.ErrNoRows; comparing on message keeps this file free of
		// a pgx import for a single sentinel.
		if err.Error() == "no rows in result set" {
			return APIKeyRecord{}, ErrAPIKeyNotFound
		}
		return APIKeyRecord{}, err
	}
	if revokedAt != 0 {
		return APIKeyRecord{}, ErrAPIKeyNotFound
	}
	rec.CreatedAt = time.Unix(createdAt, 0)
	rec.LastSeenAt = time.Unix(lastSeen, 0)
	return rec, nil
}

// TouchAPIKey records that a key was just used.
func (pg *PostgresDB) TouchAPIKey(keyHash string) error {
	if err := pg.ensureAPIKeys(); err != nil {
		return err
	}
	_, err := pg.pool.Exec(context.Background(),
		"UPDATE api_keys SET last_seen_at = $1 WHERE key_hash = $2", time.Now().Unix(), keyHash)
	return err
}

// UpsertAPIKey registers or updates a client credential.
func (pg *PostgresDB) UpsertAPIKey(rec APIKeyRecord) error {
	if err := pg.ensureAPIKeys(); err != nil {
		return err
	}
	if rec.KeyHash == "" || rec.TenantID == "" {
		return errors.New("storage: api key record needs KeyHash and TenantID")
	}
	created := rec.CreatedAt
	if created.IsZero() {
		created = time.Now()
	}
	_, err := pg.pool.Exec(context.Background(),
		`INSERT INTO api_keys(key_hash, tenant_id, client_name, created_at, last_seen_at, revoked_at)
		 VALUES ($1, $2, $3, $4, 0, 0)
		 ON CONFLICT(key_hash) DO UPDATE SET tenant_id = EXCLUDED.tenant_id, client_name = EXCLUDED.client_name, revoked_at = 0`,
		rec.KeyHash, rec.TenantID, rec.ClientName, created.Unix(),
	)
	return err
}

// RevokeAPIKey marks every key belonging to a client as revoked.
func (pg *PostgresDB) RevokeAPIKey(clientName string) (int64, error) {
	if err := pg.ensureAPIKeys(); err != nil {
		return 0, err
	}
	tag, err := pg.pool.Exec(context.Background(),
		"UPDATE api_keys SET revoked_at = $1 WHERE client_name = $2 AND revoked_at = 0", time.Now().Unix(), clientName)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// ListAPIKeys returns all registered credentials.
func (pg *PostgresDB) ListAPIKeys() ([]APIKeyRecord, error) {
	if err := pg.ensureAPIKeys(); err != nil {
		return nil, err
	}
	rows, err := pg.pool.Query(context.Background(),
		`SELECT key_hash, tenant_id, client_name, created_at, last_seen_at, revoked_at
		 FROM api_keys ORDER BY client_name, created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []APIKeyRecord
	for rows.Next() {
		var rec APIKeyRecord
		var createdAt, lastSeen, revokedAt int64
		if err := rows.Scan(&rec.KeyHash, &rec.TenantID, &rec.ClientName, &createdAt, &lastSeen, &revokedAt); err != nil {
			return nil, err
		}
		rec.CreatedAt = time.Unix(createdAt, 0)
		rec.LastSeenAt = time.Unix(lastSeen, 0)
		rec.Revoked = revokedAt != 0
		out = append(out, rec)
	}
	return out, rows.Err()
}

// Ping verifies the pool is reachable.
func (pg *PostgresDB) Ping(ctx context.Context) error {
	return pg.pool.Ping(ctx)
}
