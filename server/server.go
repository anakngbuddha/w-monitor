// Package server provides the HTTP API and embedded dashboard.
package server

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"Zeus/export"
	"Zeus/storage"
)

var BuildVersion = "dev"

const (
	readHeaderTimeout = 5 * time.Second
	readTimeout = 30 * time.Second
	writeTimeout = 120 * time.Second
	idleTimeout = 120 * time.Second
	maxHeaderBytes = 1 << 20
	shutdownGrace = 10 * time.Second
	maxIngestBody = storage.MaxBatchBytes
	healthPingLimit = 2 * time.Second
)

type ingestBackend interface {
	InitializeIngest(context.Context) error
	AcceptIngest(context.Context, string, string, storage.IngestBatch, storage.IngestPolicy) ([]storage.IngestOutcome, error)
	CleanupIngest(context.Context) error
}

type Server struct {
	db storage.Store
	port string
	mux *http.ServeMux
	httpServer *http.Server
	startedAt time.Time
	mu sync.Mutex
	dashboardViewers map[string]time.Time
	hubMode bool
	keys KeyStore
	authCache *authCache
	limiter *rateLimiter
	authEpoch int64
	epochRefreshed time.Time
	epochInterval time.Duration
	epochMu sync.Mutex
	sessions *sessionStore
	loginLimiter *rateLimiter
	alerts AlertSource
	allowedOrigins []string
	healthCheckedAt time.Time
	healthHealthy bool
	healthDBErr error
	healthLastAge time.Duration
	healthHaveMetric bool
	retentionStatus func() map[string]interface{}
	ingestion ingestBackend
	ingestionErr error
	ingestPolicy storage.IngestPolicy
	stopping bool
}

func New(db storage.Store, port string) *Server {
	s := &Server{db: db, port: port, mux: http.NewServeMux(), startedAt: time.Now(), dashboardViewers: make(map[string]time.Time), authCache: newAuthCache(), limiter: newRateLimiter(defaultRatePerSecond, defaultBurst), sessions: newSessionStore(), loginLimiter: newRateLimiter(1, 5), allowedOrigins: parseAllowedOrigins(os.Getenv("WMONITOR_ALLOWED_ORIGINS")), epochInterval: AuthRevocationMaxDelay, ingestPolicy: storage.DefaultIngestPolicy()}
	for _, setting := range []struct{ name string; target *int64 }{
		{"WMONITOR_DAILY_ROW_QUOTA", &s.ingestPolicy.DailyRows},
		{"WMONITOR_DAILY_BYTE_QUOTA", &s.ingestPolicy.DailyBytes},
		{"WMONITOR_AGENT_DAILY_ROW_QUOTA", &s.ingestPolicy.AgentDailyRows},
		{"WMONITOR_AGENT_DAILY_BYTE_QUOTA", &s.ingestPolicy.AgentDailyBytes},
	} {
		if raw := os.Getenv(setting.name); raw != "" {
			value, err := strconv.ParseInt(raw, 10, 64)
			if err != nil || value < 1 {
				s.ingestionErr = errors.New("invalid ingest budget configuration")
			} else {
				*setting.target = value
			}
		}
	}
	if err := s.ingestPolicy.Validate(); err != nil { s.ingestionErr = err }
	s.routes()
	return s
}

func (s *Server) EnableHubMode(keys KeyStore) {
	s.hubMode, s.keys = true, keys
	s.mux.HandleFunc("/api/ingest", s.handleIngest)
	s.mux.HandleFunc("/api/v1/ingest/batches", s.handleBatch)
	s.mux.HandleFunc("/api/enroll", s.handleEnroll)
	s.mux.HandleFunc("/api/admin/enroll-codes", s.handleAdminEnrollCodes)
	s.mux.HandleFunc("/api/admin/agents", s.handleAdminAgents)
	s.mux.HandleFunc("/api/admin/clients", s.handleAdminClients)
	s.mux.HandleFunc("/api/admin/agents/rotate", s.handleAdminAgentRotate)
	s.syncAuthEpoch()
	if backend, ok := s.db.(ingestBackend); ok && s.ingestionErr == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := backend.InitializeIngest(ctx); err != nil { s.ingestionErr = errors.New("ingest initialization failed") } else { s.ingestion = backend }
	} else if s.ingestionErr == nil { s.ingestionErr = errors.New("storage lacks atomic ingestion") }
	if keys == nil { s.ingestionErr = errors.New("authentication unavailable") }
}

func (s *Server) SetRetentionStatusProvider(fn func() map[string]interface{}) { s.retentionStatus = fn }
func (s *Server) SetAllowedOrigins(origins []string) { s.allowedOrigins = origins }

func (s *Server) routes() {
	s.mux.HandleFunc("/api/metrics", s.handleMetrics)
	s.mux.HandleFunc("/api/processes", s.handleProcesses)
	s.mux.HandleFunc("/api/health", s.handleHealth)
	s.mux.HandleFunc("/api/ready", s.handleReady)
	s.mux.HandleFunc("/metrics", s.handlePrometheus)
	s.mux.HandleFunc("/api/export/csv", s.handleExportCSV)
	s.mux.HandleFunc("/api/servers", s.handleServers)
	s.mux.HandleFunc("/api/session", s.handleSession)
}

func (s *Server) RegisterStatic(h http.Handler) { s.mux.Handle("/", h) }

func (s *Server) trackViewer(r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for ip, seen := range s.dashboardViewers { if now.Sub(seen) > time.Minute { delete(s.dashboardViewers, ip) } }
	if len(s.dashboardViewers) < 4096 { s.dashboardViewers[clientIP(r)] = now }
}

func (s *Server) DashboardViewers() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	for ip, seen := range s.dashboardViewers { if time.Since(seen) > time.Minute { delete(s.dashboardViewers, ip) } }
	return len(s.dashboardViewers)
}

func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; font-src 'self'; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'; connect-src 'self'")
		// Constant-time liveness neither scans customer data nor takes a lock
		// held by the dependency readiness check.
		if r.URL.Path == "/api/health" { s.handleHealth(w, r); return }
		if s.limiter != nil && !s.limiter.allow("preauth:"+clientIP(r)) {
			w.Header().Set("Retry-After", "1")
			writeJSONError(w, http.StatusTooManyRequests, "request rate exceeded")
			return
		}
		if r.URL.Path == "/" || r.URL.Path == "/index.html" { s.trackViewer(r) }
		s.mux.ServeHTTP(w, r)
	})
}

func (s *Server) Start() error {
	if s.hubMode && s.ingestionErr != nil { return s.ingestionErr }
	host := "127.0.0.1"
	if s.hubMode { host = "0.0.0.0" }
	if configured := os.Getenv("WMONITOR_LISTEN_HOST"); configured != "" {
		ip := net.ParseIP(configured)
		if ip == nil || (!s.hubMode && !ip.IsLoopback()) { return errors.New("invalid or unauthenticated non-loopback listen host") }
		host = configured
	}
	port, err := strconv.Atoi(s.port)
	if err != nil || port < 1 || port > 65535 { return errors.New("invalid HTTP port") }
	s.mu.Lock()
	if s.stopping { s.mu.Unlock(); return nil }
	h := &http.Server{Addr: net.JoinHostPort(host, s.port), Handler: s.Handler(), ReadHeaderTimeout: readHeaderTimeout, ReadTimeout: readTimeout, WriteTimeout: writeTimeout, IdleTimeout: idleTimeout, MaxHeaderBytes: maxHeaderBytes}
	s.httpServer = h
	s.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	var workers sync.WaitGroup
	if s.ingestion != nil {
		workers.Add(1)
		go func() {
			defer workers.Done()
			ticker := time.NewTicker(time.Hour)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done(): return
				case <-ticker.C:
					cleanup, stop := context.WithTimeout(ctx, 5*time.Second)
					if err := s.ingestion.CleanupIngest(cleanup); err != nil { log.Print("[server] ingest ledger cleanup incomplete") }
					stop()
				}
			}
		}()
	}
	err = h.ListenAndServe()
	cancel()
	workers.Wait()
	if errors.Is(err, http.ErrServerClosed) { return nil }
	return err
}

func (s *Server) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	s.stopping = true
	h := s.httpServer
	s.mu.Unlock()
	if h == nil { return nil }
	return h.Shutdown(ctx)
}

func ShutdownGrace() time.Duration { return shutdownGrace }

func parseAllowedOrigins(raw string) []string {
	var out []string
	for _, origin := range strings.Split(raw, ",") { if origin = strings.TrimSpace(origin); origin != "" && origin != "*" { out = append(out, origin) } }
	return out
}

func (s *Server) writeCORS(w http.ResponseWriter, r *http.Request) {
	origin := r.Header.Get("Origin")
	for _, allowed := range s.allowedOrigins {
		if origin != "" && allowed != "*" && strings.EqualFold(allowed, origin) {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Add("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Headers", "X-API-Key, Content-Type")
			return
		}
	}
}

func writeJSONError(w http.ResponseWriter, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": message})
}

func (s *Server) enforceRate(w http.ResponseWriter, r *http.Request, tenant string) bool {
	if tenant == "" { tenant = "ip:"+clientIP(r) }
	if s.limiter.allow("tenant:"+tenant) { return true }
	w.Header().Set("Retry-After", "1")
	writeJSONError(w, http.StatusTooManyRequests, "rate limit exceeded")
	return false
}

func readOnly(w http.ResponseWriter, r *http.Request) bool {
	if r.Method == http.MethodGet || r.Method == http.MethodHead { return true }
	w.Header().Set("Allow", "GET, HEAD")
	writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
	return false
}

type metricsResponse struct { Range string `json:"range"`; Count int `json:"count"`; Data []metricDataPoint `json:"data"` }
type metricDataPoint struct {
	Timestamp int64 `json:"ts"`
	ServerID string `json:"server_id"`
	Hostname string `json:"hostname"`
	CPUPct float64 `json:"cpu_pct"`
	MemPct float64 `json:"mem_pct"`
	DiskFreeGB float64 `json:"disk_free_gb"`
	NetSentBytes uint64 `json:"net_sent_bytes"`
	NetRecvBytes uint64 `json:"net_recv_bytes"`
	CPUCores int `json:"cpu_cores"`
	MemTotalGB float64 `json:"mem_total_gb"`
	DiskTotalGB float64 `json:"disk_total_gb"`
	DiskReadOps uint64 `json:"disk_read_ops"`
	DiskWriteOps uint64 `json:"disk_write_ops"`
	DiskIOPS float64 `json:"disk_iops"`
	NetMBps float64 `json:"net_mbps"`
	ConcurrentUsers int `json:"concurrent_users"`
	NetSentExternal uint64 `json:"net_sent_external"`
	NetRecvExternal uint64 `json:"net_recv_external"`
	NetSentInternal uint64 `json:"net_sent_internal"`
	NetRecvInternal uint64 `json:"net_recv_internal"`
}

func parseRange(value string) time.Duration {
	switch value { case "7d": return 7*24*time.Hour; case "30d": return 30*24*time.Hour; default: return 24*time.Hour }
}

func queryMetricsScoped(ctx context.Context, db storage.Store, since time.Time, tenant, server string) ([]storage.MetricRow, error) {
	if err := storage.RequireTenant(tenant); err != nil { return nil, err }
	if q, ok := db.(interface{ QueryMetricsQ(storage.MetricQuery) ([]storage.MetricRow, error) }); ok {
		return q.QueryMetricsQ(storage.MetricQuery{Ctx: ctx, Since: since, Until: time.Now(), TenantID: tenant, ServerID: server, Limit: storage.DefaultQueryLimit})
	}
	return nil, errors.New("storage does not support bounded contextual metric reads")
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	if !readOnly(w, r) { return }
	tenant, ok := s.authTenant(w, r)
	if !ok || !s.enforceRate(w, r, tenant) { return }
	rangeParam := r.URL.Query().Get("range")
	if rangeParam == "" { rangeParam = "24h" }
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	rows, err := queryMetricsScoped(ctx, s.db, time.Now().Add(-parseRange(rangeParam)), tenant, r.URL.Query().Get("server_id"))
	if err != nil { writeJSONError(w, 503, "metric query unavailable"); return }
	data := make([]metricDataPoint, 0, len(rows))
	for _, row := range rows {
		data = append(data, metricDataPoint{Timestamp: row.Timestamp.Unix(), ServerID: row.ServerID, Hostname: row.Hostname, CPUPct: row.CPUPct, MemPct: row.MemPct, DiskFreeGB: row.DiskFreeGB, NetSentBytes: row.NetSentBytes, NetRecvBytes: row.NetRecvBytes, CPUCores: row.CPUCores, MemTotalGB: row.MemTotalGB, DiskTotalGB: row.DiskTotalGB, DiskReadOps: row.DiskReadOps, DiskWriteOps: row.DiskWriteOps, DiskIOPS: row.DiskIOPS, NetMBps: row.NetMBps, ConcurrentUsers: row.ConcurrentUsers, NetSentExternal: row.NetSentExternal, NetRecvExternal: row.NetRecvExternal, NetSentInternal: row.NetSentInternal, NetRecvInternal: row.NetRecvInternal})
	}
	s.writeCORS(w, r)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Result-Limit", strconv.Itoa(storage.DefaultQueryLimit))
	json.NewEncoder(w).Encode(metricsResponse{Range: rangeParam, Count: len(data), Data: data})
}

type processResponse struct { Range string `json:"range"`; Count int `json:"count"`; Data []processDataPoint `json:"data"` }
type processDataPoint struct { Timestamp int64 `json:"ts"`; ServerID string `json:"server_id"`; PID int32 `json:"pid"`; Name string `json:"name"`; CPUPct float64 `json:"cpu_pct"`; MemMB float64 `json:"mem_mb"` }

func (s *Server) handleProcesses(w http.ResponseWriter, r *http.Request) {
	if !readOnly(w, r) { return }
	tenant, ok := s.authTenant(w, r)
	if !ok || !s.enforceRate(w, r, tenant) { return }
	rangeParam := r.URL.Query().Get("range")
	if rangeParam == "" { rangeParam = "24h" }
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	q, ok := s.db.(interface{ QueryProcessesQ(storage.MetricQuery) ([]storage.ProcessRow, error) })
	if !ok { writeJSONError(w, 503, "bounded process query unavailable"); return }
	rows, err := q.QueryProcessesQ(storage.MetricQuery{Ctx: ctx, Since: time.Now().Add(-parseRange(rangeParam)), Until: time.Now(), TenantID: tenant, ServerID: r.URL.Query().Get("server_id"), Limit: storage.DefaultQueryLimit})
	if err != nil { writeJSONError(w, 503, "process query unavailable"); return }
	data := make([]processDataPoint, 0, len(rows))
	for _, row := range rows { data = append(data, processDataPoint{Timestamp: row.Timestamp.Unix(), ServerID: row.ServerID, PID: row.PID, Name: row.Name, CPUPct: row.CPUPct, MemMB: row.MemMB}) }
	s.writeCORS(w, r)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(processResponse{Range: rangeParam, Count: len(data), Data: data})
}

type pinger interface { Ping(context.Context) error }

// Readiness is dependency-only. Never infer expected-agent health from an
// arbitrary global metric, nor expose another tenant's alert or freshness data.
func (s *Server) healthSnapshot() (bool, error, time.Duration, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.healthCheckedAt.IsZero() && time.Since(s.healthCheckedAt) < 10*time.Second { return s.healthHealthy, s.healthDBErr, 0, false }
	var err error
	if s.hubMode && s.ingestionErr != nil { err = s.ingestionErr } else if p, ok := s.db.(pinger); ok {
		ctx, cancel := context.WithTimeout(context.Background(), healthPingLimit)
		err = p.Ping(ctx)
		cancel()
	} else { err = errors.New("storage readiness check unavailable") }
	s.healthCheckedAt, s.healthHealthy, s.healthDBErr = time.Now(), err == nil, err
	return err == nil, err, 0, false
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if !readOnly(w, r) { return }
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"status": "ok", "version": BuildVersion, "hub_mode": s.hubMode, "uptime_seconds": int64(time.Since(s.startedAt).Seconds()), "timestamp": time.Now().Unix(), "retention": map[string]interface{}{"downsampling_enabled": false, "purge_enabled": false, "reason": "V07 contained; destructive retention remains disabled until P2.03"}})
}

func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	if !readOnly(w, r) { return }
	if healthy, _, _, _ := s.healthSnapshot(); !healthy { http.Error(w, "not ready", 503); return }
	io.WriteString(w, "ready\n")
}

func (s *Server) handlePrometheus(w http.ResponseWriter, r *http.Request) {
	if !readOnly(w, r) { return }
	// A loopback reverse proxy is not an administrative identity.
	if s.hubMode { if _, ok := s.authTenantScope(w, r, storage.ScopeAdmin); !ok { return } }
	healthy, _, _, _ := s.healthSnapshot()
	up := 0
	if healthy { up = 1 }
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	fmt.Fprintf(w, "# TYPE wmonitor_up gauge\nwmonitor_up %d\n# TYPE wmonitor_uptime_seconds gauge\nwmonitor_uptime_seconds %d\n", up, int64(time.Since(s.startedAt).Seconds()))
}

func (s *Server) handleExportCSV(w http.ResponseWriter, r *http.Request) {
	if !readOnly(w, r) { return }
	tenant, ok := s.authTenant(w, r)
	if !ok || !s.enforceRate(w, r, tenant) { return }
	if err := storage.RequireTenant(tenant); err != nil { writeJSONError(w, 400, "tenant required"); return }
	// Existing CSV formatting remains spreadsheet-safe. The storage adapter
	// supplies cancellation and server filtering to its bounded metric read.
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	view := exportView{Store: s.db, ctx: ctx, server: r.URL.Query().Get("server_id")}
	w.Header().Set("Content-Type", "text/csv")
	w.Header().Set("Content-Disposition", `attachment; filename="wmonitor_export.csv"`)
	if _, err := export.WriteCSV(w, view, time.Now().Add(-parseRange(r.URL.Query().Get("range"))), tenant); err != nil { log.Print("[server] CSV export incomplete") }
}

type exportView struct { storage.Store; ctx context.Context; server string }
func (v exportView) QueryMetrics(since time.Time, tenant string) ([]storage.MetricRow, error) { return queryMetricsScoped(v.ctx, v.Store, since, tenant, v.server) }

func (s *Server) handleServers(w http.ResponseWriter, r *http.Request) {
	if !readOnly(w, r) { return }
	tenant, ok := s.authTenant(w, r)
	if !ok || !s.enforceRate(w, r, tenant) { return }
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	q, ok := s.db.(interface{ QueryServersContext(context.Context, string, int) ([]string, error) })
	if !ok { writeJSONError(w, 503, "bounded server query unavailable"); return }
	servers, err := q.QueryServersContext(ctx, tenant, 1000)
	if err != nil { writeJSONError(w, 503, "server query unavailable"); return }
	if servers == nil { servers = []string{} }
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"servers": servers, "limit": 1000})
}

func (s *Server) handleBatch(w http.ResponseWriter, r *http.Request) { s.receiveBatch(w, r, false) }
func (s *Server) handleIngest(w http.ResponseWriter, r *http.Request) { s.receiveBatch(w, r, true) }

func (s *Server) receiveBatch(w http.ResponseWriter, r *http.Request, legacy bool) {
	if r.Method != http.MethodPost { writeJSONError(w, 405, "method not allowed"); return }
	principal, ok := s.authPrincipal(w, r, storage.ScopeIngest)
	if !ok { return }
	if principal.Kind != storage.KindAgent || principal.AgentID == "" { writeJSONError(w, 403, "a bound machine credential is required"); return }
	if !s.limiter.allow("agent:"+principal.TenantID+":"+principal.AgentID) { w.Header().Set("Retry-After", "1"); writeJSONError(w, 429, "agent request rate exceeded"); return }
	if s.ingestion == nil || s.ingestionErr != nil { writeJSONError(w, 503, "atomic ingestion unavailable"); return }
	if s.retentionStatus != nil { if pressure, _ := s.retentionStatus()["disk_pressure"].(bool); pressure { w.Header().Set("Retry-After", "60"); writeJSONError(w, 503, "storage disk budget exhausted"); return } }
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxIngestBody))
	if err != nil { writeJSONError(w, 413, "body exceeds limit"); return }
	var batch storage.IngestBatch
	if legacy {
		batch.SchemaVersion = storage.IngestSchema
		event := storage.IngestEvent{BootID: "legacy"}
		var identity string
		switch r.URL.Query().Get("type") {
		case "metric":
			event.Metric = &storage.MetricRow{}
			err = storage.DecodeIngest(body, event.Metric)
			identity = "metric:"+event.Metric.Timestamp.UTC().Format(time.RFC3339Nano)
		case "process":
			event.Process = &storage.ProcessRow{}
			err = storage.DecodeIngest(body, event.Process)
			identity = fmt.Sprintf("process:%s:%d", event.Process.Timestamp.UTC().Format(time.RFC3339Nano), event.Process.PID)
		default: err = errors.New("unknown payload type")
		}
		if err != nil { writeJSONError(w, 400, "invalid legacy event"); return }
		hash := sha256.Sum256([]byte(identity))
		event.EventID = "legacy-"+hex.EncodeToString(hash[:])
		event.Sequence = (binary.BigEndian.Uint64(hash[:8]) & ((1<<63)-1)) | 1
		batch.Events = []storage.IngestEvent{event}
	} else if err := storage.DecodeIngest(body, &batch); err != nil { writeJSONError(w, 400, "invalid batch JSON"); return }
	if err := storage.NormalizeIngest(&batch, principal.TenantID, principal.AgentID, time.Now()); err != nil { writeJSONError(w, 400, "invalid event identity, timestamp or value"); return }
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	outcomes, err := s.ingestion.AcceptIngest(ctx, principal.TenantID, principal.AgentID, batch, s.ingestPolicy)
	if errors.Is(err, storage.ErrEventConflict) { writeJSONError(w, 409, "event content conflicts with its accepted identity"); return }
	if errors.Is(err, storage.ErrAcceptedBudget) {
		now := time.Now().UTC()
		next := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, time.UTC)
		w.Header().Set("Retry-After", strconv.Itoa(int(next.Sub(now).Seconds())+1))
		writeJSONError(w, 429, "accepted-data budget exhausted")
		return
	}
	if err != nil { writeJSONError(w, 503, "ingest transaction did not complete"); return }
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]interface{}{"status": "accepted", "outcomes": outcomes})
}
