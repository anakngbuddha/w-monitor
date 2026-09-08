package storage

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func ensureEnrollmentAuxPostgres(ctx context.Context, pg *PostgresDB) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS enrollment_handshakes (
			code_hash  TEXT NOT NULL,
			server_id  TEXT NOT NULL,
			tenant_id  TEXT NOT NULL,
			token      TEXT NOT NULL,
			key_hash   TEXT NOT NULL,
			created_at BIGINT NOT NULL,
			expires_at BIGINT NOT NULL,
			PRIMARY KEY (code_hash, server_id)
		)`,
		`CREATE TABLE IF NOT EXISTS auth_epoch (
			id INTEGER PRIMARY KEY CHECK (id = 1),
			epoch BIGINT NOT NULL DEFAULT 0,
			updated_at BIGINT NOT NULL DEFAULT 0
		)`,
		`INSERT INTO auth_epoch(id, epoch, updated_at) VALUES (1, 0, 0) ON CONFLICT (id) DO NOTHING`,
		`CREATE UNIQUE INDEX IF NOT EXISTS idx_api_keys_one_active_agent
			ON api_keys(tenant_id, server_id)
			WHERE kind = 'agent' AND revoked_at = 0 AND server_id != ''`,
	}
	for _, q := range stmts {
		if _, err := pg.pool.Exec(ctx, q); err != nil {
			return err
		}
	}
	return nil
}

func bumpAuthEpochPostgres(ctx context.Context, pg *PostgresDB, now int64) error {
	_, err := pg.pool.Exec(ctx, `UPDATE auth_epoch SET epoch = epoch + 1, updated_at = $1 WHERE id = 1`, now)
	return err
}

func bumpAuthEpochPgTx(ctx context.Context, tx pgx.Tx, now int64) error {
	_, err := tx.Exec(ctx, `UPDATE auth_epoch SET epoch = epoch + 1, updated_at = $1 WHERE id = 1`, now)
	return err
}

func isPGUnique(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// AuthEpoch returns the credential revocation/version counter.
func (pg *PostgresDB) AuthEpoch() (int64, error) {
	if err := pg.ensureAPIKeys(); err != nil {
		return 0, err
	}
	var ep int64
	err := pg.pool.QueryRow(context.Background(), `SELECT epoch FROM auth_epoch WHERE id = 1`).Scan(&ep)
	if err != nil {
		if err.Error() == "no rows in result set" {
			return 0, nil
		}
		return 0, err
	}
	return ep, nil
}

// ActiveAgentHashes returns hashes of currently active agent tokens.
func (pg *PostgresDB) ActiveAgentHashes(tenantID, serverID string) ([]string, error) {
	if err := pg.ensureAPIKeys(); err != nil {
		return nil, err
	}
	rows, err := pg.pool.Query(context.Background(),
		`SELECT key_hash FROM api_keys WHERE tenant_id = $1 AND server_id = $2 AND kind = 'agent' AND revoked_at = 0`,
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

// ExpireEnrollmentHandshakes marks recovery rows expired.
func (pg *PostgresDB) ExpireEnrollmentHandshakes() error {
	if err := pg.ensureAPIKeys(); err != nil {
		return err
	}
	_, err := pg.pool.Exec(context.Background(), `UPDATE enrollment_handshakes SET expires_at = 1`)
	return err
}

func scanAPIKeyPG(row pgx.Row) (APIKeyRecord, int64, int64, error) {
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

// CompleteEnrollment is the Postgres consume-and-issue transaction.
func (pg *PostgresDB) CompleteEnrollment(req EnrollmentRequest) (EnrollmentResult, error) {
	if err := pg.ensureAPIKeys(); err != nil {
		return EnrollmentResult{}, err
	}
	if req.CodeHash == "" || req.ServerID == "" {
		return EnrollmentResult{}, errors.New("storage: enrollment needs code and server_id")
	}
	if req.Token == "" || req.TokenHash == "" {
		return EnrollmentResult{}, errors.New("storage: enrollment needs issued token")
	}

	ctx := context.Background()
	tx, err := pg.pool.Begin(ctx)
	if err != nil {
		return EnrollmentResult{}, err
	}
	defer tx.Rollback(ctx)

	now := time.Now().Unix()
	if _, err := tx.Exec(ctx, `SELECT 1 FROM api_keys WHERE key_hash = $1 AND kind = 'enroll' FOR UPDATE`, req.CodeHash); err != nil {
		return EnrollmentResult{}, err
	}

	var hsToken, hsHash, hsTenant, hsClient string
	err = tx.QueryRow(ctx,
		`SELECT h.token, h.key_hash, h.tenant_id, k.client_name
		 FROM enrollment_handshakes h
		 JOIN api_keys k ON k.key_hash = h.code_hash
		 WHERE h.code_hash = $1 AND h.server_id = $2 AND h.expires_at > $3`,
		req.CodeHash, req.ServerID, now,
	).Scan(&hsToken, &hsHash, &hsTenant, &hsClient)
	if err == nil {
		if err := tx.Commit(ctx); err != nil {
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
	if !errors.Is(err, pgx.ErrNoRows) {
		return EnrollmentResult{}, err
	}

	rec, expiresAt, revokedAt, err := scanAPIKeyPG(tx.QueryRow(ctx,
		`SELECT id, key_hash, tenant_id, client_name, kind, scope, key_prefix,
		        expires_at, max_uses, uses, server_id, issued_by,
		        created_at, last_seen_at, revoked_at
		 FROM api_keys WHERE key_hash = $1 AND kind = 'enroll'`, req.CodeHash,
	))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) || err.Error() == "no rows in result set" {
			return EnrollmentResult{}, ErrAPIKeyNotFound
		}
		return EnrollmentResult{}, err
	}
	if rec.Revoked || revokedAt != 0 || (expiresAt > 0 && now > expiresAt) || (rec.MaxUses > 0 && rec.Uses >= rec.MaxUses) {
		return EnrollmentResult{}, ErrAPIKeyNotFound
	}

	active, err := listActiveAgentHashesPgTx(ctx, tx, rec.TenantID, req.ServerID)
	if err != nil {
		return EnrollmentResult{}, err
	}
	if len(active) > 0 && !replacementAllowed(active, req.CurrentTokenHash, req.OperatorApproved) {
		return EnrollmentResult{}, ErrRotationRequiresProof
	}

	tag, err := tx.Exec(ctx,
		`UPDATE api_keys SET uses = uses + 1
		 WHERE key_hash = $1 AND revoked_at = 0 AND kind = 'enroll'
		   AND (expires_at = 0 OR expires_at > $2)
		   AND (max_uses = 0 OR uses < max_uses)`,
		req.CodeHash, now,
	)
	if err != nil {
		return EnrollmentResult{}, err
	}
	if tag.RowsAffected() != 1 {
		return EnrollmentResult{}, ErrAPIKeyNotFound
	}

	var revoked []string
	if len(active) > 0 {
		revoked = append([]string(nil), active...)
		if _, err := tx.Exec(ctx,
			`UPDATE api_keys SET revoked_at = $1 WHERE tenant_id = $2 AND server_id = $3 AND kind = 'agent' AND revoked_at = 0`,
			now, rec.TenantID, req.ServerID,
		); err != nil {
			return EnrollmentResult{}, err
		}
	}

	if _, err := tx.Exec(ctx,
		`INSERT INTO api_keys(
			key_hash, tenant_id, client_name, kind, scope, key_prefix,
			expires_at, max_uses, uses, server_id, issued_by,
			created_at, last_seen_at, revoked_at
		 ) VALUES ($1, $2, $3, $4, $5, $6, 0, 0, 0, $7, $8, $9, 0, 0)`,
		req.TokenHash, rec.TenantID, rec.ClientName, KindAgent, ScopeIngest, req.TokenPrefix,
		req.ServerID, "enroll:"+rec.ClientName, now,
	); err != nil {
		if isPGUnique(err) {
			return EnrollmentResult{}, ErrEnrollmentConflict
		}
		return EnrollmentResult{}, err
	}

	exp := now + int64(HandshakeTTL.Seconds())
	if _, err := tx.Exec(ctx,
		`INSERT INTO enrollment_handshakes(code_hash, server_id, tenant_id, token, key_hash, created_at, expires_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)
		 ON CONFLICT (code_hash, server_id) DO UPDATE SET
			token = EXCLUDED.token,
			key_hash = EXCLUDED.key_hash,
			tenant_id = EXCLUDED.tenant_id,
			created_at = EXCLUDED.created_at,
			expires_at = EXCLUDED.expires_at`,
		req.CodeHash, req.ServerID, rec.TenantID, req.Token, req.TokenHash, now, exp,
	); err != nil {
		return EnrollmentResult{}, err
	}

	if err := bumpAuthEpochPgTx(ctx, tx, now); err != nil {
		return EnrollmentResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
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

// ReplaceAgent is the operator-approved rotation path on Postgres.
func (pg *PostgresDB) ReplaceAgent(tenantID, serverID, token, tokenHash, prefix string) (EnrollmentResult, error) {
	if err := pg.ensureAPIKeys(); err != nil {
		return EnrollmentResult{}, err
	}
	if tenantID == "" || serverID == "" || token == "" || tokenHash == "" {
		return EnrollmentResult{}, errors.New("storage: replace agent needs tenant, server and token")
	}
	ctx := context.Background()
	tx, err := pg.pool.Begin(ctx)
	if err != nil {
		return EnrollmentResult{}, err
	}
	defer tx.Rollback(ctx)

	now := time.Now().Unix()
	if _, err := tx.Exec(ctx,
		`SELECT 1 FROM api_keys WHERE tenant_id = $1 AND server_id = $2 AND kind = 'agent' AND revoked_at = 0 FOR UPDATE`,
		tenantID, serverID,
	); err != nil {
		return EnrollmentResult{}, err
	}

	active, err := listActiveAgentHashesPgTx(ctx, tx, tenantID, serverID)
	if err != nil {
		return EnrollmentResult{}, err
	}
	if len(active) == 0 {
		return EnrollmentResult{}, ErrAPIKeyNotFound
	}

	var clientName string
	_ = tx.QueryRow(ctx,
		`SELECT client_name FROM api_keys WHERE tenant_id = $1 AND server_id = $2 AND kind = 'agent' LIMIT 1`,
		tenantID, serverID,
	).Scan(&clientName)

	if _, err := tx.Exec(ctx,
		`UPDATE api_keys SET revoked_at = $1 WHERE tenant_id = $2 AND server_id = $3 AND kind = 'agent' AND revoked_at = 0`,
		now, tenantID, serverID,
	); err != nil {
		return EnrollmentResult{}, err
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO api_keys(
			key_hash, tenant_id, client_name, kind, scope, key_prefix,
			expires_at, max_uses, uses, server_id, issued_by,
			created_at, last_seen_at, revoked_at
		 ) VALUES ($1, $2, $3, $4, $5, $6, 0, 0, 0, $7, 'operator-replace', $8, 0, 0)`,
		tokenHash, tenantID, clientName, KindAgent, ScopeIngest, prefix, serverID, now,
	); err != nil {
		if isPGUnique(err) {
			return EnrollmentResult{}, ErrEnrollmentConflict
		}
		return EnrollmentResult{}, err
	}
	if err := bumpAuthEpochPgTx(ctx, tx, now); err != nil {
		return EnrollmentResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
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

func listActiveAgentHashesPgTx(ctx context.Context, tx pgx.Tx, tenantID, serverID string) ([]string, error) {
	rows, err := tx.Query(ctx,
		`SELECT key_hash FROM api_keys WHERE tenant_id = $1 AND server_id = $2 AND kind = 'agent' AND revoked_at = 0`,
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
