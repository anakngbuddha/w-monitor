package storage

import (
	"context"
	"path/filepath"
	"testing"
)

func TestAuditRequiresTenantAndIsolatesRows(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "audit.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.AppendAudit(context.Background(), AuditEvent{Action: "session.login", ActorKind: "read"}); err == nil {
		t.Fatal("empty tenant accepted")
	}
	if err := db.AppendAudit(context.Background(), AuditEvent{TenantID: "t_a", Action: "session.login", ActorKind: "read", ActorPrefix: "wmr_"}); err != nil {
		t.Fatal(err)
	}
	if err := db.AppendAudit(context.Background(), AuditEvent{TenantID: "t_b", Action: "client.created", ActorKind: "cli", ActorPrefix: "cli"}); err != nil {
		t.Fatal(err)
	}
	a, err := db.ListAudit(context.Background(), "t_a", 10)
	if err != nil || len(a) != 1 || a[0].Action != "session.login" {
		t.Fatalf("tenant a: %v %+v", err, a)
	}
	b, err := db.ListAudit(context.Background(), "t_b", 10)
	if err != nil || len(b) != 1 || b[0].Action != "client.created" {
		t.Fatalf("tenant b: %v %+v", err, b)
	}
}
