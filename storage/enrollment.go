package storage

import (
	"database/sql"
	"errors"
	"strings"
	"time"
)

// HandshakeTTL is how long a newly issued agent token may be recovered after a
// lost HTTP reply. After this window (or after first ingest TouchAPIKey), the
// plaintext is gone and replacement needs current_token or operator approval.
const HandshakeTTL = 5 * time.Minute

// AuthRevocationMaxDelay is the documented worst-case Hub replica delay before
// a cached acceptance is re-checked against auth_epoch. Local revoke/rotate
// invalidates immediately on the mutating instance.
const AuthRevocationMaxDelay = 5 * time.Second

var (
	// ErrRotationRequiresProof is returned when a server_id already has an
	// active agent token and the caller did not prove possession or operator approval.
	ErrRotationRequiresProof = errors.New("storage: replacement requires current token or operator approval")
	// ErrEnrollmentConflict is returned when a concurrent enrollment wins the
	// one-active-agent constraint; the whole transaction is rolled back.
	ErrEnrollmentConflict = errors.New("storage: enrollment conflict")
)

// EnrollmentRequest is the transactional consume-and-issue payload.
type EnrollmentRequest struct {
	CodeHash         string
	ServerID         string
	CurrentTokenHash string
	OperatorApproved bool
	Token            string
	TokenHash        string
	TokenPrefix      string
}

// EnrollmentResult is the committed handshake outcome.
type EnrollmentResult struct {
	TenantID      string
	ClientName    string
	ServerID      string
	Token         string
	TokenHash     string
	Recovered     bool
	RevokedHashes []string
}

func ensureEnrollmentAuxSQLite(conn *sql.DB) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS enrollment_handshakes (
			code_hash  TEXT NOT NULL,
			server_id  TEXT NOT NULL,
			tenant_id  TEXT NOT NULL,
			token      TEXT NOT NULL,
			key_hash   TEXT NOT NULL,
			created_at INTEGER NOT NULL,
			expires_at INTEGER NOT NULL,
			PRIMARY KEY (code_hash, server_id)
		)`,
		`CREATE TABLE IF NOT EXISTS auth_epoch (
			id INTEGER PRIMARY KEY CHECK (id = 1),
			epoch INTEGER NOT NULL DEFAULT 0,
			updated_at INTEGER NOT NULL DEFAULT 0
		)`,
		`INSERT OR IGNORE INTO auth_epoch(id, epoch, updated_at) VALUES (1, 0, 0)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_api_keys_one_active_agent
			ON api_keys(tenant_id, server_id)
			WHERE kind = 'agent' AND revoked_at = 0 AND server_id != ''`,
	}
	for _, q := range stmts {
		if _, err := conn.Exec(q); err != nil {
			return err
		}
	}
	return nil
}

func bumpAuthEpochSQLite(exec interface {
	Exec(query string, args ...any) (sql.Result, error)
}, now int64) error {
	_, err := exec.Exec(`UPDATE auth_epoch SET epoch = epoch + 1, updated_at = ? WHERE id = 1`, now)
	return err
}

func isUniqueConstraint(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "UNIQUE constraint failed") ||
		strings.Contains(msg, "unique constraint") ||
		strings.Contains(msg, "SQLSTATE 23505")
}

// AuthEpoch returns the credential revocation/version counter shared by Hub replicas.
func (db *DB) AuthEpoch() (int64, error) {
	if err := db.ensureAPIKeys(); err != nil {
		return 0, err
	}
	var ep int64
	err := db.conn.QueryRow(`SELECT epoch FROM auth_epoch WHERE id = 1`).Scan(&ep)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return ep, err
}

// ActiveAgentHashes returns hashes of currently active agent tokens for an identity.
func (db *DB) ActiveAgentHashes(tenantID, serverID string) ([]string, error) {
	if err := db.ensureAPIKeys(); err != nil {
		return nil, err
	}
	rows, err := db.conn.Query(
		`SELECT key_hash FROM api_keys WHERE tenant_id = ? AND server_id = ? AND kind = 'agent' AND revoked_at = 0`,
		tenantID, serverID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// ExpireEnrollmentHandshakes marks recovery rows expired (tests and maintenance).
func (db *DB) ExpireEnrollmentHandshakes() error {
	if err := db.ensureAPIKeys(); err != nil {
		return err
	}
	_, err := db.conn.Exec(`UPDATE enrollment_handshakes SET expires_at = 1`)
	return err
}

func scanAPIKey(row interface {
	Scan(dest ...any) error
}) (APIKeyRecord, int64, int64, error) {
	var rec APIKeyRecord
	var expiresAt, createdAt, lastSeen, revokedAt int64
	err := row.Scan(
		&rec.ID, &rec.KeyHash, &rec.TenantID, &rec.ClientName, &rec.Kind, &rec.Scope, &rec.KeyPrefix,
		&expiresAt, &rec.MaxUses, &rec.Uses, &rec.ServerID, &rec.IssuedBy,
		&createdAt, &lastSeen, &revokedAt,
	)
	if err != nil {
		return APIKeyRecord{}, 0, 0, err
	}
	rec.ExpiresAt = time.Unix(expiresAt, 0)
	rec.CreatedAt = time.Unix(createdAt, 0)
	rec.LastSeenAt = time.Unix(lastSeen, 0)
	rec.Revoked = revokedAt != 0
	return rec, expiresAt, revokedAt, nil
}

// CompleteEnrollment consumes an enroll code and issues an agent token in one
// transaction. First enrollment of a server_id needs only the code. Replacement
// needs CurrentTokenHash matching the active agent, or OperatorApproved.
// A retry inside HandshakeTTL with the same code+server_id returns the same token.
func (db *DB) CompleteEnrollment(req EnrollmentRequest) (EnrollmentResult, error) {
	if err := db.ensureAPIKeys(); err != nil {
		return EnrollmentResult{}, err
	}
	if req.CodeHash == "" || req.ServerID == "" {
		return EnrollmentResult{}, errors.New("storage: enrollment needs code and server_id")
	}
	if req.Token == "" || req.TokenHash == "" {
		return EnrollmentResult{}, errors.New("storage: enrollment needs issued token")
	}

	tx, err := db.conn.Begin()
	if err != nil {
		return EnrollmentResult{}, err
	}
	defer tx.Rollback()

	now := time.Now().Unix()
	if _, err := tx.Exec(`UPDATE api_keys SET client_name = client_name WHERE key_hash = ? AND kind = 'enroll'`, req.CodeHash); err != nil {
		return EnrollmentResult{}, err
	}

	var hsToken, hsHash, hsTenant, hsClient string
	err = tx.QueryRow(
		`SELECT h.token, h.key_hash, h.tenant_id, k.client_name
		 FROM enrollment_handshakes h
		 JOIN api_keys k ON k.key_hash = h.code_hash
		 WHERE h.code_hash = ? AND h.server_id = ? AND h.expires_at > ?`,
		req.CodeHash, req.ServerID, now,
	).Scan(&hsToken, &hsHash, &hsTenant, &hsClient)
	if err == nil {
		if err := tx.Commit(); err != nil {
			return EnrollmentResult{}, err
		}
		return EnrollmentResult{
			TenantID:   hsTenant,
			ClientName: hsClient,
			ServerID:   req.ServerID,
			Token:      hsToken,
			TokenHash:  hsHash,
			Recovered:  true,
		}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return EnrollmentResult{}, err
	}

	rec, expiresAt, revokedAt, err := scanAPIKey(tx.QueryRow(
		`SELECT id, key_hash, tenant_id, client_name, kind, scope, key_prefix,
		        expires_at, max_uses, uses, server_id, issued_by,
		        created_at, last_seen_at, revoked_at
		 FROM api_keys WHERE key_hash = ? AND kind = 'enroll'`, req.CodeHash,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return EnrollmentResult{}, ErrAPIKeyNotFound
	}
	if err != nil {
		return EnrollmentResult{}, err
	}
	if rec.Revoked || revokedAt != 0 || (expiresAt > 0 && now > expiresAt) || (rec.MaxUses > 0 && rec.Uses >= rec.MaxUses) {
		return EnrollmentResult{}, ErrAPIKeyNotFound
	}

	active, err := listActiveAgentHashesTx(tx, rec.TenantID, req.ServerID)
	if err != nil {
		return EnrollmentResult{}, err
	}
	if len(active) > 0 && !replacementAllowed(active, req.CurrentTokenHash, req.OperatorApproved) {
		return EnrollmentResult{}, ErrRotationRequiresProof
	}

	res, err := tx.Exec(
		`UPDATE api_keys SET uses = uses + 1
		 WHERE key_hash = ? AND revoked_at = 0 AND kind = 'enroll'
		   AND (expires_at = 0 OR expires_at > ?)
		   AND (max_uses = 0 OR uses < max_uses)`,
		req.CodeHash, now,
	)
	if err != nil {
		return EnrollmentResult{}, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return EnrollmentResult{}, err
	}
	if n != 1 {
		return EnrollmentResult{}, ErrAPIKeyNotFound
	}

	var revoked []string
	if len(active) > 0 {
		revoked = append([]string(nil), active...)
		if _, err := tx.Exec(
			`UPDATE api_keys SET revoked_at = ? WHERE tenant_id = ? AND server_id = ? AND kind = 'agent' AND revoked_at = 0`,
			now, rec.TenantID, req.ServerID,
		); err != nil {
			return EnrollmentResult{}, err
		}
	}

	if _, err := tx.Exec(
		`INSERT INTO api_keys(
			key_hash, tenant_id, client_name, kind, scope, key_prefix,
			expires_at, max_uses, uses, server_id, issued_by,
			created_at, last_seen_at, revoked_at
		 ) VALUES (?, ?, ?, ?, ?, ?, 0, 0, 0, ?, ?, ?, 0, 0)`,
		req.TokenHash, rec.TenantID, rec.ClientName, KindAgent, ScopeIngest, req.TokenPrefix,
		req.ServerID, "enroll:"+rec.ClientName, now,
	); err != nil {
		if isUniqueConstraint(err) {
			return EnrollmentResult{}, ErrEnrollmentConflict
		}
		return EnrollmentResult{}, err
	}

	exp := now + int64(HandshakeTTL.Seconds())
	if _, err := tx.Exec(
		`INSERT INTO enrollment_handshakes(code_hash, server_id, tenant_id, token, key_hash, created_at, expires_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(code_hash, server_id) DO UPDATE SET
			token = excluded.token,
			key_hash = excluded.key_hash,
			tenant_id = excluded.tenant_id,
			created_at = excluded.created_at,
			expires_at = excluded.expires_at`,
		req.CodeHash, req.ServerID, rec.TenantID, req.Token, req.TokenHash, now, exp,
	); err != nil {
		return EnrollmentResult{}, err
	}

	if err := bumpAuthEpochSQLite(tx, now); err != nil {
		return EnrollmentResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return EnrollmentResult{}, err
	}
	return EnrollmentResult{
		TenantID:      rec.TenantID,
		ClientName:    rec.ClientName,
		ServerID:      req.ServerID,
		Token:         req.Token,
		TokenHash:     req.TokenHash,
		RevokedHashes: revoked,
	}, nil
}

// ReplaceAgent is the operator-approved rotation path: no enroll code is consumed.
func (db *DB) ReplaceAgent(tenantID, serverID, token, tokenHash, prefix string) (EnrollmentResult, error) {
	if err := db.ensureAPIKeys(); err != nil {
		return EnrollmentResult{}, err
	}
	if tenantID == "" || serverID == "" || token == "" || tokenHash == "" {
		return EnrollmentResult{}, errors.New("storage: replace agent needs tenant, server and token")
	}
	tx, err := db.conn.Begin()
	if err != nil {
		return EnrollmentResult{}, err
	}
	defer tx.Rollback()

	now := time.Now().Unix()
	if _, err := tx.Exec(
		`UPDATE api_keys SET client_name = client_name WHERE tenant_id = ? AND server_id = ? AND kind = 'agent' AND revoked_at = 0`,
		tenantID, serverID,
	); err != nil {
		return EnrollmentResult{}, err
	}

	active, err := listActiveAgentHashesTx(tx, tenantID, serverID)
	if err != nil {
		return EnrollmentResult{}, err
	}
	if len(active) == 0 {
		return EnrollmentResult{}, ErrAPIKeyNotFound
	}

	var clientName string
	_ = tx.QueryRow(
		`SELECT client_name FROM api_keys WHERE tenant_id = ? AND server_id = ? AND kind = 'agent' LIMIT 1`,
		tenantID, serverID,
	).Scan(&clientName)

	if _, err := tx.Exec(
		`UPDATE api_keys SET revoked_at = ? WHERE tenant_id = ? AND server_id = ? AND kind = 'agent' AND revoked_at = 0`,
		now, tenantID, serverID,
	); err != nil {
		return EnrollmentResult{}, err
	}
	if _, err := tx.Exec(
		`INSERT INTO api_keys(
			key_hash, tenant_id, client_name, kind, scope, key_prefix,
			expires_at, max_uses, uses, server_id, issued_by,
			created_at, last_seen_at, revoked_at
		 ) VALUES (?, ?, ?, ?, ?, ?, 0, 0, 0, ?, 'operator-replace', ?, 0, 0)`,
		tokenHash, tenantID, clientName, KindAgent, ScopeIngest, prefix, serverID, now,
	); err != nil {
		if isUniqueConstraint(err) {
			return EnrollmentResult{}, ErrEnrollmentConflict
		}
		return EnrollmentResult{}, err
	}
	if err := bumpAuthEpochSQLite(tx, now); err != nil {
		return EnrollmentResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return EnrollmentResult{}, err
	}
	return EnrollmentResult{
		TenantID:      tenantID,
		ClientName:    clientName,
		ServerID:      serverID,
		Token:         token,
		TokenHash:     tokenHash,
		RevokedHashes: active,
	}, nil
}

func listActiveAgentHashesTx(tx *sql.Tx, tenantID, serverID string) ([]string, error) {
	rows, err := tx.Query(
		`SELECT key_hash FROM api_keys WHERE tenant_id = ? AND server_id = ? AND kind = 'agent' AND revoked_at = 0`,
		tenantID, serverID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var h string
		if err := rows.Scan(&h); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

func replacementAllowed(active []string, currentHash string, operator bool) bool {
	if operator {
		return true
	}
	if currentHash == "" {
		return false
	}
	for _, h := range active {
		if h == currentHash {
			return true
		}
	}
	return false
}
