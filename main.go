// wmonitor is a single-binary monitoring Hub, agent and local collector.
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"Zeus/agent"
	"Zeus/server"
	"github.com/kardianos/service"
)

var (
	flagPort = flag.String("port", "8080", "Dashboard HTTP port")
	flagAppPort = flag.String("app-port", "", "Comma-separated application ports")
	flagInstall = flag.Bool("install", false, "Install machine service")
	flagUninstall = flag.Bool("uninstall", false, "Uninstall machine service")
	flagStart = flag.Bool("start", false, "Start machine service")
	flagStop = flag.Bool("stop", false, "Stop machine service")
	flagExportCSV = flag.String("export-csv", "", "CSV output path")
	flagExportTxt = flag.String("export-txt", "", "Text output path")
	flagTenant = flag.String("tenant", "", "Explicit tenant for Hub exports")
	flagRunFor = flag.Duration("run-for", 0, "Finite foreground collection duration")
	flagExportFlt = flag.String("export-filter", "", "Shutdown export window: daily, weekly or monthly")
	flagDB = flag.String("db", "sqlite", "sqlite or postgres")
	flagDSN = flag.String("dsn", "", "Test-only DSN; use protected config in production")
	flagDSNFile = flag.String("dsn-file", "", "Protected DSN file")
	flagAgentHub = flag.String("agent", "", "Verified HTTPS Hub origin")
	flagAPIKey = flag.String("api-key", "", "Test-only token; use protected config in production")
	flagHub = flag.Bool("hub", false, "Enable authenticated Hub")
	flagExternalIface = flag.String("external-iface", "", "External network interface override")
	flagAssessmentReport = flag.String("assessment-report", "", "HTML report output path")
	flagSince = flag.Duration("since", 30*24*time.Hour, "Report window")
	flagShowKey = flag.Bool("show-key", false, "Deprecated: secret recovery output is disabled")
	flagAddClient = flag.String("add-client", "", "Register a new client")
	flagListClients = flag.Bool("list-clients", false, "List registered clients")
	flagRevokeClient = flag.String("revoke-client", "", "Revoke a client")
	flagImportClients = flag.String("import-clients", "", "Explicit legacy CSV import")
	flagNewEnrollCode = flag.String("new-enroll-code", "", "Issue enrollment code for client")
	flagTTL = flag.Duration("ttl", 336*time.Hour, "Enrollment code TTL")
	flagMaxUses = flag.Int("max-uses", 25, "Enrollment code maximum uses")
	flagEnrollCode = flag.String("enroll-code", "", "Test-only enrollment code")
	flagNewAdminToken = flag.Bool("new-admin-token", false, "Explicit one-time admin token issuance")
	flagListAgents = flag.String("list-agents", "", "List agents for a tenant or client")
	flagRevokeAgent = flag.String("revoke-agent", "", "Revoke a server identity")
	flagMigrateOpaqueTenants = flag.Bool("migrate-opaque-tenants", false, "Explicit opaque-tenant migration")
	flagOpaqueBackupDir = flag.String("opaque-tenant-backup-dir", "", "Verified pre-migration backup directory")
	flagUserWindow = flag.Duration("user-window", time.Minute, "Connection observation window")
	flagPrintConfig = flag.Bool("print-config", false, "Print configuration without secrets")
	flagConfig = flag.String("config", "", "Absolute protected config file; default is machine config")
)

var (
	defaultHubURL string
	defaultAPIKey string
	defaultEnrollCode string
	defaultClientLabel string
	buildVersion = "dev"
	buildCommit = "unknown"
	explicitFlags = map[string]bool{}
)

func recordExplicitFlags() { flag.Visit(func(f *flag.Flag) { explicitFlags[f.Name] = true }) }

func main() {
	flag.Parse()
	recordExplicitFlags()
	// Stop/uninstall must remain available even when workload config is broken.
	// Parsing flags no longer writes agent_id or contacts a Hub.
	if controls := serviceControlCount(); controls > 1 { log.Fatal("choose exactly one service-control action") }
	if *flagStop || *flagStart || *flagUninstall { controlService(); return }
	if err := loadConfigEnv(); err != nil { log.Fatal("configuration rejected: ", err) }
	applyEnvConfig()
	alertEnvDefaults()
	if err := validateConfig(); err != nil { log.Fatal("configuration rejected: ", err) }
	server.BuildVersion, agent.BuildVersion = buildVersion, buildVersion
	if *flagShowKey { fmt.Println("Secret recovery output is disabled. Request a new scoped credential from your administrator."); return }
	if *flagPrintConfig { printConfig(); return }
	if *flagInstall { controlService(); return }
	if maybeWriteDefaultAlerts() { return }
	if adminRequested() { runClientAdmin(); return }
	if runOneShotExport() { return }
	prg, err := makeProgram()
	if err != nil { log.Fatal("startup failed: ", err) }
	svc, err := service.New(prg, &service.Config{Name: "wmonitor", DisplayName: "W-Monitor System Monitor", Description: "W-Monitor evidence collection service"})
	if err != nil { prg.Stop(nil); log.Fatal("service setup failed") }
	if !service.Interactive() { if err := svc.Run(); err != nil { prg.Stop(nil); log.Fatal("service failed") }; return }
	if err := runForeground(prg); err != nil { log.Fatal(err) }
}

func serviceControlCount() int {
	count := 0
	for _, value := range []bool{*flagInstall, *flagUninstall, *flagStart, *flagStop} { if value { count++ } }
	return count
}

func controlService() {
	config := &service.Config{Name: "wmonitor", DisplayName: "W-Monitor System Monitor", Description: "W-Monitor evidence collection service"}
	if *flagInstall {
		for name := range explicitFlags { if _, secret := secretServiceFlags["-"+name]; secret { log.Fatal("put service secrets in protected machine config, not installation arguments") } }
		config.Arguments = serviceSafeArgs(os.Args[1:])
	}
	svc, err := service.New(&program{}, config)
	if err != nil { log.Fatal("service setup failed") }
	switch { case *flagInstall: err = svc.Install(); case *flagUninstall: err = svc.Uninstall(); case *flagStart: err = service.Control(svc, "start"); case *flagStop: err = service.Control(svc, "stop") }
	if err != nil { log.Fatal("service-control operation failed: ", err) }
	fmt.Println("Service-control operation completed.")
}
