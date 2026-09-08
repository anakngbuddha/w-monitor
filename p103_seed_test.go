package main

import (
	"os"
	"path/filepath"
	"testing"

	"Zeus/storage"
)

func TestP103AutoSeedDoesNotImportCSVOrUnrevoke(t *testing.T) {
	dir := t.TempDir()
	db, err := storage.Open(filepath.Join(dir, "seed.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	const revokedKey = "seed-revoked-key"
	if err := db.UpsertAPIKey(storage.APIKeyRecord{
		KeyHash: storage.HashAPIKey(revokedKey), TenantID: "t_old", ClientName: "default",
		Kind: storage.KindLegacy, Scope: storage.ScopeRead,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.RevokeAPIKey("default"); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(dir, "clients_registry.csv"), []byte("ClientName,APIKey\nCSVClient,csv-secret-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cwd, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(cwd) })

	t.Setenv("WMONITOR_API_KEY", revokedKey)
	t.Setenv("WMONITOR_ADMIN_TOKEN", "")
	autoSeedHubKeys(db)

	keys, err := db.ListAPIKeys()
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range keys {
		if k.ClientName == "CSVClient" {
			t.Fatal("working-directory CSV was imported by autoSeedHubKeys")
		}
	}
	if _, err := db.ResolveAPIKey(storage.HashAPIKey(revokedKey)); err == nil {
		t.Fatal("revoked configured key was resurrected on seed")
	}
}
