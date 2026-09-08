package storage

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestHashAPIKeyIsExactAndDoesNotAliasTokens(t *testing.T) {
	const token = "wma_AbCDefGhIjKlMnOpQrStUvWxYz0123456789abcd"
	if HashAPIKey(token) == HashAPIKey("WMA_ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789ABCD") {
		t.Fatal("opaque tokens must not be case-aliased")
	}
	code, err := GenerateEnrollCode()
	if err != nil {
		t.Fatal(err)
	}
	if HashEnrollCode(code) != HashAPIKey(NormalizeEnrollCode(code)) {
		t.Fatal("HashEnrollCode must hash the normalized code")
	}
	if HashEnrollCode("wm-4f2k-9qx7-tr31") != HashEnrollCode("WM-4F2K-9QX7-TR31") {
		t.Fatal("enrollment codes should normalize before hashing")
	}
}

func TestResolveRejectsEnrollmentCodes(t *testing.T) {
	db := testDB(t)
	code, _ := GenerateEnrollCode()
	hash := HashEnrollCode(code)
	if err := db.UpsertAPIKey(APIKeyRecord{
		KeyHash:    hash,
		TenantID:   "t_enroll",
		ClientName: "EnrollOnly",
		Kind:       KindEnroll,
		Scope:      ScopeEnroll,
		MaxUses:    5,
		ExpiresAt:  time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ResolveAPIKey(hash); !errors.Is(err, ErrAPIKeyNotFound) {
		t.Fatalf("enroll code resolved as ingest credential: %v", err)
	}
	if _, err := db.ConsumeEnrollCode(hash); err != nil {
		t.Fatalf("handshake consume failed: %v", err)
	}
}

func TestUpsertDoesNotUnrevoke(t *testing.T) {
	db := testDB(t)
	const plaintext = "revoked-then-reimported"
	rec := APIKeyRecord{
		KeyHash:    HashAPIKey(plaintext),
		TenantID:   "t_stay_revoked",
		ClientName: "StayRevoked",
		Kind:       KindLegacy,
		Scope:      ScopeRead,
	}
	if err := db.UpsertAPIKey(rec); err != nil {
		t.Fatal(err)
	}
	if _, err := db.RevokeAPIKey("StayRevoked"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ResolveAPIKey(rec.KeyHash); !errors.Is(err, ErrAPIKeyNotFound) {
		t.Fatalf("revoked key still resolves: %v", err)
	}
	looked, err := db.LookupAPIKey(rec.KeyHash)
	if !errors.Is(err, ErrAPIKeyRevoked) {
		t.Fatalf("LookupAPIKey: %v", err)
	}
	if !looked.Revoked {
		t.Fatal("expected revoked flag")
	}
	if err := db.UpsertAPIKey(rec); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ResolveAPIKey(rec.KeyHash); !errors.Is(err, ErrAPIKeyNotFound) {
		t.Fatal("upsert resurrected a revoked credential")
	}
	looked, err = db.LookupAPIKey(rec.KeyHash)
	if !errors.Is(err, ErrAPIKeyRevoked) || !looked.Revoked {
		t.Fatal("revoked credential must stay revoked after upsert")
	}
}

func TestUniqueTenantForClientNameRejectsDuplicates(t *testing.T) {
	keys := []APIKeyRecord{
		{ClientName: "Acme", TenantID: "t_one"},
		{ClientName: "acme", TenantID: "t_two"},
	}
	if _, err := UniqueTenantForClientName(keys, "Acme"); !errors.Is(err, ErrAmbiguousClientName) {
		t.Fatalf("got %v, want ErrAmbiguousClientName", err)
	}
	one := []APIKeyRecord{{ClientName: "Solo", TenantID: "t_solo"}}
	id, err := UniqueTenantForClientName(one, "Solo")
	if err != nil || id != "t_solo" {
		t.Fatalf("unique name: id=%q err=%v", id, err)
	}
}

func TestMigrateOpaqueTenantsAtomicAndKeepsRevoked(t *testing.T) {
	db := testDB(t)
	raw := "plaintext-tenant-secret"
	if err := db.UpsertAPIKey(APIKeyRecord{
		KeyHash:    HashAPIKey("k1"),
		TenantID:   raw,
		ClientName: "LegacyClient",
		Kind:       KindLegacy,
		Scope:      ScopeRead,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.RevokeAPIKey("LegacyClient"); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if err := db.InsertMetric(MetricRow{Timestamp: now, TenantID: raw, ServerID: "srv-a"}); err != nil {
		t.Fatal(err)
	}

	backup := t.TempDir()
	migrateOpaqueFailPoint = "before-commit"
	t.Cleanup(func() { migrateOpaqueFailPoint = "" })
	if _, err := db.MigrateOpaqueTenants(backup); err == nil {
		t.Fatal("expected injected failure")
	}
	rows, err := db.QueryMetrics(now.Add(-time.Minute), raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("rollback lost metrics: %d", len(rows))
	}
	keys, _ := db.ListAPIKeys()
	if len(keys) != 1 || keys[0].TenantID != raw || !keys[0].Revoked {
		t.Fatalf("failed migration mutated credentials: %+v", keys)
	}

	migrateOpaqueFailPoint = ""
	backup2 := t.TempDir()
	report, err := db.MigrateOpaqueTenants(backup2)
	if err != nil {
		t.Fatalf("migration: %v", err)
	}
	if report.BackupPath == "" {
		t.Fatal("missing backup path")
	}
	if _, err := os.Stat(report.BackupPath); err != nil {
		t.Fatalf("backup missing: %v", err)
	}
	keys, _ = db.ListAPIKeys()
	if len(keys) != 1 {
		t.Fatalf("keys=%d", len(keys))
	}
	if !IsOpaqueTenantID(keys[0].TenantID) {
		t.Fatalf("tenant still plaintext: %q", keys[0].TenantID)
	}
	if !keys[0].Revoked {
		t.Fatal("migration un-revoked a credential")
	}
	stillOld, _ := db.QueryMetrics(now.Add(-time.Minute), raw)
	if len(stillOld) != 0 {
		t.Fatal("old tenant id still has rows")
	}
	moved, _ := db.QueryMetrics(now.Add(-time.Minute), keys[0].TenantID)
	if len(moved) != 1 {
		t.Fatalf("metrics not remapped: %d", len(moved))
	}
}

func TestMigrateOpaqueTenantsRequiresBackupDir(t *testing.T) {
	db := testDB(t)
	if _, err := db.MigrateOpaqueTenants(""); err == nil {
		t.Fatal("empty backup dir must fail")
	}
	if _, err := os.Stat(filepath.Join(db.Path)); err != nil {
		t.Fatal(err)
	}
}
