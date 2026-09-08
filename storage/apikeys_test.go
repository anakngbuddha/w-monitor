package storage

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testDB(t *testing.T) *DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "apikeys_test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestHashAPIKeyIsStableAndNotReversible(t *testing.T) {
	const key = "DveW8TMXZG+KLAj/WWKNyirgv+4NiIewp4HSUoBws3M="
	h1 := HashAPIKey(key)
	h2 := HashAPIKey(key)
	if h1 != h2 {
		t.Error("hash is not deterministic")
	}
	if len(h1) != 64 {
		t.Errorf("hash length = %d, want 64 hex chars", len(h1))
	}
	if h1 == key {
		t.Error("hash equals the plaintext key")
	}
	if HashAPIKey("other") == h1 {
		t.Error("different keys produced the same hash")
	}
}

func TestGenerateAPIKeyIsUnique(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 50; i++ {
		k, err := GenerateAPIKey()
		if err != nil {
			t.Fatalf("GenerateAPIKey: %v", err)
		}
		if seen[k] {
			t.Fatal("generated a duplicate key")
		}
		if len(k) < 32 {
			t.Fatalf("key too short: %q", k)
		}
		seen[k] = true
	}
}

func TestResolveAPIKeyRoundTrip(t *testing.T) {
	db := testDB(t)

	const plaintext = "super-secret-client-key"
	if err := db.UpsertAPIKey(APIKeyRecord{
		KeyHash:    HashAPIKey(plaintext),
		TenantID:   "t_abc123",
		ClientName: "WSI",
		Kind:       KindRead,
		Scope:      ScopeRead,
	}); err != nil {
		t.Fatalf("UpsertAPIKey: %v", err)
	}

	rec, err := db.ResolveAPIKey(HashAPIKey(plaintext))
	if err != nil {
		t.Fatalf("ResolveAPIKey: %v", err)
	}
	if rec.TenantID != "t_abc123" {
		t.Errorf("TenantID = %q, want t_abc123", rec.TenantID)
	}
	if rec.ClientName != "WSI" {
		t.Errorf("ClientName = %q, want WSI", rec.ClientName)
	}
}

// The whole point of A1: a key nobody registered must not resolve to anything.
func TestUnknownKeyIsRejected(t *testing.T) {
	db := testDB(t)

	_, err := db.ResolveAPIKey(HashAPIKey("i-just-made-this-up"))
	if !errors.Is(err, ErrAPIKeyNotFound) {
		t.Fatalf("expected ErrAPIKeyNotFound, got %v", err)
	}
}

func TestRevokedKeyIsRejected(t *testing.T) {
	db := testDB(t)

	const plaintext = "key-to-be-revoked"
	if err := db.UpsertAPIKey(APIKeyRecord{
		KeyHash:    HashAPIKey(plaintext),
		TenantID:   "t_revoke",
		ClientName: "DemoClient",
		Kind:       KindRead,
		Scope:      ScopeRead,
	}); err != nil {
		t.Fatalf("UpsertAPIKey: %v", err)
	}

	if _, err := db.ResolveAPIKey(HashAPIKey(plaintext)); err != nil {
		t.Fatalf("key should resolve before revocation: %v", err)
	}

	n, err := db.RevokeAPIKey("DemoClient")
	if err != nil {
		t.Fatalf("RevokeAPIKey: %v", err)
	}
	if n != 1 {
		t.Errorf("revoked %d keys, want 1", n)
	}

	if _, err := db.ResolveAPIKey(HashAPIKey(plaintext)); !errors.Is(err, ErrAPIKeyNotFound) {
		t.Fatalf("revoked key still resolves: %v", err)
	}
}

func TestListAPIKeysNeverExposesPlaintext(t *testing.T) {
	db := testDB(t)

	const plaintext = "do-not-leak-me"
	db.UpsertAPIKey(APIKeyRecord{
		KeyHash:    HashAPIKey(plaintext),
		TenantID:   "t_1",
		ClientName: "ClientOne",
		Kind:       KindRead,
		Scope:      ScopeRead,
	})

	keys, err := db.ListAPIKeys()
	if err != nil {
		t.Fatalf("ListAPIKeys: %v", err)
	}
	if len(keys) != 1 {
		t.Fatalf("got %d keys, want 1", len(keys))
	}
	if keys[0].KeyHash == plaintext {
		t.Error("stored value is the plaintext key")
	}
	if keys[0].KeyHash != HashAPIKey(plaintext) {
		t.Error("stored hash does not match")
	}
}

func TestUpsertIsIdempotent(t *testing.T) {
	db := testDB(t)
	rec := APIKeyRecord{
		KeyHash:    HashAPIKey("same-key"),
		TenantID:   "t_same",
		ClientName: "Repeat",
		Kind:       KindRead,
		Scope:      ScopeRead,
	}
	for i := 0; i < 3; i++ {
		if err := db.UpsertAPIKey(rec); err != nil {
			t.Fatalf("UpsertAPIKey attempt %d: %v", i, err)
		}
	}
	keys, _ := db.ListAPIKeys()
	if len(keys) != 1 {
		t.Errorf("got %d rows after 3 upserts, want 1", len(keys))
	}
}

func TestUpsertRejectsIncompleteRecords(t *testing.T) {
	db := testDB(t)
	if err := db.UpsertAPIKey(APIKeyRecord{TenantID: "t_1"}); err == nil {
		t.Error("expected error for missing KeyHash")
	}
	if err := db.UpsertAPIKey(APIKeyRecord{KeyHash: "abc"}); err == nil {
		t.Error("expected error for missing TenantID")
	}
	if err := db.UpsertAPIKey(APIKeyRecord{KeyHash: "abc", TenantID: "t_1"}); err == nil {
		t.Error("expected error for missing Kind and Scope")
	}
}

func TestTouchAPIKeyUpdatesLastSeen(t *testing.T) {
	db := testDB(t)
	hash := HashAPIKey("touch-me")
	db.UpsertAPIKey(APIKeyRecord{KeyHash: hash, TenantID: "t_touch", ClientName: "Toucher", Kind: KindRead, Scope: ScopeRead})

	before, _ := db.ResolveAPIKey(hash)
	if err := db.TouchAPIKey(hash); err != nil {
		t.Fatalf("TouchAPIKey: %v", err)
	}
	after, _ := db.ResolveAPIKey(hash)

	if !after.LastSeenAt.After(before.LastSeenAt) {
		t.Errorf("last_seen_at not advanced: %v then %v", before.LastSeenAt, after.LastSeenAt)
	}
}

func TestPingSucceedsOnOpenDB(t *testing.T) {
	db := testDB(t)
	if err := db.Ping(context.Background()); err != nil {
		t.Errorf("Ping on open DB: %v", err)
	}
}

func TestNewTenantIDIsUnique(t *testing.T) {
	a, err := NewTenantID()
	if err != nil {
		t.Fatalf("NewTenantID: %v", err)
	}
	b, _ := NewTenantID()
	if a == b {
		t.Error("tenant IDs collided")
	}
	if len(a) < 10 {
		t.Errorf("tenant id too short: %q", a)
	}
}

func TestTokenGenerationAndPrefixes(t *testing.T) {
	agentToken, err := GenerateToken(KindAgent)
	if err != nil {
		t.Fatalf("GenerateToken(agent): %v", err)
	}
	if !strings.HasPrefix(agentToken, PrefixAgent) {
		t.Errorf("agent token = %s, want prefix %s", agentToken, PrefixAgent)
	}
	if ExtractKeyPrefix(agentToken) != PrefixAgent {
		t.Errorf("ExtractKeyPrefix = %s, want %s", ExtractKeyPrefix(agentToken), PrefixAgent)
	}

	readToken, err := GenerateToken(KindRead)
	if err != nil {
		t.Fatalf("GenerateToken(read): %v", err)
	}
	if !strings.HasPrefix(readToken, PrefixRead) {
		t.Errorf("read token = %s, want prefix %s", readToken, PrefixRead)
	}

	adminToken, err := GenerateToken(KindAdmin)
	if err != nil {
		t.Fatalf("GenerateToken(admin): %v", err)
	}
	if !strings.HasPrefix(adminToken, PrefixAdmin) {
		t.Errorf("admin token = %s, want prefix %s", adminToken, PrefixAdmin)
	}
}

func TestGenerateAndNormalizeEnrollCode(t *testing.T) {
	code, err := GenerateEnrollCode()
	if err != nil {
		t.Fatalf("GenerateEnrollCode: %v", err)
	}
	if !strings.HasPrefix(code, "WM-") {
		t.Errorf("code = %s, want WM- prefix", code)
	}
	if len(code) != 16 { // WM-XXXX-XXXX-XXXX = 3 + 4 + 1 + 4 + 1 + 4 = 16 (actually 3 + 4 + 1 + 4 + 1 + 4 - wait: "WM-" is 3, 4, "-", 4, "-", 4 = 3+4+1+4+1+4 = 17)
		if len(code) != 17 {
			t.Errorf("code length = %d, want 17", len(code))
		}
	}

	// Normalization
	rawInput := "wm-4f2k-9qx7-tr31"
	norm := NormalizeEnrollCode(rawInput)
	if norm != "WM-4F2K-9QX7-TR31" {
		t.Errorf("NormalizeEnrollCode(%q) = %q, want WM-4F2K-9QX7-TR31", rawInput, norm)
	}

	rawNoDashes := "wm4f2k9qx7tr31"
	normNoDashes := NormalizeEnrollCode(rawNoDashes)
	if normNoDashes != "WM-4F2K-9QX7-TR31" {
		t.Errorf("NormalizeEnrollCode(%q) = %q, want WM-4F2K-9QX7-TR31", rawNoDashes, normNoDashes)
	}
}

func TestConsumeEnrollCodeAtomicAndExhaustion(t *testing.T) {
	db := testDB(t)

	code, _ := GenerateEnrollCode()
	codeHash := HashAPIKey(code)

	// Register code with max_uses = 3
	if err := db.UpsertAPIKey(APIKeyRecord{
		KeyHash:    codeHash,
		TenantID:   "t_test_tenant",
		ClientName: "TestClient",
		Kind:       KindEnroll,
		Scope:      ScopeIngest,
		MaxUses:    3,
		ExpiresAt:  time.Now().Add(24 * time.Hour),
	}); err != nil {
		t.Fatalf("UpsertAPIKey: %v", err)
	}

	// Consume 3 times successfully
	for i := 1; i <= 3; i++ {
		rec, err := db.ConsumeEnrollCode(codeHash)
		if err != nil {
			t.Fatalf("ConsumeEnrollCode attempt %d failed: %v", i, err)
		}
		if rec.Uses != i {
			t.Errorf("rec.Uses = %d, want %d", rec.Uses, i)
		}
		if rec.TenantID != "t_test_tenant" {
			t.Errorf("rec.TenantID = %s, want t_test_tenant", rec.TenantID)
		}
	}

	// 4th attempt must be rejected as exhausted
	_, err := db.ConsumeEnrollCode(codeHash)
	if !errors.Is(err, ErrAPIKeyNotFound) {
		t.Fatalf("4th consume expected ErrAPIKeyNotFound, got %v", err)
	}
}

func TestRevokeAgentAndListAgents(t *testing.T) {
	db := testDB(t)

	agentKey1, _ := GenerateToken(KindAgent)
	agentKey2, _ := GenerateToken(KindAgent)

	db.UpsertAPIKey(APIKeyRecord{
		KeyHash:    HashAPIKey(agentKey1),
		TenantID:   "t_agent_test",
		ClientName: "ClientA",
		Kind:       KindAgent,
		Scope:      ScopeIngest,
		ServerID:   "SRV-01",
	})
	db.UpsertAPIKey(APIKeyRecord{
		KeyHash:    HashAPIKey(agentKey2),
		TenantID:   "t_agent_test",
		ClientName: "ClientA",
		Kind:       KindAgent,
		Scope:      ScopeIngest,
		ServerID:   "SRV-02",
	})

	agents, err := db.ListAgents("t_agent_test")
	if err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	if len(agents) != 2 {
		t.Fatalf("got %d agents, want 2", len(agents))
	}

	// Revoke SRV-01
	revoked, err := db.RevokeAgent("t_agent_test", "SRV-01")
	if err != nil {
		t.Fatalf("RevokeAgent: %v", err)
	}
	if revoked != 1 {
		t.Errorf("revoked %d rows, want 1", revoked)
	}

	// SRV-01 must now be rejected
	_, err = db.ResolveAPIKey(HashAPIKey(agentKey1))
	if !errors.Is(err, ErrAPIKeyNotFound) {
		t.Errorf("revoked agent key still resolved: %v", err)
	}

	// SRV-02 must still work
	_, err = db.ResolveAPIKey(HashAPIKey(agentKey2))
	if err != nil {
		t.Errorf("active agent key failed to resolve: %v", err)
	}
}

func TestMigrateTenantID(t *testing.T) {
	db := testDB(t)

	now := time.Now()
	// Insert raw metric
	db.InsertMetric(MetricRow{
		Timestamp: now,
		TenantID:  "old_raw_key",
		ServerID:  "srv1",
		Hostname:  "host1",
	})

	aff, err := db.MigrateTenantID("old_raw_key", "t_migrated")
	if err != nil {
		t.Fatalf("MigrateTenantID: %v", err)
	}
	if aff != 1 {
		t.Errorf("affected rows = %d, want 1", aff)
	}

	// Query under new tenant
	rows, err := db.QueryMetrics(now.Add(-time.Minute), "t_migrated")
	if err != nil {
		t.Fatalf("QueryMetrics: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1", len(rows))
	}
	if rows[0].TenantID != "t_migrated" {
		t.Errorf("TenantID = %s, want t_migrated", rows[0].TenantID)
	}
}
