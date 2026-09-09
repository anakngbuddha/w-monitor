package main

import (
	"context"
	"path/filepath"
	"testing"

	"Zeus/storage"
)

func TestCLIAddClientWritesAudit(t *testing.T) {
	db, err := storage.Open(filepath.Join(t.TempDir(), "cli-audit.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	addClient(db, "CLIAuditCo")
	keys, err := db.ListAPIKeys()
	if err != nil || len(keys) != 1 {
		t.Fatalf("keys: %v %#v", err, keys)
	}
	events, err := db.ListAudit(context.Background(), keys[0].TenantID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Action != "client.created" || events[0].ActorKind != "cli" {
		t.Fatalf("audit: %+v", events)
	}
}
