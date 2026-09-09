package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"Zeus/storage"
)

type clientAdminStore interface {
	UpsertAPIKey(storage.APIKeyRecord) error
	ListAPIKeys() ([]storage.APIKeyRecord, error)
	RevokeAPIKey(string) (int64, error)
	ConsumeEnrollCode(string) (storage.APIKeyRecord, error)
	RevokeAgent(string, string) (int64, error)
	ListAgents(string) ([]storage.APIKeyRecord, error)
	LookupAPIKey(string) (storage.APIKeyRecord, error)
	AppendAudit(context.Context, storage.AuditEvent) error
}

func recordCLIAudit(keys clientAdminStore, ev storage.AuditEvent) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := keys.AppendAudit(ctx, ev); err != nil {
		log.Fatal("audit unavailable")
	}
}

func adminRequested() bool {
	return *flagAddClient != "" || *flagListClients || *flagRevokeClient != "" || *flagImportClients != "" || *flagNewEnrollCode != "" || *flagNewAdminToken || *flagListAgents != "" || *flagRevokeAgent != "" || *flagMigrateOpaqueTenants
}

func runClientAdmin() {
	store, _, err := openStore()
	if err != nil {
		log.Fatal("administrative storage unavailable")
	}
	defer store.Close()
	keys, ok := store.(clientAdminStore)
	if !ok {
		log.Fatal("backend does not support credential administration")
	}
	switch {
	case *flagNewEnrollCode != "":
		newEnrollCode(keys, *flagNewEnrollCode, *flagTTL, *flagMaxUses)
	case *flagNewAdminToken:
		newAdminToken(keys)
	case *flagListAgents != "":
		listAgents(keys, *flagListAgents)
	case *flagRevokeAgent != "":
		revokeAgent(keys, *flagRevokeAgent)
	case *flagMigrateOpaqueTenants:
		runOpaqueTenantMigration(store)
	case *flagAddClient != "":
		addClient(keys, *flagAddClient)
	case *flagListClients:
		listClients(keys)
	case *flagRevokeClient != "":
		revokeClient(keys, *flagRevokeClient)
	case *flagImportClients != "":
		importClients(keys, *flagImportClients)
	}
}

func newEnrollCode(keys clientAdminStore, name string, ttl time.Duration, uses int) {
	all, err := keys.ListAPIKeys()
	if err != nil {
		log.Fatal("credential registry unavailable")
	}
	tenant, err := storage.UniqueTenantForClientName(all, name)
	if err != nil {
		log.Fatal("ambiguous client name; use tenant-scoped administration")
	}
	if tenant == "" {
		log.Fatal("create the client explicitly before issuing enrollment codes")
	}
	code, err := storage.GenerateEnrollCode()
	if err != nil {
		log.Fatal("code generation failed")
	}
	err = keys.UpsertAPIKey(storage.APIKeyRecord{TenantID: tenant, ClientName: name, KeyHash: storage.HashEnrollCode(code), KeyPrefix: "wme_", Kind: storage.KindEnroll, Scope: storage.ScopeEnroll, ExpiresAt: time.Now().Add(ttl), MaxUses: uses, IssuedBy: "cli", CreatedAt: time.Now()})
	if err != nil {
		log.Fatal("code issuance failed")
	}
	recordCLIAudit(keys, storage.AuditEvent{TenantID: tenant, ActorKind: "cli", ActorPrefix: "cli", Action: "enroll.code_issued", TargetType: "client", TargetID: name})
	fmt.Printf("Enrollment code (shown once): %s\nTenant: %s\nStore it in protected machine config, not a build or service argument.\n", code, tenant)
}

func newAdminToken(keys clientAdminStore) {
	token, err := storage.GenerateToken(storage.KindAdmin)
	if err != nil {
		log.Fatal("token generation failed")
	}
	if err := keys.UpsertAPIKey(storage.APIKeyRecord{TenantID: "admin", ClientName: "Admin", KeyHash: storage.HashAPIKey(token), KeyPrefix: storage.ExtractKeyPrefix(token), Kind: storage.KindAdmin, Scope: storage.ScopeAdmin, IssuedBy: "cli", CreatedAt: time.Now()}); err != nil {
		log.Fatal("admin token issuance failed")
	}
	recordCLIAudit(keys, storage.AuditEvent{TenantID: "admin", ActorKind: "cli", ActorPrefix: "cli", Action: "admin.token_issued", TargetType: "admin", TargetID: "Admin"})
	fmt.Printf("Admin token (shown once): %s\n", token)
}

func addClient(keys clientAdminStore, name string) {
	all, err := keys.ListAPIKeys()
	if err != nil {
		log.Fatal("credential registry unavailable")
	}
	for _, record := range all {
		if strings.EqualFold(record.ClientName, name) {
			log.Fatal("client name already exists; no duplicate identity was created")
		}
	}
	tenant, err := storage.NewTenantID()
	if err != nil {
		log.Fatal("tenant generation failed")
	}
	token, err := storage.GenerateToken(storage.KindRead)
	if err != nil {
		log.Fatal("token generation failed")
	}
	if err := keys.UpsertAPIKey(storage.APIKeyRecord{TenantID: tenant, ClientName: name, KeyHash: storage.HashAPIKey(token), KeyPrefix: storage.ExtractKeyPrefix(token), Kind: storage.KindRead, Scope: storage.ScopeRead, IssuedBy: "cli", CreatedAt: time.Now()}); err != nil {
		log.Fatal("client creation failed")
	}
	recordCLIAudit(keys, storage.AuditEvent{TenantID: tenant, ActorKind: "cli", ActorPrefix: "cli", Action: "client.created", TargetType: "client", TargetID: name})
	fmt.Printf("Client: %s\nTenant: %s\nDashboard read token (shown once): %s\n", name, tenant, token)
}

func listClients(keys clientAdminStore) {
	records, err := keys.ListAPIKeys()
	if err != nil {
		log.Fatal("credential registry unavailable")
	}
	for _, record := range records {
		fmt.Printf("%s\t%s\tkind=%s\trevoked=%t\n", record.ClientName, record.TenantID, record.Kind, record.Revoked)
	}
}

func listAgents(keys clientAdminStore, filter string) {
	tenant := filter
	if filter == "all" || filter == "*" {
		tenant = ""
	} else {
		all, err := keys.ListAPIKeys()
		if err != nil {
			log.Fatal("credential registry unavailable")
		}
		matched, err := storage.UniqueTenantForClientName(all, filter)
		if err != nil {
			log.Fatal("ambiguous client name")
		}
		if matched != "" {
			tenant = matched
		}
	}
	agents, err := keys.ListAgents(tenant)
	if err != nil {
		log.Fatal("agent registry unavailable")
	}
	for _, item := range agents {
		fmt.Printf("%s\t%s\trevoked=%t\n", item.TenantID, item.ServerID, item.Revoked)
	}
}

func revokeClient(keys clientAdminStore, name string) {
	all, err := keys.ListAPIKeys()
	if err != nil {
		log.Fatal("credential registry unavailable")
	}
	tenant, err := storage.UniqueTenantForClientName(all, name)
	if err != nil {
		log.Fatal("ambiguous client name; nothing revoked")
	}
	n, err := keys.RevokeAPIKey(name)
	if err != nil {
		log.Fatal("revocation failed")
	}
	if tenant != "" {
		recordCLIAudit(keys, storage.AuditEvent{TenantID: tenant, ActorKind: "cli", ActorPrefix: "cli", Action: "client.revoked", TargetType: "client", TargetID: name})
	}
	fmt.Printf("Revoked %d credentials.\n", n)
}

func revokeAgent(keys clientAdminStore, serverID string) {
	all, err := keys.ListAgents("")
	if err != nil {
		log.Fatal("agent registry unavailable")
	}
	tenants := map[string]bool{}
	for _, record := range all {
		if record.ServerID == serverID && !record.Revoked {
			tenants[record.TenantID] = true
		}
	}
	if len(tenants) > 1 {
		log.Fatal("agent ID occurs in multiple tenants; nothing revoked")
	}
	for tenant := range tenants {
		n, err := keys.RevokeAgent(tenant, serverID)
		if err != nil {
			log.Fatal("agent revocation failed")
		}
		recordCLIAudit(keys, storage.AuditEvent{TenantID: tenant, ActorKind: "cli", ActorPrefix: "cli", Action: "agent.revoked", TargetType: "agent", TargetID: serverID})
		fmt.Printf("Revoked %d credentials.\n", n)
	}
}

// Legacy CSV imports were non-transactional and matched display names. Keep the
// command explicit but fail closed rather than introduce partial tenant writes.
// Use the reviewed opaque-tenant migration for existing data and explicit client
// creation/credential re-issuance for new installations.
func importClientsFromCSV(clientAdminStore, string) (int, error) {
	return 0, errors.New("legacy CSV credential import disabled; use reviewed migration and explicit re-issuance")
}
func importClients(keys clientAdminStore, path string) {
	if _, err := importClientsFromCSV(keys, path); err != nil {
		log.Fatal(err)
	}
}

// Retained as a no-op for compatibility tests. Startup never registers,
// resurrects or rotates credentials from config, working-directory files or CSV.
func autoSeedHubKeys(storage.Store) {}

func runOpaqueTenantMigration(store storage.Store) {
	db, ok := store.(*storage.DB)
	if !ok || *flagOpaqueBackupDir == "" {
		log.Fatal("opaque migration requires SQLite and an explicit verified backup directory")
	}
	report, err := db.MigrateOpaqueTenants(*flagOpaqueBackupDir)
	if err != nil {
		log.Fatal("opaque migration failed; review the backup and transaction state before retrying")
	}
	recordCLIAudit(db, storage.AuditEvent{TenantID: "admin", ActorKind: "cli", ActorPrefix: "cli", Action: "tenants.migrated", TargetType: "migration", TargetID: "opaque"})
	fmt.Printf("Opaque migration completed. Backup: %s\nCredentials: %d; metrics: %d; processes: %d\nDo not downgrade to legacy credentials.\n", report.BackupPath, report.CredentialsMoved, report.MetricsMoved, report.ProcessesMoved)
}
