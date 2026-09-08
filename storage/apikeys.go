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
	"strings"
	"sync"
	"time"
)

// ErrAPIKeyNotFound is returned when a presented key is unknown, expired, or revoked.
var ErrAPIKeyNotFound = errors.New("storage: api key not found")

// ErrAPIKeyRevoked is returned by LookupAPIKey when the row exists but is revoked.
var ErrAPIKeyRevoked = errors.New("storage: api key revoked")

// ErrMissingCredentialFields is returned when UpsertAPIKey is given an empty kind or scope.
var ErrMissingCredentialFields = errors.New("storage: api key record needs Kind and Scope")

// ErrAmbiguousClientName is returned when a display name maps to more than one tenant.
var ErrAmbiguousClientName = errors.New("storage: client display name is ambiguous")

const (
	KindEnroll = "enroll"
	KindAgent  = "agent"
	KindRead   = "read"
	KindAdmin  = "admin"
	KindLegacy = "legacy"

	ScopeIngest = "ingest"
	ScopeRead   = "read"
	ScopeAdmin  = "admin"
	ScopeAll    = "all"
	ScopeEnroll = "enroll" // handshake-only; never a general-route permission

	PrefixAgent  = "wma_"
	PrefixRead   = "wmr_"
	PrefixAdmin  = "wmk_"
	PrefixEnroll = "wme_"
)

// APIKeyRecord is a single registered client credential.
//
// The plaintext key is never stored. Only KeyHash is persisted, so a database
// dump, a leaked backup, or a support engineer reading the table cannot
// authenticate as a client.
type APIKeyRecord struct {
	ID         int64
	TenantID   string
	ClientName string
	KeyHash    string
	KeyPrefix  string
	Kind       string // "enroll", "agent", "read", "admin", "legacy"
	Scope      string // "ingest", "read", "admin", "all"
	ExpiresAt  time.Time
	MaxUses    int
	Uses       int
	ServerID   string
	IssuedBy   string
	CreatedAt  time.Time
	LastSeenAt time.Time
	Revoked    bool
}

// crockfordAlphabet avoids visually ambiguous characters: I, L, O, U.
const crockfordAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// HashAPIKey returns the hex-encoded SHA-256 of a presented opaque token.
//
// Tokens are hashed as trimmed exact bytes. Enrollment-code formatting must
// go through HashEnrollCode; this function must not uppercase or strip wma_/wmr_/wmk_ tokens.
func HashAPIKey(key string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(key)))
	return hex.EncodeToString(sum[:])
}

// HashEnrollCode hashes a human enrollment code after hyphen/case normalization.
func HashEnrollCode(code string) string {
	return HashAPIKey(NormalizeEnrollCode(code))
}

// GenerateToken creates a high-entropy URL-safe token with a recognizable prefix.
func GenerateToken(kind string) (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	encoded := base64.RawURLEncoding.EncodeToString(buf)
	switch kind {
	case KindAgent:
		return PrefixAgent + encoded, nil
	case KindRead:
		return PrefixRead + encoded, nil
	case KindAdmin:
		return PrefixAdmin + encoded, nil
	case KindEnroll:
		return PrefixEnroll + encoded, nil
	default:
		return encoded, nil
	}
}

// GenerateEnrollCode returns a formatted, human-friendly code: WM-XXXX-XXXX-XXXX.
func GenerateEnrollCode() (string, error) {
	var bytes [12]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", fmt.Errorf("generate enroll code: %w", err)
	}
	var chars [12]byte
	for i, b := range bytes {
		chars[i] = crockfordAlphabet[b%byte(len(crockfordAlphabet))]
	}
	return fmt.Sprintf("WM-%s-%s-%s", string(chars[0:4]), string(chars[4:8]), string(chars[8:12])), nil
}

// NormalizeEnrollCode standardizes user input by upper-casing and normalizing dashes.
func NormalizeEnrollCode(code string) string {
	code = strings.TrimSpace(code)
	if strings.HasPrefix(code, PrefixEnroll) {
		return code
	}
	cleaned := strings.ToUpper(strings.ReplaceAll(strings.ReplaceAll(code, "-", ""), " ", ""))
	if strings.HasPrefix(cleaned, "WM") && len(cleaned) == 14 {
		return fmt.Sprintf("WM-%s-%s-%s", cleaned[2:6], cleaned[6:10], cleaned[10:14])
	}
	return cleaned
}

// ExtractKeyPrefix returns the prefix of a token or code (e.g. "wma_", "wmr_").
func ExtractKeyPrefix(token string) string {
	token = strings.TrimSpace(token)
	if len(token) >= 4 && token[3] == '_' {
		return token[:4]
	}
	if strings.HasPrefix(strings.ToUpper(token), "WM-") {
		return "wme_"
	}
	if len(token) > 8 {
		return token[:8]
	}
	return ""
}

// GenerateAPIKey returns a legacy random base64 API key. Kept for backwards compatibility.
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

// ── lazy table creation & migration ──

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
    kind         TEXT NOT NULL DEFAULT 'legacy',
    scope        TEXT NOT NULL DEFAULT 'all',
    key_prefix   TEXT NOT NULL DEFAULT '',
    expires_at   INTEGER NOT NULL DEFAULT 0,
    max_uses     INTEGER NOT NULL DEFAULT 0,
    uses         INTEGER NOT NULL DEFAULT 0,
    server_id    TEXT NOT NULL DEFAULT '',
    issued_by    TEXT NOT NULL DEFAULT '',
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
    kind         TEXT NOT NULL DEFAULT 'legacy',
    scope        TEXT NOT NULL DEFAULT 'all',
    key_prefix   TEXT NOT NULL DEFAULT '',
    expires_at   BIGINT NOT NULL DEFAULT 0,
    max_uses     INT NOT NULL DEFAULT 0,
    uses         INT NOT NULL DEFAULT 0,
    server_id    TEXT NOT NULL DEFAULT '',
    issued_by    TEXT NOT NULL DEFAULT '',
    created_at   BIGINT NOT NULL,
    last_seen_at BIGINT NOT NULL DEFAULT 0,
    revoked_at   BIGINT NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_api_keys_hash ON api_keys(key_hash);
`

func migrateSQLiteAPIKeys(conn *sql.DB) error {
	rows, err := conn.Query("PRAGMA table_info(api_keys)")
	if err != nil {
		return err
	}
	defer rows.Close()

	cols := make(map[string]bool)
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull, pk int
		var dfltValue any
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dfltValue, &pk); err != nil {
			return err
		}
		cols[name] = true
	}

	migrations := []struct {
		name string
		ddl  string
	}{
		{"kind", "ALTER TABLE api_keys ADD COLUMN kind TEXT NOT NULL DEFAULT 'legacy'"},
		{"scope", "ALTER TABLE api_keys ADD COLUMN scope TEXT NOT NULL DEFAULT 'all'"},
		{"key_prefix", "ALTER TABLE api_keys ADD COLUMN key_prefix TEXT NOT NULL DEFAULT ''"},
		{"expires_at", "ALTER TABLE api_keys ADD COLUMN expires_at INTEGER NOT NULL DEFAULT 0"},
		{"max_uses", "ALTER TABLE api_keys ADD COLUMN max_uses INTEGER NOT NULL DEFAULT 0"},
		{"uses", "ALTER TABLE api_keys ADD COLUMN uses INTEGER NOT NULL DEFAULT 0"},
		{"server_id", "ALTER TABLE api_keys ADD COLUMN server_id TEXT NOT NULL DEFAULT ''"},
		{"issued_by", "ALTER TABLE api_keys ADD COLUMN issued_by TEXT NOT NULL DEFAULT ''"},
	}

	for _, m := range migrations {
		if !cols[m.name] {
			if _, err := conn.Exec(m.ddl); err != nil {
				return fmt.Errorf("migrate sqlite api_keys col %s: %w", m.name, err)
			}
		}
	}
	_, _ = conn.Exec("CREATE INDEX IF NOT EXISTS idx_api_keys_tenant ON api_keys(tenant_id)")
	_, _ = conn.Exec("CREATE INDEX IF NOT EXISTS idx_api_keys_server ON api_keys(tenant_id, server_id)")
	return ensureEnrollmentAuxSQLite(conn)
}

// ── SQLite implementation ──

func (db *DB) ensureAPIKeys() error {
	return ensureAPIKeyTable(db.conn, func() error {
		if _, err := db.conn.Exec(sqliteAPIKeySchema); err != nil {
			return fmt.Errorf("create api_keys table: %w", err)
		}
		return migrateSQLiteAPIKeys(db.conn)
	})
}

// ResolveAPIKey looks up a key by its hash. Revoked, expired, or exhausted keys are rejected.
func (db *DB) ResolveAPIKey(keyHash string) (APIKeyRecord, error) {
	if err := db.ensureAPIKeys(); err != nil {
		return APIKeyRecord{}, err
	}
	var rec APIKeyRecord
	var expiresAt, createdAt, lastSeen, revokedAt int64
	err := db.conn.QueryRow(
		`SELECT id, key_hash, tenant_id, client_name, kind, scope, key_prefix,
		        expires_at, max_uses, uses, server_id, issued_by,
		        created_at, last_seen_at, revoked_at
		 FROM api_keys WHERE key_hash = ?`, keyHash,
	).Scan(
		&rec.ID, &rec.KeyHash, &rec.TenantID, &rec.ClientName, &rec.Kind, &rec.Scope, &rec.KeyPrefix,
		&expiresAt, &rec.MaxUses, &rec.Uses, &rec.ServerID, &rec.IssuedBy,
		&createdAt, &lastSeen, &revokedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return APIKeyRecord{}, ErrAPIKeyNotFound
	}
	if err != nil {
		return APIKeyRecord{}, err
	}
	rec.ExpiresAt = time.Unix(expiresAt, 0)
	rec.CreatedAt = time.Unix(createdAt, 0)
	rec.LastSeenAt = time.Unix(lastSeen, 0)
	rec.Revoked = revokedAt != 0
	if rec.Kind == KindEnroll {
		return APIKeyRecord{}, ErrAPIKeyNotFound
	}
	if rec.Revoked {
		return APIKeyRecord{}, ErrAPIKeyNotFound
	}
	if expiresAt > 0 && time.Now().Unix() >= expiresAt {
		return APIKeyRecord{}, ErrAPIKeyNotFound
	}
	if rec.MaxUses > 0 && rec.Uses >= rec.MaxUses {
		return APIKeyRecord{}, ErrAPIKeyNotFound
	}
	return rec, nil
}

// LookupAPIKey returns the credential row including revoked records.
// Enrollment codes are still hidden from general lookup.
func (db *DB) LookupAPIKey(keyHash string) (APIKeyRecord, error) {
	if err := db.ensureAPIKeys(); err != nil {
		return APIKeyRecord{}, err
	}
	var rec APIKeyRecord
	var expiresAt, createdAt, lastSeen, revokedAt int64
	err := db.conn.QueryRow(
		`SELECT id, key_hash, tenant_id, client_name, kind, scope, key_prefix,
		        expires_at, max_uses, uses, server_id, issued_by,
		        created_at, last_seen_at, revoked_at
		 FROM api_keys WHERE key_hash = ?`, keyHash,
	).Scan(
		&rec.ID, &rec.KeyHash, &rec.TenantID, &rec.ClientName, &rec.Kind, &rec.Scope, &rec.KeyPrefix,
		&expiresAt, &rec.MaxUses, &rec.Uses, &rec.ServerID, &rec.IssuedBy,
		&createdAt, &lastSeen, &revokedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return APIKeyRecord{}, ErrAPIKeyNotFound
	}
	if err != nil {
		return APIKeyRecord{}, err
	}
	rec.ExpiresAt = time.Unix(expiresAt, 0)
	rec.CreatedAt = time.Unix(createdAt, 0)
	rec.LastSeenAt = time.Unix(lastSeen, 0)
	rec.Revoked = revokedAt != 0
	if rec.Revoked {
		return rec, ErrAPIKeyRevoked
	}
	return rec, nil
}

// ConsumeEnrollCode atomically increments uses and returns the enrollment code record.
func (db *DB) ConsumeEnrollCode(codeHash string) (APIKeyRecord, error) {
	if err := db.ensureAPIKeys(); err != nil {
		return APIKeyRecord{}, err
	}
	now := time.Now().Unix()
	var rec APIKeyRecord
	var expiresAt, createdAt, lastSeen, revokedAt int64
	err := db.conn.QueryRow(
		`UPDATE api_keys
		 SET uses = uses + 1
		 WHERE key_hash = ?
		   AND revoked_at = 0
		   AND kind = 'enroll'
		   AND (expires_at = 0 OR expires_at > ?)
		   AND (max_uses = 0 OR uses < max_uses)
		 RETURNING id, key_hash, tenant_id, client_name, kind, scope, key_prefix,
		           expires_at, max_uses, uses, server_id, issued_by,
		           created_at, last_seen_at, revoked_at`,
		codeHash, now,
	).Scan(
		&rec.ID, &rec.KeyHash, &rec.TenantID, &rec.ClientName, &rec.Kind, &rec.Scope, &rec.KeyPrefix,
		&expiresAt, &rec.MaxUses, &rec.Uses, &rec.ServerID, &rec.IssuedBy,
		&createdAt, &lastSeen, &revokedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return APIKeyRecord{}, ErrAPIKeyNotFound
	}
	if err != nil {
		return APIKeyRecord{}, err
	}
	rec.ExpiresAt = time.Unix(expiresAt, 0)
	rec.CreatedAt = time.Unix(createdAt, 0)
	rec.LastSeenAt = time.Unix(lastSeen, 0)
	return rec, nil
}

// TouchAPIKey records that a key was just used and drops any enrollment
// handshake plaintext for that credential (lost-reply recovery ends after first use).
func (db *DB) TouchAPIKey(keyHash string) error {
	if err := db.ensureAPIKeys(); err != nil {
		return err
	}
	now := time.Now().Unix()
	if _, err := db.conn.Exec("UPDATE api_keys SET last_seen_at = ? WHERE key_hash = ?", now, keyHash); err != nil {
		return err
	}
	_, err := db.conn.Exec("DELETE FROM enrollment_handshakes WHERE key_hash = ?", keyHash)
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
	if rec.Kind == "" || rec.Scope == "" {
		return ErrMissingCredentialFields
	}
	created := rec.CreatedAt
	if created.IsZero() {
		created = time.Now()
	}
	var expiresAt int64
	if !rec.ExpiresAt.IsZero() {
		expiresAt = rec.ExpiresAt.Unix()
	}
	_, err := db.conn.Exec(
		`INSERT INTO api_keys(
			key_hash, tenant_id, client_name, kind, scope, key_prefix,
			expires_at, max_uses, uses, server_id, issued_by,
			created_at, last_seen_at, revoked_at
		 ) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, 0)
		 ON CONFLICT(key_hash) DO UPDATE SET
			tenant_id = excluded.tenant_id,
			client_name = excluded.client_name,
			kind = excluded.kind,
			scope = excluded.scope,
			key_prefix = excluded.key_prefix,
			expires_at = excluded.expires_at,
			max_uses = excluded.max_uses,
			server_id = excluded.server_id,
			issued_by = excluded.issued_by`,
		rec.KeyHash, rec.TenantID, rec.ClientName, rec.Kind, rec.Scope, rec.KeyPrefix,
		expiresAt, rec.MaxUses, rec.Uses, rec.ServerID, rec.IssuedBy,
		created.Unix(),
	)
	return err
}

// RevokeAPIKey marks every key belonging to a client as revoked.
func (db *DB) RevokeAPIKey(clientName string) (int64, error) {
	if err := db.ensureAPIKeys(); err != nil {
		return 0, err
	}
	now := time.Now().Unix()
	res, err := db.conn.Exec("UPDATE api_keys SET revoked_at = ? WHERE client_name = ? AND revoked_at = 0", now, clientName)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return n, err
	}
	if n > 0 {
		if err := bumpAuthEpochSQLite(db.conn, now); err != nil {
			return n, err
		}
	}
	return n, nil
}

// RevokeAgent marks a machine agent token as revoked.
func (db *DB) RevokeAgent(tenantID, serverID string) (int64, error) {
	if err := db.ensureAPIKeys(); err != nil {
		return 0, err
	}
	now := time.Now().Unix()
	res, err := db.conn.Exec(
		"UPDATE api_keys SET revoked_at = ? WHERE tenant_id = ? AND server_id = ? AND kind = 'agent' AND revoked_at = 0",
		now, tenantID, serverID,
	)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return n, err
	}
	if n > 0 {
		if err := bumpAuthEpochSQLite(db.conn, now); err != nil {
			return n, err
		}
	}
	return n, nil
}

// ListAgents returns all registered machine agent tokens.
func (db *DB) ListAgents(tenantID string) ([]APIKeyRecord, error) {
	if err := db.ensureAPIKeys(); err != nil {
		return nil, err
	}
	query := `
		SELECT id, key_hash, tenant_id, client_name, kind, scope, key_prefix,
		       expires_at, max_uses, uses, server_id, issued_by,
		       created_at, last_seen_at, revoked_at
		FROM api_keys
		WHERE kind = 'agent'
	`
	var args []any
	if tenantID != "" {
		query += " AND tenant_id = ?"
		args = append(args, tenantID)
	}
	query += " ORDER BY last_seen_at DESC, created_at DESC"
	rows, err := db.conn.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []APIKeyRecord
	for rows.Next() {
		var rec APIKeyRecord
		var expiresAt, createdAt, lastSeen, revokedAt int64
		if err := rows.Scan(
			&rec.ID, &rec.KeyHash, &rec.TenantID, &rec.ClientName, &rec.Kind, &rec.Scope, &rec.KeyPrefix,
			&expiresAt, &rec.MaxUses, &rec.Uses, &rec.ServerID, &rec.IssuedBy,
			&createdAt, &lastSeen, &revokedAt,
		); err != nil {
			return nil, err
		}
		rec.ExpiresAt = time.Unix(expiresAt, 0)
		rec.CreatedAt = time.Unix(createdAt, 0)
		rec.LastSeenAt = time.Unix(lastSeen, 0)
		rec.Revoked = revokedAt != 0
		out = append(out, rec)
	}
	return out, rows.Err()
}

// ListAPIKeys returns all registered credentials (hashes only, never plaintext).
func (db *DB) ListAPIKeys() ([]APIKeyRecord, error) {
	if err := db.ensureAPIKeys(); err != nil {
		return nil, err
	}
	rows, err := db.conn.Query(
		`SELECT id, key_hash, tenant_id, client_name, kind, scope, key_prefix,
		        expires_at, max_uses, uses, server_id, issued_by,
		        created_at, last_seen_at, revoked_at
		 FROM api_keys ORDER BY client_name, created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []APIKeyRecord
	for rows.Next() {
		var rec APIKeyRecord
		var expiresAt, createdAt, lastSeen, revokedAt int64
		if err := rows.Scan(
			&rec.ID, &rec.KeyHash, &rec.TenantID, &rec.ClientName, &rec.Kind, &rec.Scope, &rec.KeyPrefix,
			&expiresAt, &rec.MaxUses, &rec.Uses, &rec.ServerID, &rec.IssuedBy,
			&createdAt, &lastSeen, &revokedAt,
		); err != nil {
			return nil, err
		}
		rec.ExpiresAt = time.Unix(expiresAt, 0)
		rec.CreatedAt = time.Unix(createdAt, 0)
		rec.LastSeenAt = time.Unix(lastSeen, 0)
		rec.Revoked = revokedAt != 0
		out = append(out, rec)
	}
	return out, rows.Err()
}

// Ping verifies the database is reachable.
func (db *DB) Ping(ctx context.Context) error {
	return db.conn.PingContext(ctx)
}

// ── Postgres implementation ──

func (pg *PostgresDB) ensureAPIKeys() error {
	return ensureAPIKeyTable(pg.pool, func() error {
		ctx := context.Background()
		if _, err := pg.pool.Exec(ctx, postgresAPIKeySchema); err != nil {
			return fmt.Errorf("create postgres api_keys table: %w", err)
		}
		migrations := []string{
			"ALTER TABLE api_keys ADD COLUMN IF NOT EXISTS kind TEXT NOT NULL DEFAULT 'legacy'",
			"ALTER TABLE api_keys ADD COLUMN IF NOT EXISTS scope TEXT NOT NULL DEFAULT 'all'",
			"ALTER TABLE api_keys ADD COLUMN IF NOT EXISTS key_prefix TEXT NOT NULL DEFAULT ''",
			"ALTER TABLE api_keys ADD COLUMN IF NOT EXISTS expires_at BIGINT NOT NULL DEFAULT 0",
			"ALTER TABLE api_keys ADD COLUMN IF NOT EXISTS max_uses INT NOT NULL DEFAULT 0",
			"ALTER TABLE api_keys ADD COLUMN IF NOT EXISTS uses INT NOT NULL DEFAULT 0",
			"ALTER TABLE api_keys ADD COLUMN IF NOT EXISTS server_id TEXT NOT NULL DEFAULT ''",
			"ALTER TABLE api_keys ADD COLUMN IF NOT EXISTS issued_by TEXT NOT NULL DEFAULT ''",
			"CREATE INDEX IF NOT EXISTS idx_api_keys_tenant ON api_keys(tenant_id)",
			"CREATE INDEX IF NOT EXISTS idx_api_keys_server ON api_keys(tenant_id, server_id)",
		}
		for _, q := range migrations {
			if _, err := pg.pool.Exec(ctx, q); err != nil {
				return fmt.Errorf("migrate postgres api_keys: %w", err)
			}
		}
		return ensureEnrollmentAuxPostgres(ctx, pg)
	})
}

// ResolveAPIKey looks up a key by its hash.
func (pg *PostgresDB) ResolveAPIKey(keyHash string) (APIKeyRecord, error) {
	if err := pg.ensureAPIKeys(); err != nil {
		return APIKeyRecord{}, err
	}
	var rec APIKeyRecord
	var expiresAt, createdAt, lastSeen, revokedAt int64
	err := pg.pool.QueryRow(context.Background(),
		`SELECT id, key_hash, tenant_id, client_name, kind, scope, key_prefix,
		        expires_at, max_uses, uses, server_id, issued_by,
		        created_at, last_seen_at, revoked_at
		 FROM api_keys WHERE key_hash = $1`, keyHash,
	).Scan(
		&rec.ID, &rec.KeyHash, &rec.TenantID, &rec.ClientName, &rec.Kind, &rec.Scope, &rec.KeyPrefix,
		&expiresAt, &rec.MaxUses, &rec.Uses, &rec.ServerID, &rec.IssuedBy,
		&createdAt, &lastSeen, &revokedAt,
	)
	if err != nil {
		if err.Error() == "no rows in result set" {
			return APIKeyRecord{}, ErrAPIKeyNotFound
		}
		return APIKeyRecord{}, err
	}
	rec.ExpiresAt = time.Unix(expiresAt, 0)
	rec.CreatedAt = time.Unix(createdAt, 0)
	rec.LastSeenAt = time.Unix(lastSeen, 0)
	rec.Revoked = revokedAt != 0
	if rec.Kind == KindEnroll {
		return APIKeyRecord{}, ErrAPIKeyNotFound
	}
	if rec.Revoked {
		return APIKeyRecord{}, ErrAPIKeyNotFound
	}
	if expiresAt > 0 && time.Now().Unix() >= expiresAt {
		return APIKeyRecord{}, ErrAPIKeyNotFound
	}
	if rec.MaxUses > 0 && rec.Uses >= rec.MaxUses {
		return APIKeyRecord{}, ErrAPIKeyNotFound
	}
	return rec, nil
}

// LookupAPIKey returns the credential row including revoked records.
func (pg *PostgresDB) LookupAPIKey(keyHash string) (APIKeyRecord, error) {
	if err := pg.ensureAPIKeys(); err != nil {
		return APIKeyRecord{}, err
	}
	var rec APIKeyRecord
	var expiresAt, createdAt, lastSeen, revokedAt int64
	err := pg.pool.QueryRow(context.Background(),
		`SELECT id, key_hash, tenant_id, client_name, kind, scope, key_prefix,
		        expires_at, max_uses, uses, server_id, issued_by,
		        created_at, last_seen_at, revoked_at
		 FROM api_keys WHERE key_hash = $1`, keyHash,
	).Scan(
		&rec.ID, &rec.KeyHash, &rec.TenantID, &rec.ClientName, &rec.Kind, &rec.Scope, &rec.KeyPrefix,
		&expiresAt, &rec.MaxUses, &rec.Uses, &rec.ServerID, &rec.IssuedBy,
		&createdAt, &lastSeen, &revokedAt,
	)
	if err != nil {
		if err.Error() == "no rows in result set" {
			return APIKeyRecord{}, ErrAPIKeyNotFound
		}
		return APIKeyRecord{}, err
	}
	rec.ExpiresAt = time.Unix(expiresAt, 0)
	rec.CreatedAt = time.Unix(createdAt, 0)
	rec.LastSeenAt = time.Unix(lastSeen, 0)
	rec.Revoked = revokedAt != 0
	if rec.Revoked {
		return rec, ErrAPIKeyRevoked
	}
	return rec, nil
}

// ConsumeEnrollCode atomically increments uses and returns the enrollment code record for Postgres.
func (pg *PostgresDB) ConsumeEnrollCode(codeHash string) (APIKeyRecord, error) {
	if err := pg.ensureAPIKeys(); err != nil {
		return APIKeyRecord{}, err
	}
	now := time.Now().Unix()
	var rec APIKeyRecord
	var expiresAt, createdAt, lastSeen, revokedAt int64
	err := pg.pool.QueryRow(context.Background(),
		`UPDATE api_keys
		 SET uses = uses + 1
		 WHERE key_hash = $1
		   AND revoked_at = 0
		   AND kind = 'enroll'
		   AND (expires_at = 0 OR expires_at > $2)
		   AND (max_uses = 0 OR uses < max_uses)
		 RETURNING id, key_hash, tenant_id, client_name, kind, scope, key_prefix,
		           expires_at, max_uses, uses, server_id, issued_by,
		           created_at, last_seen_at, revoked_at`,
		codeHash, now,
	).Scan(
		&rec.ID, &rec.KeyHash, &rec.TenantID, &rec.ClientName, &rec.Kind, &rec.Scope, &rec.KeyPrefix,
		&expiresAt, &rec.MaxUses, &rec.Uses, &rec.ServerID, &rec.IssuedBy,
		&createdAt, &lastSeen, &revokedAt,
	)
	if err != nil {
		if err.Error() == "no rows in result set" {
			return APIKeyRecord{}, ErrAPIKeyNotFound
		}
		return APIKeyRecord{}, err
	}
	rec.ExpiresAt = time.Unix(expiresAt, 0)
	rec.CreatedAt = time.Unix(createdAt, 0)
	rec.LastSeenAt = time.Unix(lastSeen, 0)
	return rec, nil
}

// TouchAPIKey records that a key was just used and drops handshake plaintext.
func (pg *PostgresDB) TouchAPIKey(keyHash string) error {
	if err := pg.ensureAPIKeys(); err != nil {
		return err
	}
	ctx := context.Background()
	now := time.Now().Unix()
	if _, err := pg.pool.Exec(ctx,
		"UPDATE api_keys SET last_seen_at = $1 WHERE key_hash = $2", now, keyHash); err != nil {
		return err
	}
	_, err := pg.pool.Exec(ctx, "DELETE FROM enrollment_handshakes WHERE key_hash = $1", keyHash)
	return err
}

// UpsertAPIKey registers or updates a client credential in Postgres.
func (pg *PostgresDB) UpsertAPIKey(rec APIKeyRecord) error {
	if err := pg.ensureAPIKeys(); err != nil {
		return err
	}
	if rec.KeyHash == "" || rec.TenantID == "" {
		return errors.New("storage: api key record needs KeyHash and TenantID")
	}
	if rec.Kind == "" || rec.Scope == "" {
		return ErrMissingCredentialFields
	}
	created := rec.CreatedAt
	if created.IsZero() {
		created = time.Now()
	}
	var expiresAt int64
	if !rec.ExpiresAt.IsZero() {
		expiresAt = rec.ExpiresAt.Unix()
	}
	_, err := pg.pool.Exec(context.Background(),
		`INSERT INTO api_keys(
			key_hash, tenant_id, client_name, kind, scope, key_prefix,
			expires_at, max_uses, uses, server_id, issued_by,
			created_at, last_seen_at, revoked_at
		 ) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, 0, 0)
		 ON CONFLICT(key_hash) DO UPDATE SET
			tenant_id = EXCLUDED.tenant_id,
			client_name = EXCLUDED.client_name,
			kind = EXCLUDED.kind,
			scope = EXCLUDED.scope,
			key_prefix = EXCLUDED.key_prefix,
			expires_at = EXCLUDED.expires_at,
			max_uses = EXCLUDED.max_uses,
			server_id = EXCLUDED.server_id,
			issued_by = EXCLUDED.issued_by`,
		rec.KeyHash, rec.TenantID, rec.ClientName, rec.Kind, rec.Scope, rec.KeyPrefix,
		expiresAt, rec.MaxUses, rec.Uses, rec.ServerID, rec.IssuedBy,
		created.Unix(),
	)
	return err
}

// RevokeAPIKey marks every key belonging to a client as revoked.
func (pg *PostgresDB) RevokeAPIKey(clientName string) (int64, error) {
	if err := pg.ensureAPIKeys(); err != nil {
		return 0, err
	}
	ctx := context.Background()
	now := time.Now().Unix()
	tag, err := pg.pool.Exec(ctx,
		"UPDATE api_keys SET revoked_at = $1 WHERE client_name = $2 AND revoked_at = 0", now, clientName)
	if err != nil {
		return 0, err
	}
	n := tag.RowsAffected()
	if n > 0 {
		if err := bumpAuthEpochPostgres(ctx, pg, now); err != nil {
			return n, err
		}
	}
	return n, nil
}

// RevokeAgent marks a machine agent token as revoked.
func (pg *PostgresDB) RevokeAgent(tenantID, serverID string) (int64, error) {
	if err := pg.ensureAPIKeys(); err != nil {
		return 0, err
	}
	ctx := context.Background()
	now := time.Now().Unix()
	tag, err := pg.pool.Exec(ctx,
		"UPDATE api_keys SET revoked_at = $1 WHERE tenant_id = $2 AND server_id = $3 AND kind = 'agent' AND revoked_at = 0",
		now, tenantID, serverID,
	)
	if err != nil {
		return 0, err
	}
	n := tag.RowsAffected()
	if n > 0 {
		if err := bumpAuthEpochPostgres(ctx, pg, now); err != nil {
			return n, err
		}
	}
	return n, nil
}

// ListAgents returns all registered machine agent tokens.
func (pg *PostgresDB) ListAgents(tenantID string) ([]APIKeyRecord, error) {
	if err := pg.ensureAPIKeys(); err != nil {
		return nil, err
	}
	query := `
		SELECT id, key_hash, tenant_id, client_name, kind, scope, key_prefix,
		       expires_at, max_uses, uses, server_id, issued_by,
		       created_at, last_seen_at, revoked_at
		FROM api_keys
		WHERE kind = 'agent'
	`
	var args []any
	if tenantID != "" {
		query += " AND tenant_id = $1"
		args = append(args, tenantID)
	}
	query += " ORDER BY last_seen_at DESC, created_at DESC"
	rows, err := pg.pool.Query(context.Background(), query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []APIKeyRecord
	for rows.Next() {
		var rec APIKeyRecord
		var expiresAt, createdAt, lastSeen, revokedAt int64
		if err := rows.Scan(
			&rec.ID, &rec.KeyHash, &rec.TenantID, &rec.ClientName, &rec.Kind, &rec.Scope, &rec.KeyPrefix,
			&expiresAt, &rec.MaxUses, &rec.Uses, &rec.ServerID, &rec.IssuedBy,
			&createdAt, &lastSeen, &revokedAt,
		); err != nil {
			return nil, err
		}
		rec.ExpiresAt = time.Unix(expiresAt, 0)
		rec.CreatedAt = time.Unix(createdAt, 0)
		rec.LastSeenAt = time.Unix(lastSeen, 0)
		rec.Revoked = revokedAt != 0
		out = append(out, rec)
	}
	return out, rows.Err()
}

// ListAPIKeys returns all registered credentials.
func (pg *PostgresDB) ListAPIKeys() ([]APIKeyRecord, error) {
	if err := pg.ensureAPIKeys(); err != nil {
		return nil, err
	}
	rows, err := pg.pool.Query(context.Background(),
		`SELECT id, key_hash, tenant_id, client_name, kind, scope, key_prefix,
		        expires_at, max_uses, uses, server_id, issued_by,
		        created_at, last_seen_at, revoked_at
		 FROM api_keys ORDER BY client_name, created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []APIKeyRecord
	for rows.Next() {
		var rec APIKeyRecord
		var expiresAt, createdAt, lastSeen, revokedAt int64
		if err := rows.Scan(
			&rec.ID, &rec.KeyHash, &rec.TenantID, &rec.ClientName, &rec.Kind, &rec.Scope, &rec.KeyPrefix,
			&expiresAt, &rec.MaxUses, &rec.Uses, &rec.ServerID, &rec.IssuedBy,
			&createdAt, &lastSeen, &revokedAt,
		); err != nil {
			return nil, err
		}
		rec.ExpiresAt = time.Unix(expiresAt, 0)
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

// UniqueTenantForClientName returns the single tenant bound to a display name.
// Zero matches returns ("", nil). Two or more distinct tenant IDs is an error:
// display names never resolve identity when they are ambiguous.
func UniqueTenantForClientName(keys []APIKeyRecord, name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", errors.New("storage: client name is required")
	}
	seen := make(map[string]struct{})
	for _, k := range keys {
		if !strings.EqualFold(k.ClientName, name) || k.TenantID == "" {
			continue
		}
		seen[k.TenantID] = struct{}{}
	}
	switch len(seen) {
	case 0:
		return "", nil
	case 1:
		for id := range seen {
			return id, nil
		}
	}
	return "", ErrAmbiguousClientName
}

// IsOpaqueTenantID reports whether tenantID already uses the t_<hex> form
// (or the reserved platform-admin tenant).
func IsOpaqueTenantID(tenantID string) bool {
	if tenantID == "" || tenantID == "admin" {
		return true
	}
	if !strings.HasPrefix(tenantID, "t_") {
		return false
	}
	hexPart := tenantID[2:]
	if len(hexPart) < 16 {
		return false
	}
	for _, c := range hexPart {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
			return false
		}
	}
	return true
}
