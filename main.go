// wmonitor — single-binary system monitoring tool
//
// Usage:
//
//	wmonitor                      # run in foreground
//	wmonitor -install             # install as system service
//	wmonitor -uninstall           # remove system service
//	wmonitor -port 9090           # specify HTTP port (default: 8080)
//	wmonitor -export-csv <path>   # export 30-day CSV report and exit
//	wmonitor -export-txt <path>   # export 30-day text report and exit
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

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
	flagPort       = flag.String("port", "8080", "HTTP port for the dashboard")
	flagInstall    = flag.Bool("install", false, "Install wmonitor as a system service")
	flagUninstall  = flag.Bool("uninstall", false, "Uninstall the wmonitor service")
	flagStart      = flag.Bool("start", false, "Start the installed service")
	flagStop       = flag.Bool("stop", false, "Stop the running service")
	flagExportCSV  = flag.String("export-csv", "", "Export 30-day CSV report to this path and exit")
	flagExportTxt  = flag.String("export-txt", "", "Export 30-day text report to this path and exit")
	flagRunFor     = flag.Duration("run-for", 0, "Run for this duration then export and exit (e.g. 1h, 30m)")
	flagExportFlt  = flag.String("export-filter", "", "Filter for automatic export (daily, weekly, monthly)")
)

// ── Service program ──

type program struct {
	db        *storage.DB
	srv       *server.Server
	collector *collector.Collector
	retention *retention.Job
	cancel    context.CancelFunc
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

	// Retention job (hourly)
	go func() {
		// Run once immediately on startup
		if err := p.retention.Run(); err != nil {
			log.Printf("[wmonitor] initial retention run: %v", err)
		}
		p.retention.RunScheduled()
	}()

	// HTTP server (blocking)
	if err := p.srv.Start(); err != nil {
		log.Printf("[wmonitor] server error: %v", err)
	}
}

func (p *program) Stop(s service.Service) error {
	log.Println("[wmonitor] service stopping")
	if p.cancel != nil {
		p.cancel()
	}
	if p.db != nil {
		p.db.Close()
	}
	return nil
}

// ── Main ──

func main() {
	flag.Parse()

	// ── Open DB ──
	dataDir, err := storage.DataDir()
	if err != nil {
		log.Fatalf("data dir: %v", err)
	}
	dbPath := dataDir + "/wmonitor.db"

	db, err := storage.Open(dbPath)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	log.Printf("[wmonitor] DB path: %s", dbPath)

	// ── Export modes ──
	if *flagExportCSV != "" {
		n, err := export.CSVReport(db, time.Now().Add(-30*24*time.Hour), *flagExportCSV)
		if err != nil {
			log.Fatalf("CSV export: %v", err)
		}
		fmt.Printf("Exported %d rows to %s\n", n, *flagExportCSV)
		db.Close()
		return
	}
	if *flagExportTxt != "" {
		s, err := export.TextReport(db, time.Now().Add(-30*24*time.Hour), *flagExportTxt)
		if err != nil {
			log.Fatalf("text export: %v", err)
		}
		fmt.Printf("Report written to %s (%d samples, avg CPU %.1f%%)\n", *flagExportTxt, s.RowCount, s.AvgCPU)
		db.Close()
		return
	}

	// ── Build service program ──
	col := collector.New(db)
	ret := retention.New(db.Conn())
	srv := server.New(db, *flagPort)
	col.SetUserTracker(srv)

	if err := dashboard.Register(srv); err != nil {
		log.Fatalf("dashboard register: %v", err)
	}

	prg := &program{
		db:        db,
		srv:       srv,
		collector: col,
		retention: ret,
	}

	// ── Service config ──
	svcConfig := &service.Config{
		Name:        "wmonitor",
		DisplayName: "W-Monitor System Monitor",
		Description: "Collects system metrics and serves the monitoring dashboard at localhost:" + *flagPort,
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
	// If running as a service, hand off to the service runner.
	// If running interactively (not as a service), run directly.
	isInteractive := service.Interactive()
	if !isInteractive {
		// Running under the service manager
		if err := svc.Run(); err != nil {
			log.Fatalf("service run: %v", err)
		}
		return
	}

	// Foreground mode: start collector + server directly
	log.Printf("[wmonitor] starting in foreground mode, dashboard at http://localhost:%s", *flagPort)

	ctx, cancel := context.WithCancel(context.Background())

	// Collector
	go col.Run(ctx)

	// Retention (initial run + scheduled)
	go func() {
		if err := ret.Run(); err != nil {
			log.Printf("[wmonitor] retention: %v", err)
		}
		ret.RunScheduled()
	}()

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
				n, err := export.CSVReport(db, since, outPath)
				if err != nil {
					log.Printf("Auto-export CSV error: %v", err)
				} else {
					log.Printf("Auto-exported %d rows to %s", n, outPath)
				}
			} else {
				outPath := fmt.Sprintf("wmonitor_export_%s.txt", time.Now().Format("20060102_150405"))
				s, err := export.TextReport(db, since, outPath)
				if err != nil {
					log.Printf("Auto-export TXT error: %v", err)
				} else {
					log.Printf("Auto-exported report to %s (avg CPU %.1f%%)", outPath, s.AvgCPU)
				}
			}
		}

		db.Close()
		os.Exit(0)
	}()

	// HTTP server (blocking)
	if err := srv.Start(); err != nil {
		log.Fatalf("server: %v", err)
	}
}
