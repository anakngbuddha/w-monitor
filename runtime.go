package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"runtime"
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

type program struct {
	store storage.Store
	srv *server.Server
	collector *collector.Collector
	retention *retention.Job
	evaluator *alerting.Evaluator
	sqliteDB *storage.DB
	mu sync.Mutex
	ctx context.Context
	cancel context.CancelFunc
	collectorWg sync.WaitGroup
	stopOnce sync.Once
	stopErr error
	errors chan error
}

func (p *program) Start(service.Service) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cancel != nil { return errors.New("service already started") }
	p.ctx, p.cancel = context.WithCancel(context.Background())
	p.errors = make(chan error, 1)
	start := func(work func()) { p.collectorWg.Add(1); go func() { defer p.collectorWg.Done(); work() }() }
	if p.collector != nil { start(func() { p.collector.Run(p.ctx) }) }
	if p.evaluator != nil { start(func() { p.evaluator.Run(p.ctx) }) }
	if p.retention != nil { start(func() { p.retention.RunContext(p.ctx) }) }
	if p.srv != nil { start(func() { if err := p.srv.Start(); err != nil { p.errors <- err; p.cancel() } }) }
	return nil
}

func (p *program) Stop(service.Service) error {
	p.stopOnce.Do(func() {
		p.mu.Lock()
		if p.cancel != nil { p.cancel() }
		p.mu.Unlock()
		if p.srv != nil {
			ctx, cancel := context.WithTimeout(context.Background(), server.ShutdownGrace())
			p.stopErr = p.srv.Shutdown(ctx)
			cancel()
		}
		p.collectorWg.Wait()
		// If handlers could not drain, do not close their live DB underneath
		// them. Report failure to the service manager instead of claiming stop.
		if p.stopErr != nil { return }
		if p.store != nil {
			if *flagAgentHub == "" && (*flagRunFor > 0 || *flagExportFlt != "") { writeShutdownExport(p.store) }
			p.stopErr = p.store.Close()
		}
	})
	return p.stopErr
}

func runForeground(p *program) error {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sig)
	if err := p.Start(nil); err != nil { return err }
	var timer *time.Timer
	var deadline <-chan time.Time
	if *flagRunFor > 0 { timer = time.NewTimer(*flagRunFor); deadline = timer.C; defer timer.Stop() }
	var runErr error
	select { case <-sig: case <-deadline: case runErr = <-p.errors: }
	if err := p.Stop(nil); err != nil { return err }
	return runErr
}

func makeProgram() (*program, error) {
	if *flagAgentHub != "" {
		key, err := agentCredentials()
		if err != nil { return nil, err }
		ag := agent.New(*flagAgentHub, key)
		bindAgentSpool(ag)
		if err := ag.InitializationError(); err != nil { ag.Close(); return nil, err }
		col := collector.New(ag)
		col.SetServerID(resolveServerID())
		if *flagExternalIface != "" { col.SetExternalIface(*flagExternalIface) }
		configureUserTracker(col)
		return &program{store: ag, collector: col}, nil
	}
	store, sqliteDB, err := openStore()
	if err != nil { return nil, err }
	var ret *retention.Job
	if sqliteDB != nil { ret = retention.New(sqliteDB.Conn()); ret.SetDataPath(sqliteDB.Path) } else if pruner, ok := store.(retention.Pruner); ok { ret = retention.NewWithPruner(pruner) }
	if ret != nil {
		if budget, ok := positiveEnvInt("WMONITOR_DISK_BUDGET_BYTES"); ok { ret.SetDiskBudget(budget) }
	}
	srv := server.New(store, *flagPort)
	if ret != nil { srv.SetRetentionStatusProvider(func() map[string]interface{} {
		_ = ret.CheckCapacity()
		status := ret.Status()
		return map[string]interface{}{"downsampling_enabled": false, "purge_enabled": false, "reason": retention.DisabledReason, "disk_pressure": status.DiskPressure, "disk_used_bytes": status.DiskUsedBytes, "disk_budget_bytes": status.DiskBudgetBytes}
	}) }
	if *flagHub {
		keys, ok := store.(server.KeyStore)
		if !ok { store.Close(); return nil, errors.New("backend cannot authenticate Hub requests") }
		// Deliberately no automatic CSV or configuration-key bootstrap. Explicit
		// -new-admin-token / -add-client workflows own credential issuance.
		srv.EnableHubMode(keys)
	}
	if err := dashboard.Register(srv); err != nil { store.Close(); return nil, err }
	var col *collector.Collector
	if !*flagHub {
		var sink storage.Store = store
		if ret != nil { sink = capacityStore{Store: store, retention: ret} }
		col = collector.New(sink)
		if *flagExternalIface != "" { col.SetExternalIface(*flagExternalIface) }
		configureUserTracker(col)
	}
	return &program{store: store, srv: srv, collector: col, retention: ret, evaluator: buildEvaluator(store, srv), sqliteDB: sqliteDB}, nil
}

type capacityStore struct { storage.Store; retention *retention.Job }
func (s capacityStore) InsertMetric(row storage.MetricRow) error { if err := s.retention.CheckCapacity(); err != nil { return err }; return s.Store.InsertMetric(row) }
func (s capacityStore) InsertProcess(row storage.ProcessRow) error { if err := s.retention.CheckCapacity(); err != nil { return err }; return s.Store.InsertProcess(row) }

func configureUserTracker(col *collector.Collector) {
	tracker, ok := col.UserTracker().(*collector.TCPUserTracker)
	if !ok { return }
	tracker.SetAppPorts(parseAppPorts(*flagAppPort)...)
	ports := parseAppPorts(*flagPort)
	if len(ports) == 1 { tracker.SetExcludePorts(ports[0]) }
	tracker.SetWindow(*flagUserWindow)
}

func agentCredentials() (string, error) {
	if err := agent.RequireHTTPSHub(*flagAgentHub); err != nil { return "", err }
	if key := resolveAPIKey(); key != "" { return key, nil }
	if creds, err := agent.LoadCredentials(); err == nil && creds != nil {
		if !agent.SameHubOrigin(creds.HubURL, *flagAgentHub) || creds.ServerID != resolveServerID() { return "", errors.New("stored credential destination or identity changed; explicit re-enrollment required") }
		return creds.Token, nil
	} else if err != nil && !errors.Is(err, agent.ErrNoCredentials) { return "", errors.New("stored credentials could not be read safely") }
	code := resolveEnrollCode()
	if code == "" { return "", errors.New("configure an agent credential or enrollment code in the protected config") }
	hostname, _ := os.Hostname()
	ctx, cancel := context.WithTimeout(context.Background(), 75*time.Second)
	defer cancel()
	creds, err := agent.Enroll(ctx, *flagAgentHub, code, resolveServerID(), hostname, buildVersion, "")
	if err != nil { return "", errors.New("enrollment failed; no credential was logged") }
	return creds.Token, nil
}

func ensureAgentCredentials() string { key, err := agentCredentials(); if err != nil { log.Fatal(err) }; return key }
func bindAgentSpool(ag *agent.Agent) { if resolveAPIKey() == "" { if creds, err := agent.LoadCredentials(); err == nil && creds != nil { ag.BindIdentity(creds.TenantID, creds.ServerID) } } }
func runAgentMode() { p, err := makeProgram(); if err != nil { log.Fatal(err) }; if err := runForeground(p); err != nil { log.Fatal(err) } }
func runAgentAsService(apiKey string) { p, err := makeProgram(); if err != nil { log.Fatal(err) }; svc, err := service.New(p, &service.Config{Name: "wmonitor"}); if err != nil { log.Fatal(err) }; if err := svc.Run(); err != nil { log.Fatal(err) } }

func runOneShotExport() bool {
	if *flagExportCSV == "" && *flagExportTxt == "" && *flagAssessmentReport == "" { return false }
	if *flagHub && strings.TrimSpace(*flagTenant) == "" { log.Fatal("Hub export requires an explicit -tenant") }
	store, _, err := openStore()
	if err != nil { log.Fatal("export storage unavailable") }
	defer store.Close()
	start, end := time.Now().Add(-*flagSince), time.Now()
	switch { case *flagExportCSV != "": _, err = export.CSVReport(store, start, *flagExportCSV, exportTenant()); case *flagExportTxt != "": _, err = export.TextReport(store, start, *flagExportTxt, exportTenant()); default: err = export.GenerateAssessmentReport(store, start, end, *flagAssessmentReport, exportTenant()) }
	if err != nil { log.Fatal("export failed") }
	fmt.Println("Scoped export written.")
	return true
}

func writeShutdownExport(store storage.Store) {
	window := *flagRunFor
	switch *flagExportFlt { case "daily": window = 24*time.Hour; case "weekly": window = 7*24*time.Hour; case "monthly": window = 30*24*time.Hour }
	if window <= 0 { window = 24*time.Hour }
	if *flagHub && *flagTenant == "" { log.Print("[export] skipped: explicit tenant required"); return }
	name := "wmonitor_export_"+time.Now().Format("20060102_150405")
	var err error
	if runtime.GOOS == "windows" { _, err = export.CSVReport(store, time.Now().Add(-window), name+".csv", exportTenant()) } else { _, err = export.TextReport(store, time.Now().Add(-window), name+".txt", exportTenant()) }
	if err != nil { log.Print("[export] shutdown export failed") }
}
