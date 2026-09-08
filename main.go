// wmonitor — single-binary system monitoring tool
//
// Usage:
//
//	wmonitor                                    # run in foreground (SQLite, local)
//	wmonitor -install                           # install as system service
//	wmonitor -uninstall                         # remove system service
//	wmonitor -port 9090                         # specify HTTP port (default: 8080)
//	wmonitor -export-csv <path>                 # export 30-day CSV report and exit
//	wmonitor -export-txt <path>                 # export 30-day text report and exit
//	wmonitor -assessment-report <path.html>     # generate HTML assessment report and exit
//	wmonitor -db postgres                       # use Postgres backend (reads DSN from env/file/flag)
//	wmonitor -agent <hub-url>                   # run as agent, push to hub (no local DB)
//	wmonitor -hub                               # enable hub ingest endpoint (POST /api/ingest)
//	wmonitor -external-iface <name>             # override external NIC auto-detect (Phase 10)
//
// Client credential management (hub only):
//
//	wmonitor -add-client <name>                 # generate an API key, print it once
//	wmonitor -list-clients                      # list registered clients
//	wmonitor -revoke-client <name>              # revoke a client's keys
//	wmonitor -import-clients <csv>              # migrate clients_registry.csv into the DB
//
// Alerting:
//
//	wmonitor -write-default-alerts alerts.json  # write a starter rules file
//	wmonitor -alerts alerts.json                # evaluate custom rules
//	wmonitor -alert-slack <webhook-url>         # notify Slack when a rule fires
package main

import (
	"bufio"
	"context"
	"encoding/csv"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"Zeus/agent"
	"Zeus/alerting"
	"Zeus/collector"
	"Zeus/dashboard"
	"Zeus/export"
	"Zeus/retention"
	"Zeus/server"
	"Zeus/storage"

	"github.com/kardianos/service"
)

// ── CLI flags ──
var (
	// Existing flags
	flagPort      = flag.String("port", "8080", "HTTP port for the dashboard")
	flagAppPort   = flag.String("app-port", "", "Comma-separated application listening ports to monitor for concurrent users (e.g. 80,443,3000)")
	flagInstall   = flag.Bool("install", false, "Install wmonitor as a system service")
	flagUninstall = flag.Bool("uninstall", false, "Uninstall the wmonitor service")
	flagStart     = flag.Bool("start", false, "Start the installed service")
	flagStop      = flag.Bool("stop", false, "Stop the running service")
	flagExportCSV = flag.String("export-csv", "", "Export 30-day CSV report to this path and exit")
	flagExportTxt = flag.String("export-txt", "", "Export 30-day text report to this path and exit")
	flagTenant    = flag.String("tenant", "", "Tenant ID for CSV/HTML/text export (required for hub data; default t_local)")
	flagRunFor    = flag.Duration("run-for", 0, "Run for this duration then export and exit (e.g. 1h, 30m)")
	flagExportFlt = flag.String("export-filter", "", "Filter for automatic export (daily, weekly, monthly)")

	// Phase 3 — backend selection & credential handling
	flagDB      = flag.String("db", "sqlite", "Database backend: sqlite or postgres")
	flagDSN     = flag.String("dsn", "", "Database DSN (quick-test only — prefer WMONITOR_DB_DSN env var)")
	flagDSNFile = flag.String("dsn-file", "", "Path to a file containing the DSN (one line)")

	// Phase 5 — agent mode
	flagAgentHub = flag.String("agent", "", "Run as agent: push metrics to this hub URL (e.g. https://hub:8080)")
	flagAPIKey   = flag.String("api-key", "", "API key for hub authentication (or use WMONITOR_API_KEY env var)")

	// Phase 6 — hub mode
	flagHub = flag.Bool("hub", false, "Enable hub ingest endpoint (POST /api/ingest)")

	// Phase 10 — external interface override
	flagExternalIface = flag.String("external-iface", "", "NIC name to treat as external (overrides auto-detect)")

	// Phase 12 — assessment report
	flagAssessmentReport = flag.String("assessment-report", "", "Generate HTML assessment report to this path and exit")
	flagSince            = flag.Duration("since", 30*24*time.Hour, "Time window for assessment/export reports (default 30d)")

	// Key recovery
	flagShowKey = flag.Bool("show-key", false, "Print the baked-in API key and exit")

	// Phase 13 — client credential management (hub side)
	flagAddClient     = flag.String("add-client", "", "Generate and register a dashboard read token for this client name, then exit")
	flagListClients   = flag.Bool("list-clients", false, "List registered API clients and exit")
	flagRevokeClient  = flag.String("revoke-client", "", "Revoke all API keys for this client name and exit")
	flagImportClients = flag.String("import-clients", "", "Import a clients_registry.csv into the API key table and exit")
	flagNewEnrollCode = flag.String("new-enroll-code", "", "Generate an enrollment code for a client name and exit (hub only)")
	flagTTL           = flag.Duration("ttl", 336*time.Hour, "Time-to-live for new enrollment code (default 14d / 336h)")
	flagMaxUses       = flag.Int("max-uses", 25, "Maximum number of uses for enrollment code (default 25)")
	flagEnrollCode    = flag.String("enroll-code", "", "Enrollment code for agent self-provisioning (or use WMONITOR_ENROLL_CODE)")
	flagNewAdminToken = flag.Bool("new-admin-token", false, "Generate and register a new admin token (wmk_...) and exit")
	flagListAgents    = flag.String("list-agents", "", "List enrolled agents for a client or tenant and exit")
	flagRevokeAgent   = flag.String("revoke-agent", "", "Revoke agent token for a server ID and exit")
	flagMigrateOpaqueTenants = flag.Bool("migrate-opaque-tenants", false, "One-time remap of plaintext tenant IDs to t_<hex> (SQLite; requires backup dir)")
	flagOpaqueBackupDir      = flag.String("opaque-tenant-backup-dir", "", "Directory for the pre-migration SQLite copy (required with -migrate-opaque-tenants)")

	// Phase 13 — observability & tuning
	flagUserWindow  = flag.Duration("user-window", 60*time.Second, "Sliding window for counting concurrent users")
	flagPrintConfig = flag.Bool("print-config", false, "Print the resolved configuration (secrets masked) and exit")
)

// Build-time variables injected via -ldflags (e.g. for pre-configured client binaries)
var (
	defaultHubURL      string
	defaultAPIKey      string
	defaultEnrollCode  string
	defaultClientLabel string
	buildVersion       = "dev"
	buildCommit        = "unknown"
)

// explicitFlags records which flags the operator actually typed.
//
// The previous config logic inferred this by comparing each flag against its
// default value, so `-port 8080` (the default) was treated as "not set" and got
// silently overridden by WMONITOR_PORT. flag.Visit reports only flags that were
// present on the command line, which is the real signal.
var explicitFlags = map[string]bool{}

func recordExplicitFlags() {
	flag.Visit(func(f *flag.Flag) { explicitFlags[f.Name] = true })
}

// envOr applies an environment variable only when the flag was not given.
func envOr(flagName string, target *string, envNames ...string) {
	if explicitFlags[flagName] {
		return
	}
	for _, name := range envNames {
		if v := os.Getenv(name); v != "" {
			*target = v
			return
		}
	}
}

// ── Service program ──

type program struct {
	store     storage.Store
	srv       *server.Server
	collector *collector.Collector
	retention *retention.Job
	evaluator *alerting.Evaluator
	cancel    context.CancelFunc
	// Keep *storage.DB around for Conn() (retention uses raw *sql.DB)
	sqliteDB    *storage.DB
	collectorWg sync.WaitGroup
}

func (p *program) Start(s service.Service) error {
	log.Println("[wmonitor] service starting")
	go p.run()
	return nil
}

func (p *program) run() {
	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel

	// Collector goroutine
	if p.collector != nil {
		p.collectorWg.Add(1)
		go func() {
			defer p.collectorWg.Done()
			p.collector.Run(ctx)
		}()
	}

	// Alert evaluation
	runEvaluator(ctx, p.evaluator)

	// Retention job (hourly) — only when we have a local SQLite DB
	if p.retention != nil {
		go func() {
			if err := p.retention.Run(); err != nil {
				log.Printf("[wmonitor] initial retention run: %v", err)
			}
			p.retention.RunScheduled()
		}()
	}

	// HTTP server (blocking) — only when not in pure agent mode
	if p.srv != nil {
		if err := p.srv.Start(); err != nil {
			log.Printf("[wmonitor] server error: %v", err)
		}
	} else {
		// Pure agent mode: block on context
		<-ctx.Done()
	}
}

func (p *program) Stop(s service.Service) error {
	log.Println("[wmonitor] service stopping")

	// Stop accepting new requests and let in-flight ones finish BEFORE the store
	// is closed. Previously the store was closed while handlers were still
	// running, so a CSV export in progress died mid-stream.
	if p.srv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), server.ShutdownGrace())
		defer cancel()
		if err := p.srv.Shutdown(ctx); err != nil {
			log.Printf("[wmonitor] http shutdown: %v", err)
		}
	}
	if p.cancel != nil {
		p.cancel()
	}
	// Wait for collector loop to finish before closing storage backend
	p.collectorWg.Wait()
	if p.store != nil {
		p.store.Close()
	}
	return nil
}

// ── Main ──

func main() {
	flag.Parse()
	recordExplicitFlags()
	loadConfigEnv()

	// Propagate the build stamp so /api/health and X-Agent-Version are useful.
	server.BuildVersion = buildVersion
	agent.BuildVersion = buildVersion

	if *flagShowKey {
		key := resolveAPIKey()
		if key == "" {
			fmt.Println("No API key configured or baked into this binary.")
		} else {
			fmt.Printf("Your W-Monitor API Key: %s\n", key)
		}
		return
	}

	applyEnvConfig()
	alertEnvDefaults()

	if *flagPrintConfig {
		printConfig()
		return
	}
	if maybeWriteDefaultAlerts() {
		return
	}

	// ── Client credential management (needs a store, no collector) ──
	if *flagAddClient != "" || *flagListClients || *flagRevokeClient != "" || *flagImportClients != "" ||
		*flagNewEnrollCode != "" || *flagNewAdminToken || *flagListAgents != "" || *flagRevokeAgent != "" ||
		*flagMigrateOpaqueTenants {
		runClientAdmin()
		return
	}

	// ── Service control commands (-install, -uninstall, -start, -stop) ──
	if *flagInstall {
		var svcArgs []string
		if *flagAgentHub != "" {
			ensureAgentCredentials()
			svcArgs = append(svcArgs, "-agent", *flagAgentHub)
			if *flagExternalIface != "" {
				svcArgs = append(svcArgs, "-external-iface", *flagExternalIface)
			}
			if *flagAppPort != "" {
				svcArgs = append(svcArgs, "-app-port", *flagAppPort)
			}
		} else {
			for _, arg := range os.Args[1:] {
				if arg != "-install" && arg != "--install" {
					svcArgs = append(svcArgs, arg)
				}
			}
		}

		svcConfig := &service.Config{
			Name:        "wmonitor",
			DisplayName: "W-Monitor System Monitor",
			Description: "W-Monitor Monitoring Service",
			Arguments:   serviceSafeArgs(svcArgs),
		}
		svc, err := service.New(&program{}, svcConfig)
		if err != nil {
			log.Fatalf("service.New: %v", err)
		}
		if err := svc.Install(); err != nil {
			log.Fatalf("install: %v", err)
		}
		fmt.Println("Service installed. Start it with: wmonitor -start")
		return
	}

	if *flagUninstall {
		svc, err := service.New(&program{}, &service.Config{Name: "wmonitor"})
		if err != nil {
			log.Fatalf("service.New: %v", err)
		}
		if err := svc.Uninstall(); err != nil {
			log.Fatalf("uninstall: %v", err)
		}
		fmt.Println("Service uninstalled.")
		return
	}

	if *flagStart {
		svc, err := service.New(&program{}, &service.Config{Name: "wmonitor"})
		if err != nil {
			log.Fatalf("service.New: %v", err)
		}
		if err := service.Control(svc, "start"); err != nil {
			log.Fatalf("start: %v", err)
		}
		fmt.Println("Service started.")
		return
	}

	if *flagStop {
		svc, err := service.New(&program{}, &service.Config{Name: "wmonitor"})
		if err != nil {
			log.Fatalf("service.New: %v", err)
		}
		if err := service.Control(svc, "stop"); err != nil {
			log.Fatalf("stop: %v", err)
		}
		fmt.Println("Service stopped.")
		return
	}

	// ── Agent mode (Phase 5): no local DB, no Aiven credentials ──
	if *flagAgentHub != "" {
		runAgentMode()
		return
	}

	// ── Open Store ──
	store, sqliteDB, err := openStore()
	if err != nil {
		log.Fatalf("open store: %v", err)
	}

	// ── One-shot modes (export / report) ──
	if *flagExportCSV != "" {
		n, err := export.CSVReport(store, time.Now().Add(-*flagSince), *flagExportCSV, exportTenant())
		if err != nil {
			log.Fatalf("CSV export: %v", err)
		}
		fmt.Printf("Exported %d rows to %s\n", n, *flagExportCSV)
		store.Close()
		return
	}
	if *flagExportTxt != "" {
		s, err := export.TextReport(store, time.Now().Add(-*flagSince), *flagExportTxt, exportTenant())
		if err != nil {
			log.Fatalf("text export: %v", err)
		}
		fmt.Printf("Report written to %s (%d samples, avg CPU %.1f%%)\n", *flagExportTxt, s.RowCount, s.AvgCPU)
		store.Close()
		return
	}
	if *flagAssessmentReport != "" {
		end := time.Now()
		start := end.Add(-*flagSince)
		if err := export.GenerateAssessmentReport(store, start, end, *flagAssessmentReport, exportTenant()); err != nil {
			log.Fatalf("assessment report: %v", err)
		}
		fmt.Printf("Assessment report written to %s\n", *flagAssessmentReport)
		store.Close()
		return
	}

	// ── Build service components ──
	col := collector.New(store)
	if *flagExternalIface != "" {
		col.SetExternalIface(*flagExternalIface)
	}
	configureUserTracker(col)

	// Retention: destructive downsampling/purge are contained (V07) until P2.03.
	var ret *retention.Job
	if sqliteDB != nil {
		ret = retention.New(sqliteDB.Conn())
		ret.SetDataPath(sqliteDB.Path)
	} else if pruner, ok := store.(retention.Pruner); ok {
		ret = retention.NewWithPruner(pruner)
	}
	if ret != nil {
		if v := strings.TrimSpace(os.Getenv("WMONITOR_DISK_BUDGET_BYTES")); v != "" {
			if n, err := strconv.ParseInt(v, 10, 64); err == nil {
				ret.SetDiskBudget(n)
			} else {
				log.Printf("[wmonitor] ignoring invalid WMONITOR_DISK_BUDGET_BYTES=%q: %v", v, err)
			}
		}
		log.Printf("[wmonitor] retention containment: %s", retention.DisabledReason)
	}

	srv := server.New(store, *flagPort)
	if ret != nil {
		job := ret
		srv.SetRetentionStatusProvider(func() map[string]interface{} {
			st := job.Status()
			return map[string]interface{}{
				"downsampling_enabled": st.DownsamplingEnabled,
				"purge_enabled":        st.PurgeEnabled,
				"reason":               st.Reason,
				"disk_pressure":        st.DiskPressure,
				"disk_used_bytes":      st.DiskUsedBytes,
				"disk_budget_bytes":    st.DiskBudgetBytes,
			}
		})
	}

	if *flagHub {
		keys, ok := store.(server.KeyStore)
		if !ok {
			// Fail loudly at startup rather than serving an unauthenticated hub.
			log.Fatalf("hub mode requires a backend that can verify API keys; %T cannot", store)
		}
		autoSeedHubKeys(store)
		srv.EnableHubMode(keys)
	}

	evaluator := buildEvaluator(store, srv)

	if err := dashboard.Register(srv); err != nil {
		log.Fatalf("dashboard register: %v", err)
	}

	prg := &program{
		store:     store,
		srv:       srv,
		collector: col,
		retention: ret,
		evaluator: evaluator,
		sqliteDB:  sqliteDB,
	}

	// Collect service arguments (flags passed with -install)
	var svcArgs []string
	for _, arg := range os.Args[1:] {
		if arg != "-install" && arg != "--install" {
			svcArgs = append(svcArgs, arg)
		}
	}

	// ── Service config ──
	svcConfig := &service.Config{
		Name:        "wmonitor",
		DisplayName: "W-Monitor System Monitor",
		Description: "Collects system metrics and serves the monitoring dashboard at localhost:" + *flagPort,
		Arguments:   serviceSafeArgs(svcArgs),
	}

	svc, err := service.New(prg, svcConfig)
	if err != nil {
		log.Fatalf("service.New: %v", err)
	}

	// ── Interactive/foreground mode ──
	if !service.Interactive() {
		if err := svc.Run(); err != nil {
			log.Fatalf("service run: %v", err)
		}
		return
	}

	log.Printf("[wmonitor] starting in foreground mode (version %s), dashboard at http://localhost:%s", buildVersion, *flagPort)

	ctx, cancel := context.WithCancel(context.Background())

	go col.Run(ctx)
	runEvaluator(ctx, evaluator)

	if ret != nil {
		go func() {
			if err := ret.Run(); err != nil {
				log.Printf("[wmonitor] retention: %v", err)
			}
			ret.RunScheduled()
		}()
	}

	// Timer for custom run duration
	var runTimer <-chan time.Time
	if *flagRunFor > 0 {
		runTimer = time.After(*flagRunFor)
		log.Printf("[wmonitor] will run for %s before shutting down", *flagRunFor)
	}

	// Signal handler for graceful shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	done := make(chan struct{})
	go func() {
		defer close(done)
		select {
		case sig := <-sigCh:
			log.Printf("[wmonitor] received %v, shutting down", sig)
		case <-runTimer:
			log.Printf("[wmonitor] run-for duration (%s) elapsed, shutting down", *flagRunFor)
		}
		cancel()

		// Drain HTTP before touching the store.
		shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), server.ShutdownGrace())
		defer cancelShutdown()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			log.Printf("[wmonitor] http shutdown: %v", err)
		}

		if *flagExportFlt != "" || *flagRunFor > 0 {
			writeShutdownExport(store)
		}

		store.Close()
	}()

	// HTTP server (blocking until Shutdown).
	//
	// The old code called os.Exit(0) from inside the signal handler, which killed
	// the process before any of the shutdown path could complete.
	if err := srv.Start(); err != nil {
		log.Printf("[wmonitor] server: %v", err)
	}
	<-done
}

// writeShutdownExport performs the automatic export on shutdown.
func writeShutdownExport(store storage.Store) {
	var since time.Time
	switch *flagExportFlt {
	case "daily":
		since = time.Now().Add(-24 * time.Hour)
	case "weekly":
		since = time.Now().Add(-7 * 24 * time.Hour)
	case "monthly":
		since = time.Now().Add(-30 * 24 * time.Hour)
	default:
		if *flagRunFor > 0 {
			since = time.Now().Add(-*flagRunFor)
		} else {
			since = time.Now().Add(-24 * time.Hour)
		}
	}

	if runtime.GOOS == "windows" {
		outPath := fmt.Sprintf("wmonitor_export_%s.csv", time.Now().Format("20060102_150405"))
		n, err := export.CSVReport(store, since, outPath, exportTenant())
		if err != nil {
			log.Printf("Auto-export CSV error: %v", err)
			return
		}
		log.Printf("Auto-exported %d rows to %s", n, outPath)
		return
	}

	outPath := fmt.Sprintf("wmonitor_export_%s.txt", time.Now().Format("20060102_150405"))
	s, err := export.TextReport(store, since, outPath, exportTenant())
	if err != nil {
		log.Printf("Auto-export TXT error: %v", err)
		return
	}
	log.Printf("Auto-exported report to %s (avg CPU %.1f%%)", outPath, s.AvgCPU)
}

// configureUserTracker applies app-port, exclusion, and window settings.
func configureUserTracker(col *collector.Collector) {
	tracker, ok := col.UserTracker().(*collector.TCPUserTracker)
	if !ok {
		return
	}
	if *flagAppPort != "" {
		tracker.SetAppPorts(parseAppPorts(*flagAppPort)...)
	}
	// Never count viewers of our own dashboard as application users.
	if srvPort, err := strconv.Atoi(*flagPort); err == nil && srvPort > 0 {
		tracker.SetExcludePorts(uint32(srvPort))
	}
	if *flagUserWindow > 0 {
		tracker.SetWindow(*flagUserWindow)
	}
}

// applyEnvConfig fills in unset flags from environment variables and build-time
// defaults, in that precedence order.
func applyEnvConfig() {
	if !explicitFlags["agent"] {
		if hub := os.Getenv("WMONITOR_AGENT_HUB"); hub != "" {
			*flagAgentHub = hub
		} else if defaultHubURL != "" {
			*flagAgentHub = defaultHubURL
		}
	}
	envOr("enroll-code", flagEnrollCode, "WMONITOR_ENROLL_CODE")
	envOr("db", flagDB, "WMONITOR_DB")
	envOr("port", flagPort, "WMONITOR_PORT", "PORT")
	envOr("external-iface", flagExternalIface, "WMONITOR_EXTERNAL_IFACE")
	envOr("app-port", flagAppPort, "WMONITOR_APP_PORT", "WMONITOR_APP_PORTS")

	if !explicitFlags["hub"] {
		if os.Getenv("WMONITOR_MODE") == "hub" || os.Getenv("WMONITOR_HUB") == "true" {
			*flagHub = true
		}
	}
}

// printConfig dumps the resolved configuration with secrets masked.
func printConfig() {
	fmt.Printf("version:         %s (%s)\n", buildVersion, buildCommit)
	fmt.Printf("mode:            %s\n", resolveMode())
	fmt.Printf("port:            %s\n", *flagPort)
	fmt.Printf("db backend:      %s\n", *flagDB)
	fmt.Printf("agent hub:       %s\n", orNone(*flagAgentHub))
	fmt.Printf("api key:         %s\n", mask(resolveAPIKey()))
	fmt.Printf("enroll code:     %s\n", mask(resolveEnrollCode()))
	fmt.Printf("dsn:             %s\n", maskDSN())
	fmt.Printf("app ports:       %s\n", orNone(*flagAppPort))
	fmt.Printf("external iface:  %s\n", orNone(*flagExternalIface))
	fmt.Printf("user window:     %s\n", *flagUserWindow)
	fmt.Printf("alert rules:     %s\n", orNone(*flagAlerts))
	fmt.Printf("alert webhook:   %s\n", orNone(*flagAlertWebhook))
	fmt.Printf("alert slack:     %s\n", mask(*flagAlertSlack))
	fmt.Printf("allowed origins: %s\n", orNone(os.Getenv("WMONITOR_ALLOWED_ORIGINS")))
}

func resolveMode() string {
	switch {
	case *flagAgentHub != "":
		return "agent"
	case *flagHub:
		return "hub"
	default:
		return "standalone"
	}
}

func orNone(s string) string {
	if s == "" {
		return "(not set)"
	}
	return s
}

// mask shows only enough of a secret to confirm which one is loaded.
func mask(s string) string {
	if s == "" {
		return "(not set)"
	}
	return "(set)"
}

func maskDSN() string {
	dsn, err := resolveDSN()
	if err != nil || dsn == "" {
		return "(not set)"
	}
	u, err := url.Parse(dsn)
	if err != nil {
		return "(unparseable)"
	}
	return fmt.Sprintf("postgres://***@%s%s", u.Hostname(), u.Path)
}

// ── Client credential administration ──

// runClientAdmin handles -add-client / -list-clients / -revoke-client /
// -import-clients. All of these need a credential-capable store.
func runClientAdmin() {
	store, _, err := openStore()
	if err != nil {
		log.Fatalf("open store: %v", err)
	}
	defer store.Close()

	keys, ok := store.(clientAdminStore)
	if !ok {
		log.Fatalf("backend %T does not support API key management", store)
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

// clientAdminStore is the credential-management surface of a backend.
type clientAdminStore interface {
	UpsertAPIKey(rec storage.APIKeyRecord) error
	ListAPIKeys() ([]storage.APIKeyRecord, error)
	RevokeAPIKey(clientName string) (int64, error)
	ConsumeEnrollCode(codeHash string) (storage.APIKeyRecord, error)
	RevokeAgent(tenantID, serverID string) (int64, error)
	ListAgents(tenantID string) ([]storage.APIKeyRecord, error)
	LookupAPIKey(keyHash string) (storage.APIKeyRecord, error)
}

func newEnrollCode(keys clientAdminStore, name string, ttl time.Duration, maxUses int) {
	code, err := storage.GenerateEnrollCode()
	if err != nil {
		log.Fatalf("generate enroll code: %v", err)
	}

	var tenantID string
	allKeys, err := keys.ListAPIKeys()
	if err != nil {
		log.Fatalf("list keys: %v", err)
	}
	tenantID, err = storage.UniqueTenantForClientName(allKeys, name)
	if err != nil {
		log.Fatalf("client name %q is ambiguous; pass a unique name or create the code with an explicit tenant", name)
	}
	if tenantID == "" {
		tenantID, err = storage.NewTenantID()
		if err != nil {
			log.Fatalf("generate tenant id: %v", err)
		}
	}

	codeHash := storage.HashEnrollCode(code)
	expiresAt := time.Now().Add(ttl)

	if err := keys.UpsertAPIKey(storage.APIKeyRecord{
		TenantID:   tenantID,
		ClientName: name,
		KeyHash:    codeHash,
		KeyPrefix:  "wme_",
		Kind:       storage.KindEnroll,
		Scope:      storage.ScopeEnroll,
		ExpiresAt:  expiresAt,
		MaxUses:    maxUses,
		IssuedBy:   "cli",
		CreatedAt:  time.Now(),
	}); err != nil {
		log.Fatalf("save enroll code: %v", err)
	}

	fmt.Printf("Enrollment Code: %s\n", code)
	fmt.Printf("Client:          %s\n", name)
	fmt.Printf("Tenant ID:       %s\n", tenantID)
	fmt.Printf("Expires:         %s (%v)\n", expiresAt.Format("2006-01-02 15:04:05"), ttl)
	fmt.Printf("Max Uses:        %d\n\n", maxUses)
	fmt.Println("Agents can enroll with this code using:")
	fmt.Printf("  wmonitor.exe -agent <hub-url> -enroll-code %s\n", code)
}

func newAdminToken(keys clientAdminStore) {
	token, err := storage.GenerateToken(storage.KindAdmin)
	if err != nil {
		log.Fatalf("generate admin token: %v", err)
	}
	hash := storage.HashAPIKey(token)
	prefix := storage.ExtractKeyPrefix(token)

	if err := keys.UpsertAPIKey(storage.APIKeyRecord{
		TenantID:   "admin",
		ClientName: "Admin",
		KeyHash:    hash,
		KeyPrefix:  prefix,
		Kind:       storage.KindAdmin,
		Scope:      storage.ScopeAdmin,
		IssuedBy:   "cli",
		CreatedAt:  time.Now(),
	}); err != nil {
		log.Fatalf("save admin token: %v", err)
	}

	fmt.Printf("Admin Token: %s\n\n", token)
	fmt.Println("Store this admin token securely. Set it as WMONITOR_ADMIN_TOKEN in CI/build scripts.")
}

func listAgents(keys clientAdminStore, filter string) {
	var tenantID string
	if filter != "" && filter != "all" && filter != "*" {
		allKeys, _ := keys.ListAPIKeys()
		for _, k := range allKeys {
			if strings.EqualFold(k.ClientName, filter) || strings.EqualFold(k.TenantID, filter) {
				tenantID = k.TenantID
				break
			}
		}
		if tenantID == "" {
			tenantID = filter
		}
	}

	agents, err := keys.ListAgents(tenantID)
	if err != nil {
		log.Fatalf("list agents: %v", err)
	}
	if len(agents) == 0 {
		fmt.Printf("No enrolled agents found for %q.\n", filter)
		return
	}
	fmt.Printf("%-24s %-20s %-10s %-20s %s\n", "SERVER ID", "CLIENT", "STATUS", "LAST SEEN", "TOKEN PREFIX")
	for _, a := range agents {
		status := "active"
		if a.Revoked {
			status = "revoked"
		}
		lastSeen := "never"
		if a.LastSeenAt.Unix() > 0 {
			lastSeen = a.LastSeenAt.Format("2006-01-02 15:04:05")
		}
		fmt.Printf("%-24s %-20s %-10s %-20s %s\n", a.ServerID, a.ClientName, status, lastSeen, a.KeyPrefix)
	}
}

func revokeAgent(keys clientAdminStore, serverID string) {
	agents, err := keys.ListAgents("")
	if err != nil {
		log.Fatalf("list agents: %v", err)
	}
	var matches []storage.APIKeyRecord
	for _, a := range agents {
		if strings.EqualFold(a.ServerID, serverID) && !a.Revoked {
			matches = append(matches, a)
		}
	}
	tenants := map[string]struct{}{}
	for _, a := range matches {
		tenants[a.TenantID] = struct{}{}
	}
	if len(tenants) > 1 {
		log.Fatalf("server ID %q exists in %d tenants; refusing to revoke by display identity. Use tenant-scoped revocation.", serverID, len(tenants))
	}
	if len(matches) == 0 {
		fmt.Printf("No active agent token found for server ID %q.\n", serverID)
		return
	}
	var count int64
	for _, a := range matches {
		n, err := keys.RevokeAgent(a.TenantID, a.ServerID)
		if err != nil {
			log.Fatalf("revoke agent: %v", err)
		}
		count += n
	}
	fmt.Printf("Revoked %d agent token(s) for server ID %q.\n", count, serverID)
}

func addClient(keys clientAdminStore, name string) {
	readToken, err := storage.GenerateToken(storage.KindRead)
	if err != nil {
		log.Fatalf("generate read token: %v", err)
	}
	existing, err := keys.ListAPIKeys()
	if err != nil {
		log.Fatalf("list keys: %v", err)
	}
	if _, err := storage.UniqueTenantForClientName(existing, name); err != nil {
		log.Fatalf("client name %q is ambiguous", name)
	}
	for _, k := range existing {
		if strings.EqualFold(k.ClientName, name) {
			log.Fatalf("client name %q already exists (tenant %s); not minting a second identity", name, k.TenantID)
		}
	}
	tenantID, err := storage.NewTenantID()
	if err != nil {
		log.Fatalf("generate tenant id: %v", err)
	}

	if err := keys.UpsertAPIKey(storage.APIKeyRecord{
		KeyHash:    storage.HashAPIKey(readToken),
		KeyPrefix:  storage.ExtractKeyPrefix(readToken),
		Kind:       storage.KindRead,
		Scope:      storage.ScopeRead,
		TenantID:   tenantID,
		ClientName: name,
		IssuedBy:   "cli",
		CreatedAt:  time.Now(),
	}); err != nil {
		log.Fatalf("register key: %v", err)
	}

	fmt.Printf("Client:     %s\n", name)
	fmt.Printf("Tenant ID:  %s\n", tenantID)
	fmt.Printf("Read Token (Dashboard): %s\n\n", readToken)
	fmt.Println("Store this token now. Use it to log in to the central dashboard.")
	fmt.Printf("\nTo create an agent enrollment code for this client, run:\n")
	fmt.Printf("  wmonitor.exe -new-enroll-code %q\n", name)
}

func listClients(keys clientAdminStore) {
	records, err := keys.ListAPIKeys()
	if err != nil {
		log.Fatalf("list keys: %v", err)
	}
	if len(records) == 0 {
		fmt.Println("No API clients registered. Add one with: wmonitor -add-client <name>")
		return
	}
	fmt.Printf("%-20s %-38s %-10s %-20s %s\n", "CLIENT", "TENANT", "STATUS", "LAST SEEN", "KEY HASH (prefix)")
	for _, r := range records {
		status := "active"
		if r.Revoked {
			status = "revoked"
		}
		lastSeen := "never"
		if r.LastSeenAt.Unix() > 0 {
			lastSeen = r.LastSeenAt.Format("2006-01-02 15:04:05")
		}
		hashPrefix := r.KeyHash
		if len(hashPrefix) > 12 {
			hashPrefix = hashPrefix[:12]
		}
		fmt.Printf("%-20s %-38s %-10s %-20s %s\n", r.ClientName, r.TenantID, status, lastSeen, hashPrefix)
	}
}

func revokeClient(keys clientAdminStore, name string) {
	n, err := keys.RevokeAPIKey(name)
	if err != nil {
		log.Fatalf("revoke: %v", err)
	}
	if n == 0 {
		fmt.Printf("No active keys found for client %q.\n", name)
		return
	}
	fmt.Printf("Revoked %d key(s) for client %q. Hubs may cache the decision for up to 60s.\n", n, name)
}

// importClientsFromCSV reads a clients_registry.csv and upserts all keys into the database.
func importClientsFromCSV(keys clientAdminStore, path string) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	reader := csv.NewReader(f)
	reader.FieldsPerRecord = -1

	imported := 0
	row := 0
	for {
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return imported, fmt.Errorf("read %s: %w", path, err)
		}
		row++
		if row == 1 && strings.EqualFold(strings.TrimSpace(record[0]), "ClientName") {
			continue // header
		}
		if len(record) < 2 {
			continue
		}

		name := strings.TrimSpace(record[0])
		rawKey := strings.TrimSpace(record[1])
		if name == "" || rawKey == "" {
			continue
		}

		hash := storage.HashAPIKey(rawKey)
		if _, err := keys.LookupAPIKey(hash); err == nil || errors.Is(err, storage.ErrAPIKeyRevoked) {
			log.Printf("row %d (%s): key already registered; not importing (will not un-revoke)", row, name)
			continue
		}

		known, listErr := keys.ListAPIKeys()
		if listErr != nil {
			return imported, listErr
		}
		existingTenant, ambErr := storage.UniqueTenantForClientName(known, name)
		if ambErr != nil {
			log.Printf("row %d (%s): ambiguous client name; skipped", row, name)
			continue
		}
		if existingTenant != "" {
			log.Printf("row %d (%s): client name already bound to %s; skipped", row, name, existingTenant)
			continue
		}

		tenantID, err := storage.NewTenantID()
		if err != nil {
			return imported, err
		}

		if err := keys.UpsertAPIKey(storage.APIKeyRecord{
			KeyHash:    hash,
			TenantID:   tenantID,
			ClientName: name,
			Kind:       storage.KindLegacy,
			Scope:      storage.ScopeRead,
			IssuedBy:   "import-clients",
			CreatedAt:  time.Now(),
		}); err != nil {
			log.Printf("row %d (%s): %v", row, name, err)
			continue
		}
		imported++
	}
	return imported, nil
}

// importClients is an explicit, operator-run import. It never stores the raw
// key as tenant_id. Historical telemetry still using a plaintext tenant_id
// must be remapped with -migrate-opaque-tenants after a verified backup.
func importClients(keys clientAdminStore, path string) {
	imported, err := importClientsFromCSV(keys, path)
	if err != nil {
		log.Fatalf("open %s: %v", path, err)
	}

	fmt.Printf("\nImported %d new client key(s) as hashes with opaque tenant IDs.\n", imported)
	fmt.Printf("Existing or revoked hashes were left unchanged.\n")
	fmt.Printf("Now delete %s: it holds plaintext credentials that are no longer needed.\n", path)
	fmt.Printf("If historical metrics still use raw keys as tenant_id, run -migrate-opaque-tenants with a backup directory.\n")
}

// autoSeedHubKeys may register a never-seen configured token on first boot.
// It never imports CSV files and never un-revokes an existing hash.
func autoSeedHubKeys(store storage.Store) {
	adminStore, ok := store.(clientAdminStore)
	if !ok {
		return
	}

	log.Printf("[server] clients_registry.csv is not auto-imported; use -import-clients then -migrate-opaque-tenants")

	registerIfAbsent := func(plaintext, tenantID, clientName, kind, scope, issuedBy string) {
		if plaintext == "" {
			return
		}
		keyHash := storage.HashAPIKey(plaintext)
		_, err := adminStore.LookupAPIKey(keyHash)
		if errors.Is(err, storage.ErrAPIKeyRevoked) {
			log.Printf("[server] configured credential for %q is revoked; leaving it revoked", clientName)
			return
		}
		if err == nil {
			return
		}
		if !errors.Is(err, storage.ErrAPIKeyNotFound) {
			log.Printf("[server] lookup configured credential for %q: %v", clientName, err)
			return
		}
		rec := storage.APIKeyRecord{
			KeyHash:    keyHash,
			KeyPrefix:  storage.ExtractKeyPrefix(plaintext),
			TenantID:   tenantID,
			ClientName: clientName,
			Kind:       kind,
			Scope:      scope,
			IssuedBy:   issuedBy,
			CreatedAt:  time.Now(),
		}
		if rec.TenantID == "" {
			id, genErr := storage.NewTenantID()
			if genErr != nil {
				log.Printf("[server] generate tenant id: %v", genErr)
				return
			}
			rec.TenantID = id
		}
		if err := adminStore.UpsertAPIKey(rec); err != nil {
			log.Printf("[server] failed to register configured credential for %q: %v", clientName, err)
			return
		}
		log.Printf("[server] registered first-boot credential for %q (kind=%s scope=%s)", clientName, kind, scope)
	}

	registerIfAbsent(resolveAPIKey(), "", "default", storage.KindLegacy, storage.ScopeRead, "env")
	registerIfAbsent(strings.TrimSpace(os.Getenv("WMONITOR_ADMIN_TOKEN")), "admin", "Admin", storage.KindAdmin, storage.ScopeAdmin, "env")
}

func runOpaqueTenantMigration(store storage.Store) {
	db, ok := store.(*storage.DB)
	if !ok {
		log.Fatalf("-migrate-opaque-tenants currently supports SQLite only; snapshot Postgres separately and do not blindly downgrade credentials")
	}
	backupDir := strings.TrimSpace(*flagOpaqueBackupDir)
	if backupDir == "" {
		log.Fatalf("-opaque-tenant-backup-dir is required")
	}
	report, err := db.MigrateOpaqueTenants(backupDir)
	if err != nil {
		log.Fatalf("opaque tenant migration failed (database unchanged if the transaction rolled back): %v\nBackup (if written): %s", err, report.BackupPath)
	}
	fmt.Printf("Opaque tenant migration complete.\n")
	fmt.Printf("Backup:        %s\n", report.BackupPath)
	fmt.Printf("Credentials:   %d remapped\n", report.CredentialsMoved)
	fmt.Printf("Metrics rows:  %d remapped\n", report.MetricsMoved)
	fmt.Printf("Process rows:  %d remapped\n", report.ProcessesMoved)
	fmt.Printf("Rollback: stop the hub and replace the live DB with the backup file. Do not downgrade into legacy plaintext tenant IDs.\n")
}

// openStore opens the appropriate backend based on -db flag and DSN resolution.
// Returns the Store, and optionally the *storage.DB if SQLite (for Conn()).
func openStore() (storage.Store, *storage.DB, error) {
	switch *flagDB {
	case "postgres":
		dsn, err := resolveDSN()
		if err != nil {
			return nil, nil, err
		}
		// Log host/dbname only — never log user/password
		logSafeDSN(dsn)
		pg, err := storage.OpenPostgres(dsn)
		if err != nil {
			return nil, nil, fmt.Errorf("postgres: %w", err)
		}
		return pg, nil, nil

	default: // "sqlite"
		dataDir, err := storage.DataDir()
		if err != nil {
			return nil, nil, fmt.Errorf("data dir: %v", err)
		}
		dbPath := dataDir + "/wmonitor.db"
		log.Printf("[wmonitor] DB path: %s", dbPath)
		db, err := storage.Open(dbPath)
		if err != nil {
			return nil, nil, fmt.Errorf("open sqlite: %w", err)
		}
		return db, db, nil
	}
}

// resolveDSN reads the Postgres DSN in order:
// 1. WMONITOR_DB_DSN environment variable
// 2. -dsn-file flag (first non-empty line)
// 3. -dsn flag (quick-test fallback only)
func resolveDSN() (string, error) {
	var dsn string
	switch {
	case os.Getenv("WMONITOR_DB_DSN") != "":
		dsn = os.Getenv("WMONITOR_DB_DSN")
	case *flagDSNFile != "":
		content, err := os.ReadFile(*flagDSNFile)
		if err != nil {
			return "", fmt.Errorf("read dsn-file %s: %w", *flagDSNFile, err)
		}
		scanner := bufio.NewScanner(strings.NewReader(string(content)))
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line != "" && !strings.HasPrefix(line, "#") {
				dsn = line
				break
			}
		}
		if dsn == "" {
			return "", fmt.Errorf("dsn-file %s is empty or contains only comments", *flagDSNFile)
		}
	case *flagDSN != "":
		log.Println("[wmonitor] WARNING: using -dsn flag — DSN visible in process list. Use WMONITOR_DB_DSN env var for production.")
		dsn = *flagDSN
	default:
		return "", fmt.Errorf("postgres backend requires a DSN: set WMONITOR_DB_DSN env var, use -dsn-file, or -dsn (test only)")
	}
	if err := rejectInsecureRemotePostgres(dsn); err != nil {
		return "", err
	}
	return dsn, nil
}

func rejectInsecureRemotePostgres(dsn string) error {
	host := postgresDSNHost(dsn)
	if host == "" || isLoopbackDSNHost(host) {
		return nil
	}
	mode := postgresDSNSSLMode(dsn)
	switch mode {
	case "require", "verify-ca", "verify-full":
		return nil
	default:
		return fmt.Errorf("postgres: remote DSN must set sslmode=require, verify-ca, or verify-full (got %q)", mode)
	}
}

func postgresDSNHost(dsn string) string {
	u, err := url.Parse(dsn)
	if err == nil && u.Host != "" {
		return strings.ToLower(u.Hostname())
	}
	return strings.ToLower(dsnKV(dsn, "host"))
}

func postgresDSNSSLMode(dsn string) string {
	u, err := url.Parse(dsn)
	if err == nil {
		if m := strings.ToLower(u.Query().Get("sslmode")); m != "" {
			return m
		}
	}
	return strings.ToLower(dsnKV(dsn, "sslmode"))
}

func dsnKV(dsn, key string) string {
	normalized := strings.ReplaceAll(dsn, ";", " ")
	for _, part := range strings.Fields(normalized) {
		k, v, ok := strings.Cut(part, "=")
		if ok && strings.EqualFold(strings.TrimSpace(k), key) {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func isLoopbackDSNHost(host string) bool {
	h := strings.ToLower(strings.Trim(host, "[]"))
	return h == "localhost" || h == "::1" || h == "127.0.0.1" || strings.HasPrefix(h, "127.")
}

// logSafeDSN logs only host/dbname from the DSN, never user/password.
func logSafeDSN(dsn string) {
	u, err := url.Parse(dsn)
	if err != nil {
		log.Printf("[wmonitor] DB backend: postgres (DSN parse error — proceeding)")
		return
	}
	log.Printf("[wmonitor] DB backend: postgres @ %s%s", u.Hostname(), u.Path)
}

// resolveAPIKey reads API key from -api-key flag, WMONITOR_API_KEY env var, or build-time default.
func resolveAPIKey() string {
	if *flagAPIKey != "" {
		return *flagAPIKey
	}
	if v := os.Getenv("WMONITOR_API_KEY"); v != "" {
		return v
	}
	return defaultAPIKey
}

// resolveEnrollCode reads enrollment code from -enroll-code flag, WMONITOR_ENROLL_CODE env var, or build-time default.
func resolveEnrollCode() string {
	if *flagEnrollCode != "" {
		return *flagEnrollCode
	}
	if v := os.Getenv("WMONITOR_ENROLL_CODE"); v != "" {
		return v
	}
	return defaultEnrollCode
}

// parseAppPorts parses a comma-separated list of port numbers.
func parseAppPorts(raw string) []uint32 {
	if raw == "" {
		return nil
	}
	var ports []uint32
	for _, p := range strings.Split(raw, ",") {
		p = strings.TrimSpace(p)
		if val, err := strconv.Atoi(p); err == nil && val > 0 && val <= 65535 {
			ports = append(ports, uint32(val))
		}
	}
	return ports
}

func exportTenant() string {
	if t := strings.TrimSpace(*flagTenant); t != "" {
		return t
	}
	return storage.LocalTenantID
}

// ensureAgentCredentials resolves or auto-enrolls agent credentials.
func ensureAgentCredentials() string {
	hubURL := strings.TrimRight(*flagAgentHub, "/")
	if err := agent.RequireHTTPSHub(hubURL); err != nil {
		log.Fatalf("[agent] %v", err)
	}
	serverID := resolveServerID()
	hostname, _ := os.Hostname()

	apiKey := resolveAPIKey()
	enrollCode := resolveEnrollCode()

	if apiKey != "" {
		return apiKey
	}

	if creds, err := agent.LoadCredentials(); err == nil && creds != nil && creds.Token != "" {
		if !agent.SameHubOrigin(creds.HubURL, hubURL) {
			log.Fatalf("[agent] stored credentials are bound to %s, not %s; re-enroll at the new hub (previous token was not sent)", creds.HubURL, hubURL)
		}
		log.Printf("[agent] using previously enrolled credentials (server_id=%s)", creds.ServerID)
		return creds.Token
	}

	if enrollCode != "" {
		log.Printf("[agent] performing first-run enrollment with hub %s (please wait if hub is waking up)...", hubURL)
		ctx, cancel := context.WithTimeout(context.Background(), 75*time.Second)
		defer cancel()
		creds, err := agent.Enroll(ctx, hubURL, enrollCode, serverID, hostname, buildVersion, "")
		if err != nil {
			log.Fatalf("[agent] enrollment failed: %v", err)
		}
		log.Printf("[agent] successfully enrolled! Machine token saved to local credential store.")
		return creds.Token
	}

	log.Fatal("[agent] API key or enrollment code required: use -enroll-code <code or -api-key <key>")
	return ""
}

// runAgentAsService runs the agent under the Windows Service Control Manager.
func runAgentAsService(apiKey string) {
	hubURL := strings.TrimRight(*flagAgentHub, "/")
	serverID := resolveServerID()

	ag := agent.New(hubURL, apiKey)
	bindAgentSpool(ag)
	defer ag.Close()

	col := collector.New(ag)
	if *flagExternalIface != "" {
		col.SetExternalIface(*flagExternalIface)
	}
	configureUserTracker(col)
	col.SetServerID(serverID)

	prg := &program{
		store:     ag,
		collector: col,
	}

	svcConfig := &service.Config{
		Name:        "wmonitor",
		DisplayName: "W-Monitor System Monitor",
		Description: "Pushes system metrics to central hub at " + hubURL,
	}

	svc, err := service.New(prg, svcConfig)
	if err != nil {
		log.Fatalf("[agent] service.New: %v", err)
	}

	if err := svc.Run(); err != nil {
		log.Fatalf("[agent] service run: %v", err)
	}
}

// runAgentMode starts the collector in agent mode — no local DB, no dashboard.
// Metrics are POSTed to the hub's /api/ingest endpoint.
func runAgentMode() {
	apiKey := ensureAgentCredentials()

	// If running under Service Manager (non-interactive):
	if !service.Interactive() {
		runAgentAsService(apiKey)
		return
	}

	hubURL := strings.TrimRight(*flagAgentHub, "/")
	serverID := resolveServerID()

	log.Printf("[agent] starting in foreground mode (version %s) — pushing to %s (server: %s)", buildVersion, hubURL, serverID)

	ctx, cancel := context.WithCancel(context.Background())

	ag := agent.New(hubURL, apiKey)
	bindAgentSpool(ag)
	ag.StartDrainer(ctx)
	defer ag.Close()

	col := collector.New(ag)
	if *flagExternalIface != "" {
		col.SetExternalIface(*flagExternalIface)
	}
	configureUserTracker(col)
	col.SetServerID(serverID)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigCh
		log.Println("[agent] stopping")
		cancel()
	}()

	col.Run(ctx) // blocks
}

// resolveServerID returns a stable identifier for this agent.
//
// Hostname alone is not unique: two clients each running a machine called
// WIN-SERVER would merge into a single series inside a tenant, and renaming a
// host would orphan all of its history. An identifier is generated once and
// persisted in the data directory.
func resolveServerID() string {
	hostname, _ := os.Hostname()
	if hostname == "" {
		hostname = "unknown-host"
	}

	dir, err := storage.DataDir()
	if err != nil {
		return hostname
	}
	idPath := filepath.Join(dir, "agent_id")

	if data, err := os.ReadFile(idPath); err == nil {
		if id := strings.TrimSpace(string(data)); id != "" {
			return id
		}
	}

	suffix, err := storage.NewTenantID()
	if err != nil {
		return hostname
	}
	// Human-readable prefix so operators can still recognise the machine.
	trimmed := strings.TrimPrefix(suffix, "t_")
	if len(trimmed) > 8 {
		trimmed = trimmed[:8]
	}
	id := hostname + "-" + trimmed
	if err := os.WriteFile(idPath, []byte(id), 0o600); err != nil {
		log.Printf("[agent] could not persist server id (%v); falling back to hostname", err)
		return hostname
	}
	log.Printf("[agent] generated stable server id %q", id)
	return id
}

// loadConfigEnv attempts to load environment variables from config.env in:
// 1. DataDir()/config.env
// 2. Executable dir/config.env
// 3. Current working dir/config.env
func loadConfigEnv() {
	var candidates []string
	if d, err := storage.DataDir(); err == nil {
		candidates = append(candidates, filepath.Join(d, "config.env"))
	}
	if exe, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(exe), "config.env"))
	}
	candidates = append(candidates, machineConfigEnvPath())
	candidates = append(candidates, "config.env")

	for _, path := range candidates {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		scanner := bufio.NewScanner(strings.NewReader(string(data)))
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			parts := strings.SplitN(line, "=", 2)
			if len(parts) == 2 {
				k := strings.TrimSpace(parts[0])
				v := strings.TrimSpace(parts[1])
				v = strings.Trim(v, `"'`)
				if os.Getenv(k) == "" {
					os.Setenv(k, v)
				}
			}
		}
		break
	}
}

func machineConfigEnvPath() string {
	if runtime.GOOS == "windows" {
		pd := os.Getenv("PROGRAMDATA")
		if pd == "" {
			pd = `C:\ProgramData`
		}
		return filepath.Join(pd, "wmonitor", "config.env")
	}
	return "/etc/wmonitor/config.env"
}

func bindAgentSpool(ag *agent.Agent) {
	if creds, err := agent.LoadCredentials(); err == nil && creds != nil {
		ag.BindIdentity(creds.TenantID, creds.ServerID)
	}
}

var secretServiceFlags = map[string]struct{}{
	"-api-key": {}, "--api-key": {},
	"-dsn": {}, "--dsn": {},
	"-enroll-code": {}, "--enroll-code": {},
	"-alert-slack": {}, "--alert-slack": {},
}

// serviceSafeArgs drops install and secret flags so they never appear in the
// SCM/systemd argument list. Secrets belong in config.env / environment.
func serviceSafeArgs(args []string) []string {
	out := make([]string, 0, len(args))
	skipVal := false
	for _, arg := range args {
		if skipVal {
			skipVal = false
			continue
		}
		if arg == "-install" || arg == "--install" {
			continue
		}
		name, _, hasEq := strings.Cut(arg, "=")
		if _, secret := secretServiceFlags[name]; secret {
			if !hasEq {
				skipVal = true
			}
			continue
		}
		out = append(out, arg)
	}
	return out
}
