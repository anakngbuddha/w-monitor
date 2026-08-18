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
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"log"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"Zeus/agent"
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
	flagInstall   = flag.Bool("install", false, "Install wmonitor as a system service")
	flagUninstall = flag.Bool("uninstall", false, "Uninstall the wmonitor service")
	flagStart     = flag.Bool("start", false, "Start the installed service")
	flagStop      = flag.Bool("stop", false, "Stop the running service")
	flagExportCSV = flag.String("export-csv", "", "Export 30-day CSV report to this path and exit")
	flagExportTxt = flag.String("export-txt", "", "Export 30-day text report to this path and exit")
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
)

// ── Service program ──

type program struct {
	store     storage.Store
	srv       *server.Server
	collector *collector.Collector
	retention *retention.Job
	cancel    context.CancelFunc
	// Keep *storage.DB around for Conn() (retention uses raw *sql.DB)
	sqliteDB *storage.DB
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
	go p.collector.Run(ctx)

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
	if p.cancel != nil {
		p.cancel()
	}
	if p.store != nil {
		p.store.Close()
	}
	return nil
}

// ── Main ──

func main() {
	flag.Parse()
	loadConfigEnv()

	// Apply config.env fallbacks if flags were not explicitly provided
	if *flagAgentHub == "" {
		if hub := os.Getenv("WMONITOR_AGENT_HUB"); hub != "" {
			*flagAgentHub = hub
		}
	}
	if *flagDB == "sqlite" {
		if dbVal := os.Getenv("WMONITOR_DB"); dbVal != "" {
			*flagDB = dbVal
		}
	}
	if !*flagHub {
		if os.Getenv("WMONITOR_MODE") == "hub" || os.Getenv("WMONITOR_HUB") == "true" {
			*flagHub = true
		}
	}
	if *flagPort == "8080" {
		if p := os.Getenv("WMONITOR_PORT"); p != "" {
			*flagPort = p
		} else if p := os.Getenv("PORT"); p != "" {
			*flagPort = p
		}
	}
	if *flagExternalIface == "" {
		if ifc := os.Getenv("WMONITOR_EXTERNAL_IFACE"); ifc != "" {
			*flagExternalIface = ifc
		}
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
		n, err := export.CSVReport(store, time.Now().Add(-*flagSince), *flagExportCSV)
		if err != nil {
			log.Fatalf("CSV export: %v", err)
		}
		fmt.Printf("Exported %d rows to %s\n", n, *flagExportCSV)
		store.Close()
		return
	}
	if *flagExportTxt != "" {
		s, err := export.TextReport(store, time.Now().Add(-*flagSince), *flagExportTxt)
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
		if err := export.GenerateAssessmentReport(store, start, end, *flagAssessmentReport); err != nil {
			log.Fatalf("assessment report: %v", err)
		}
		fmt.Printf("Assessment report written to %s\n", *flagAssessmentReport)
		store.Close()
		return
	}

	// ── Build service components ──
	apiKey := resolveAPIKey()

	col := collector.New(store)
	if *flagExternalIface != "" {
		col.SetExternalIface(*flagExternalIface)
	}

	// Retention only works with SQLite (uses raw *sql.DB). Postgres has no retention yet.
	var ret *retention.Job
	if sqliteDB != nil {
		ret = retention.New(sqliteDB.Conn())
	}

	srv := server.New(store, *flagPort)
	col.SetUserTracker(srv)

	if *flagHub {
		srv.EnableHubMode(apiKey)
	}

	if err := dashboard.Register(srv); err != nil {
		log.Fatalf("dashboard register: %v", err)
	}

	prg := &program{
		store:     store,
		srv:       srv,
		collector: col,
		retention: ret,
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
		Arguments:   svcArgs,
	}

	svc, err := service.New(prg, svcConfig)
	if err != nil {
		log.Fatalf("service.New: %v", err)
	}

	// ── Service control commands ──
	if *flagInstall {
		if err := svc.Install(); err != nil {
			log.Fatalf("install: %v", err)
		}
		fmt.Println("Service installed. Start it with: wmonitor -start")
		return
	}
	if *flagUninstall {
		if err := svc.Uninstall(); err != nil {
			log.Fatalf("uninstall: %v", err)
		}
		fmt.Println("Service uninstalled.")
		return
	}
	if *flagStart {
		if err := service.Control(svc, "start"); err != nil {
			log.Fatalf("start: %v", err)
		}
		fmt.Println("Service started.")
		return
	}
	if *flagStop {
		if err := service.Control(svc, "stop"); err != nil {
			log.Fatalf("stop: %v", err)
		}
		fmt.Println("Service stopped.")
		return
	}

	// ── Interactive/foreground mode ──
	isInteractive := service.Interactive()
	if !isInteractive {
		if err := svc.Run(); err != nil {
			log.Fatalf("service run: %v", err)
		}
		return
	}

	log.Printf("[wmonitor] starting in foreground mode, dashboard at http://localhost:%s", *flagPort)

	ctx, cancel := context.WithCancel(context.Background())

	go col.Run(ctx)

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
	go func() {
		select {
		case sig := <-sigCh:
			log.Printf("[wmonitor] received %v, shutting down", sig)
		case <-runTimer:
			log.Printf("[wmonitor] run-for duration (%s) elapsed, shutting down", *flagRunFor)
		}
		cancel()

		// Automated export on shutdown
		if *flagExportFlt != "" || *flagRunFor > 0 {
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
				n, err := export.CSVReport(store, since, outPath)
				if err != nil {
					log.Printf("Auto-export CSV error: %v", err)
				} else {
					log.Printf("Auto-exported %d rows to %s", n, outPath)
				}
			} else {
				outPath := fmt.Sprintf("wmonitor_export_%s.txt", time.Now().Format("20060102_150405"))
				s, err := export.TextReport(store, since, outPath)
				if err != nil {
					log.Printf("Auto-export TXT error: %v", err)
				} else {
					log.Printf("Auto-exported report to %s (avg CPU %.1f%%)", outPath, s.AvgCPU)
				}
			}
		}

		store.Close()
		os.Exit(0)
	}()

	// HTTP server (blocking)
	if err := srv.Start(); err != nil {
		log.Fatalf("server: %v", err)
	}
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
	if v := os.Getenv("WMONITOR_DB_DSN"); v != "" {
		return v, nil
	}
	if *flagDSNFile != "" {
		content, err := os.ReadFile(*flagDSNFile)
		if err != nil {
			return "", fmt.Errorf("read dsn-file %s: %w", *flagDSNFile, err)
		}
		scanner := bufio.NewScanner(strings.NewReader(string(content)))
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line != "" && !strings.HasPrefix(line, "#") {
				return line, nil
			}
		}
		return "", fmt.Errorf("dsn-file %s is empty or contains only comments", *flagDSNFile)
	}
	if *flagDSN != "" {
		log.Println("[wmonitor] WARNING: using -dsn flag — DSN visible in process list. Use WMONITOR_DB_DSN env var for production.")
		return *flagDSN, nil
	}
	return "", fmt.Errorf("postgres backend requires a DSN: set WMONITOR_DB_DSN env var, use -dsn-file, or -dsn (test only)")
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

// resolveAPIKey reads API key from -api-key flag or WMONITOR_API_KEY env var.
func resolveAPIKey() string {
	if *flagAPIKey != "" {
		return *flagAPIKey
	}
	return os.Getenv("WMONITOR_API_KEY")
}

// runAgentMode starts the collector in agent mode — no local DB, no dashboard.
// Metrics are POSTed to the hub's /api/ingest endpoint.
func runAgentMode() {
	apiKey := resolveAPIKey()
	if apiKey == "" {
		log.Fatal("[agent] API key required: set -api-key flag or WMONITOR_API_KEY env var")
	}

	hubURL := strings.TrimRight(*flagAgentHub, "/")
	log.Printf("[agent] starting — pushing to %s", hubURL)

	ag := agent.New(hubURL, apiKey)
	col := collector.New(ag)
	if *flagExternalIface != "" {
		col.SetExternalIface(*flagExternalIface)
	}
	// Use hostname as server_id for identification in the hub's DB
	h, _ := os.Hostname()
	col.SetServerID(h)

	ctx, cancel := context.WithCancel(context.Background())
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigCh
		log.Println("[agent] stopping")
		cancel()
	}()

	col.Run(ctx) // blocks
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
