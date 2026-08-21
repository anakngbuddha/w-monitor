package storage

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
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
}

func TestTouchAPIKeyUpdatesLastSeen(t *testing.T) {
	db := testDB(t)
	hash := HashAPIKey("touch-me")
	db.UpsertAPIKey(APIKeyRecord{KeyHash: hash, TenantID: "t_touch", ClientName: "Toucher"})

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
