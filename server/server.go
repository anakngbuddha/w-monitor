// Package server provides the HTTP API and embedded dashboard for sysmon.
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"Zeus/export"
	"Zeus/storage"
)

// BuildVersion is overridden from main via -ldflags at build time.
var BuildVersion = "dev"

// HTTP server timeouts.
//
// Previously this used a bare http.ListenAndServe, which applies no timeouts at
// all. A handful of connections trickling headers one byte at a time (Slowloris)
// could hold every goroutine open indefinitely.
const (
	readHeaderTimeout = 5 * time.Second
	readTimeout       = 30 * time.Second
	// Generous: CSV export of a wide time range legitimately streams for a while.
	writeTimeout    = 120 * time.Second
	idleTimeout     = 120 * time.Second
	maxHeaderBytes  = 1 << 20 // 1 MB
	shutdownGrace   = 10 * time.Second
	maxIngestBody   = 1 << 18 // 256 KB
	healthPingLimit = 2 * time.Second
)

// Server wraps the HTTP mux and storage store.
type Server struct {
	db         storage.Store
	port       string
	mux        *http.ServeMux
	httpServer *http.Server
	startedAt  time.Time

	mu               sync.Mutex
	dashboardViewers map[string]time.Time

	// Hub mode — when true, /api/ingest is enabled and all endpoints require a
	// registered API key that maps to a tenant.
	hubMode   bool
	keys      KeyStore
	authCache *authCache
	limiter   *rateLimiter

	allowedOrigins []string
}

// New creates a Server bound to the given port (e.g. "8080").
func New(db storage.Store, port string) *Server {
	s := &Server{
		db:               db,
		port:             port,
		mux:              http.NewServeMux(),
		startedAt:        time.Now(),
		dashboardViewers: make(map[string]time.Time),
		authCache:        newAuthCache(),
		limiter:          newRateLimiter(defaultRatePerSecond, defaultBurst),
		allowedOrigins:   parseAllowedOrigins(os.Getenv("WMONITOR_ALLOWED_ORIGINS")),
	}
	s.routes()
	return s
}

// EnableHubMode enables the /api/ingest endpoint and turns on tenant isolation.
//
// The previous signature took the API key as a string and then ignored it, so
// every endpoint accepted any non-empty X-API-Key value as a valid tenant. It
// now takes the credential store that keys are actually verified against.
func (s *Server) EnableHubMode(keys KeyStore) {
	s.hubMode = true
	s.keys = keys
	s.mux.HandleFunc("/api/ingest", s.handleIngest)
	if keys == nil {
		log.Println("[server] WARNING: hub mode enabled with no key store — all requests will be rejected")
		return
	}
	log.Println("[server] hub mode enabled — POST /api/ingest requires a registered API key")
}

// SetAllowedOrigins overrides the CORS allowlist.
func (s *Server) SetAllowedOrigins(origins []string) {
	s.allowedOrigins = origins
}

// routes registers all HTTP handlers.
func (s *Server) routes() {
	s.mux.HandleFunc("/api/metrics", s.handleMetrics)
	s.mux.HandleFunc("/api/processes", s.handleProcesses)
	s.mux.HandleFunc("/api/health", s.handleHealth)
	s.mux.HandleFunc("/api/ready", s.handleReady)
	s.mux.HandleFunc("/metrics", s.handlePrometheus)
	s.mux.HandleFunc("/api/export/csv", s.handleExportCSV)
	s.mux.HandleFunc("/api/servers", s.handleServers)
	// Dashboard served at root — registered by dashboard package via RegisterStatic
}

// RegisterStatic registers a static file handler at / using the provided http.Handler.
// Called by the dashboard package after setting up embed.FS.
func (s *Server) RegisterStatic(h http.Handler) {
	s.mux.Handle("/", h)
}

// trackViewer records a request from a client IP.
func (s *Server) trackViewer(r *http.Request) {
	ip := clientIP(r)
	s.mu.Lock()
	s.dashboardViewers[ip] = time.Now()
	s.mu.Unlock()
}

// DashboardViewers returns the count of unique IPs that have hit this server in
// the last 60 seconds.
//
// This used to be called GetConcurrentUsers, which made it satisfy the same
// interface as the collector's TCP user tracker despite measuring something
// completely different (people looking at the dashboard, not users of the
// monitored application). Nothing ever wired it to the collector, but the name
// made doing so by accident far too easy.
func (s *Server) DashboardViewers() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	cutoff := time.Now().Add(-60 * time.Second)
	for ip, lastSeen := range s.dashboardViewers {
		if lastSeen.Before(cutoff) {
			delete(s.dashboardViewers, ip)
		}
	}
	return len(s.dashboardViewers)
}

// Handler returns the underlying http.Handler with viewer tracking.
func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.trackViewer(r)
		s.mux.ServeHTTP(w, r)
	})
}

// Start begins listening on the configured port.
func (s *Server) Start() error {
	s.httpServer = &http.Server{
		Addr:              ":" + s.port,
		Handler:           s.Handler(),
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
		MaxHeaderBytes:    maxHeaderBytes,
	}
	log.Printf("[server] listening on http://localhost:%s", s.port)
	err := s.httpServer.ListenAndServe()
	if err == http.ErrServerClosed {
		// Expected during graceful shutdown.
		return nil
	}
	return err
}

// Shutdown stops accepting connections and waits for in-flight requests.
//
// Without this, stopping the service cancelled the collector context and closed
// the database while HTTP handlers were still running, so a CSV export in
// progress died mid-stream and the client received a truncated file.
func (s *Server) Shutdown(ctx context.Context) error {
	if s.httpServer == nil {
		return nil
	}
	log.Println("[server] shutting down HTTP server")
	return s.httpServer.Shutdown(ctx)
}

// ShutdownGrace is the recommended grace period for Shutdown.
func ShutdownGrace() time.Duration { return shutdownGrace }

// ── CORS ──

func parseAllowedOrigins(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var out []string
	for _, o := range strings.Split(raw, ",") {
		if o = strings.TrimSpace(o); o != "" {
			out = append(out, o)
		}
	}
	return out
}

// writeCORS echoes the request origin only when it is explicitly allowed.
//
// These endpoints return tenant-scoped data behind an API key. "*" on an
// authenticated API invites any page in any tab to read it.
func (s *Server) writeCORS(w http.ResponseWriter, r *http.Request) {
	origin := r.Header.Get("Origin")
	if origin == "" || len(s.allowedOrigins) == 0 {
		return
	}
	for _, allowed := range s.allowedOrigins {
		if allowed == "*" || strings.EqualFold(allowed, origin) {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Headers", "X-API-Key, Content-Type")
			return
		}
	}
}

// ── helpers ──

func writeJSONError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// enforceRate applies the per-tenant (or per-IP when unauthenticated) limit.
func (s *Server) enforceRate(w http.ResponseWriter, r *http.Request, tenantID string) bool {
	key := tenantID
	if key == "" {
		key = "ip:" + clientIP(r)
	}
	if s.limiter.allow(key) {
		return true
	}
	retry := s.limiter.retryAfter()
	w.Header().Set("Retry-After", strconv.Itoa(int(retry.Seconds())))
	writeJSONError(w, http.StatusTooManyRequests, "rate limit exceeded")
	return false
}

// ── API handlers ──

type metricsResponse struct {
	Range string            `json:"range"`
	Count int               `json:"count"`
	Data  []metricDataPoint `json:"data"`
}

type metricDataPoint struct {
	Timestamp       int64   `json:"ts"`
	ServerID        string  `json:"server_id"`
	Hostname        string  `json:"hostname"`
	CPUPct          float64 `json:"cpu_pct"`
	MemPct          float64 `json:"mem_pct"`
	DiskFreeGB      float64 `json:"disk_free_gb"`
	NetSentBytes    uint64  `json:"net_sent_bytes"`
	NetRecvBytes    uint64  `json:"net_recv_bytes"`
	CPUCores        int     `json:"cpu_cores"`
	MemTotalGB      float64 `json:"mem_total_gb"`
	DiskTotalGB     float64 `json:"disk_total_gb"`
	DiskReadOps     uint64  `json:"disk_read_ops"`
	DiskWriteOps    uint64  `json:"disk_write_ops"`
	DiskIOPS        float64 `json:"disk_iops"`
	NetMBps         float64 `json:"net_mbps"`
	ConcurrentUsers int     `json:"concurrent_users"`
	NetSentExternal uint64  `json:"net_sent_external"`
	NetRecvExternal uint64  `json:"net_recv_external"`
	NetSentInternal uint64  `json:"net_sent_internal"`
	NetRecvInternal uint64  `json:"net_recv_internal"`
}

func parseRange(r string) time.Duration {
	switch r {
	case "7d":
		return 7 * 24 * time.Hour
	case "30d":
		return 30 * 24 * time.Hour
	default: // "24h" or anything else
		return 24 * time.Hour
	}
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := s.authTenant(w, r)
	if !ok {
		return
	}
	if !s.enforceRate(w, r, tenantID) {
		return
	}

	rangeParam := r.URL.Query().Get("range")
	if rangeParam == "" {
		rangeParam = "24h"
	}
	since := time.Now().Add(-parseRange(rangeParam))

	rows, err := s.db.QueryMetrics(since, tenantID)
	if err != nil {
		log.Printf("[server] QueryMetrics error: %v", err)
		writeJSONError(w, http.StatusInternalServerError, "db error")
		return
	}

	serverFilter := r.URL.Query().Get("server_id")

	// Build response — empty slice (not nil) so JSON returns [] not null
	data := make([]metricDataPoint, 0, len(rows))
	for _, row := range rows {
		if serverFilter != "" && row.ServerID != serverFilter {
			continue
		}
		data = append(data, metricDataPoint{
			Timestamp:       row.Timestamp.Unix(),
			ServerID:        row.ServerID,
			Hostname:        row.Hostname,
			CPUPct:          row.CPUPct,
			MemPct:          row.MemPct,
			DiskFreeGB:      row.DiskFreeGB,
			NetSentBytes:    row.NetSentBytes,
			NetRecvBytes:    row.NetRecvBytes,
			CPUCores:        row.CPUCores,
			MemTotalGB:      row.MemTotalGB,
			DiskTotalGB:     row.DiskTotalGB,
			DiskReadOps:     row.DiskReadOps,
			DiskWriteOps:    row.DiskWriteOps,
			DiskIOPS:        row.DiskIOPS,
			NetMBps:         row.NetMBps,
			ConcurrentUsers: row.ConcurrentUsers,
			NetSentExternal: row.NetSentExternal,
			NetRecvExternal: row.NetRecvExternal,
			NetSentInternal: row.NetSentInternal,
			NetRecvInternal: row.NetRecvInternal,
		})
	}

	resp := metricsResponse{Range: rangeParam, Count: len(data), Data: data}
	s.writeCORS(w, r)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

type processResponse struct {
	Range string             `json:"range"`
	Count int                `json:"count"`
	Data  []processDataPoint `json:"data"`
}

type processDataPoint struct {
	Timestamp int64   `json:"ts"`
	ServerID  string  `json:"server_id"`
	PID       int32   `json:"pid"`
	Name      string  `json:"name"`
	CPUPct    float64 `json:"cpu_pct"`
	MemMB     float64 `json:"mem_mb"`
}

func (s *Server) handleProcesses(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := s.authTenant(w, r)
	if !ok {
		return
	}
	if !s.enforceRate(w, r, tenantID) {
		return
	}

	rangeParam := r.URL.Query().Get("range")
	if rangeParam == "" {
		rangeParam = "24h"
	}
	since := time.Now().Add(-parseRange(rangeParam))

	rows, err := s.db.QueryProcesses(since, tenantID)
	if err != nil {
		log.Printf("[server] QueryProcesses error: %v", err)
		writeJSONError(w, http.StatusInternalServerError, "db error")
		return
	}

	serverFilter := r.URL.Query().Get("server_id")

	data := make([]processDataPoint, 0, len(rows))
	for _, row := range rows {
		if serverFilter != "" && row.ServerID != serverFilter {
			continue
		}
		data = append(data, processDataPoint{
			Timestamp: row.Timestamp.Unix(),
			ServerID:  row.ServerID,
			PID:       row.PID,
			Name:      row.Name,
			CPUPct:    row.CPUPct,
			MemMB:     row.MemMB,
		})
	}

	resp := processResponse{Range: rangeParam, Count: len(data), Data: data}
	s.writeCORS(w, r)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// pinger is implemented by backends that can verify connectivity. Checked with a
// type assertion so the storage.Store interface does not need to grow a method
// that agent mode cannot meaningfully implement.
type pinger interface {
	Ping(ctx context.Context) error
}

// healthSnapshot gathers the facts the health endpoints report on.
func (s *Server) healthSnapshot() (healthy bool, dbErr error, lastMetricAge time.Duration, haveMetric bool) {
	healthy = true

	if p, ok := s.db.(pinger); ok {
		ctx, cancel := context.WithTimeout(context.Background(), healthPingLimit)
		defer cancel()
		if err := p.Ping(ctx); err != nil {
			return false, err, 0, false
		}
	}

	// Freshness matters more than row counts: a hub with 40 million rows and no
	// new data for an hour is broken, and COUNT(*) would happily report "ok".
	rows, err := s.db.QueryMetrics(time.Now().Add(-10*time.Minute), "")
	if err != nil {
		return false, err, 0, false
	}
	if len(rows) > 0 {
		newest := rows[len(rows)-1].Timestamp
		lastMetricAge = time.Since(newest)
		haveMetric = true
	}
	return healthy, nil, lastMetricAge, haveMetric
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	healthy, dbErr, lastAge, haveMetric := s.healthSnapshot()

	body := map[string]interface{}{
		"status":         "ok",
		"version":        BuildVersion,
		"uptime_seconds": int64(time.Since(s.startedAt).Seconds()),
		"hub_mode":       s.hubMode,
		"timestamp":      time.Now().Unix(),
	}
	if haveMetric {
		body["last_metric_age_seconds"] = int64(lastAge.Seconds())
	} else {
		body["last_metric_age_seconds"] = nil
	}

	w.Header().Set("Content-Type", "application/json")
	if !healthy {
		// The old handler discarded database errors entirely and always answered
		// 200 "ok", so an unreachable database looked perfectly healthy to every
		// uptime monitor pointed at it.
		body["status"] = "degraded"
		if dbErr != nil {
			body["error"] = "database unreachable"
			log.Printf("[server] health check failed: %v", dbErr)
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}
	json.NewEncoder(w).Encode(body)
}

// handleReady is the orchestrator-facing check: no body, just a status code.
func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	healthy, _, _, _ := s.healthSnapshot()
	if !healthy {
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte("not ready\n"))
		return
	}
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("ready\n"))
}

// handlePrometheus exposes basic internals in Prometheus text format so the
// monitor can itself be monitored.
func (s *Server) handlePrometheus(w http.ResponseWriter, r *http.Request) {
	healthy, _, lastAge, haveMetric := s.healthSnapshot()

	up := 0
	if healthy {
		up = 1
	}

	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	fmt.Fprintf(w, "# HELP wmonitor_up Whether the storage backend is reachable.\n")
	fmt.Fprintf(w, "# TYPE wmonitor_up gauge\nwmonitor_up %d\n", up)
	fmt.Fprintf(w, "# HELP wmonitor_uptime_seconds Process uptime.\n")
	fmt.Fprintf(w, "# TYPE wmonitor_uptime_seconds gauge\nwmonitor_uptime_seconds %d\n", int64(time.Since(s.startedAt).Seconds()))
	if haveMetric {
		fmt.Fprintf(w, "# HELP wmonitor_last_metric_age_seconds Age of the most recent stored sample.\n")
		fmt.Fprintf(w, "# TYPE wmonitor_last_metric_age_seconds gauge\nwmonitor_last_metric_age_seconds %d\n", int64(lastAge.Seconds()))
	}
	fmt.Fprintf(w, "# HELP wmonitor_dashboard_viewers Unique IPs seen in the last 60s.\n")
	fmt.Fprintf(w, "# TYPE wmonitor_dashboard_viewers gauge\nwmonitor_dashboard_viewers %d\n", s.DashboardViewers())
}

func (s *Server) handleExportCSV(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := s.authTenant(w, r)
	if !ok {
		return
	}
	if !s.enforceRate(w, r, tenantID) {
		return
	}

	rangeParam := r.URL.Query().Get("range")
	if rangeParam == "" {
		rangeParam = "24h"
	}
	since := time.Now().Add(-parseRange(rangeParam))

	w.Header().Set("Content-Type", "text/csv")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="wmonitor_export_%s.csv"`, time.Now().Format("20060102_150405")))

	if _, err := export.WriteCSV(w, s.db, since, tenantID); err != nil {
		log.Printf("[server] CSV export error: %v", err)
	}
}

// handleServers returns distinct server_id values seen in the DB (Phase 7).
func (s *Server) handleServers(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := s.authTenant(w, r)
	if !ok {
		return
	}
	if !s.enforceRate(w, r, tenantID) {
		return
	}

	servers, err := s.db.QueryServers(tenantID)
	if err != nil {
		log.Printf("[server] QueryServers error: %v", err)
		writeJSONError(w, http.StatusInternalServerError, "db error")
		return
	}
	if servers == nil {
		servers = []string{}
	}
	s.writeCORS(w, r)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"servers": servers})
}

// handleIngest accepts JSON metric/process payloads from agents (hub mode).
//
// The presented API key is verified against the registry and mapped to a tenant.
// Previously any non-empty key was accepted and used verbatim as the tenant ID,
// which meant unauthenticated writes into an attacker-chosen namespace.
func (s *Server) handleIngest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	tenantID, ok := s.authTenant(w, r)
	if !ok {
		return
	}
	if !s.enforceRate(w, r, tenantID) {
		return
	}

	// Cap the body. json.NewDecoder on an unbounded r.Body let a single request
	// stream until the process ran out of memory.
	r.Body = http.MaxBytesReader(w, r.Body, maxIngestBody)

	switch r.URL.Query().Get("type") {
	case "metric":
		var m storage.MetricRow
		if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
			writeJSONError(w, http.StatusBadRequest, "bad request")
			return
		}
		m.TenantID = tenantID // server-assigned; never trust the client's value
		if err := s.db.InsertMetric(m); err != nil {
			log.Printf("[server] ingest metric error: %v", err)
			writeJSONError(w, http.StatusInternalServerError, "db error")
			return
		}
	case "process":
		var p storage.ProcessRow
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			writeJSONError(w, http.StatusBadRequest, "bad request")
			return
		}
		p.TenantID = tenantID
		if err := s.db.InsertProcess(p); err != nil {
			log.Printf("[server] ingest process error: %v", err)
			writeJSONError(w, http.StatusInternalServerError, "db error")
			return
		}
	default:
		writeJSONError(w, http.StatusBadRequest, "type must be 'metric' or 'process'")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	w.Write([]byte(`{"status":"accepted"}`))
}
