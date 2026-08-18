// Package server provides the HTTP API and embedded dashboard for sysmon.
package server

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"sync"
	"time"

	"Zeus/export"
	"Zeus/storage"
)

// Server wraps the HTTP mux and storage store.
type Server struct {
	db          storage.Store
	port        string
	mux         *http.ServeMux
	mu          sync.Mutex
	activeUsers map[string]time.Time
	// Hub mode — when true, /api/ingest is enabled and API key acts as tenant ID
	hubMode bool
}

// New creates a Server bound to the given port (e.g. "8080").
func New(db storage.Store, port string) *Server {
	s := &Server{
		db:          db,
		port:        port,
		mux:         http.NewServeMux(),
		activeUsers: make(map[string]time.Time),
	}
	s.routes()
	return s
}

// EnableHubMode enables the /api/ingest endpoint.
// In hub mode the X-API-Key header serves as both authentication and tenant isolation.
// Any non-empty key is accepted; each key's data is stored and queried separately.
func (s *Server) EnableHubMode(_ string) {
	s.hubMode = true
	s.mux.HandleFunc("/api/ingest", s.handleIngest)
	log.Println("[server] hub mode enabled — POST /api/ingest accepting agent data (key = tenant ID)")
}

// routes registers all HTTP handlers.
func (s *Server) routes() {
	s.mux.HandleFunc("/api/metrics", s.handleMetrics)
	s.mux.HandleFunc("/api/processes", s.handleProcesses)
	s.mux.HandleFunc("/api/health", s.handleHealth)
	s.mux.HandleFunc("/api/export/csv", s.handleExportCSV)
	s.mux.HandleFunc("/api/servers", s.handleServers)
	// Dashboard served at root — registered by dashboard package via RegisterDashboard
}

// RegisterStatic registers a static file handler at / using the provided http.Handler.
// Called by the dashboard package after setting up embed.FS.
func (s *Server) RegisterStatic(h http.Handler) {
	s.mux.Handle("/", h)
}

// trackRequest records an active request from a client IP.
func (s *Server) trackRequest(r *http.Request) {
	ip := r.RemoteAddr
	if host, _, err := net.SplitHostPort(ip); err == nil {
		ip = host
	}
	s.mu.Lock()
	s.activeUsers[ip] = time.Now()
	s.mu.Unlock()
}

// GetConcurrentUsers returns the count of unique active users/IPs in the last 60 seconds.
func (s *Server) GetConcurrentUsers() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	cutoff := now.Add(-60 * time.Second)
	for ip, lastSeen := range s.activeUsers {
		if lastSeen.Before(cutoff) {
			delete(s.activeUsers, ip)
		}
	}
	return len(s.activeUsers)
}

// Handler returns the underlying http.Handler with active request tracking (for testing and server start).
func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.trackRequest(r)
		s.mux.ServeHTTP(w, r)
	})
}

// Start begins listening on the configured port.
func (s *Server) Start() error {
	addr := ":" + s.port
	log.Printf("[server] listening on http://localhost%s", addr)
	return http.ListenAndServe(addr, s.Handler())
}

// --- API handlers ---

type metricsResponse struct {
	Range string            `json:"range"`
	Count int               `json:"count"`
	Data  []metricDataPoint `json:"data"`
}

type metricDataPoint struct {
	Timestamp        int64   `json:"ts"`
	ServerID         string  `json:"server_id"`
	Hostname         string  `json:"hostname"`
	CPUPct           float64 `json:"cpu_pct"`
	MemPct           float64 `json:"mem_pct"`
	DiskFreeGB       float64 `json:"disk_free_gb"`
	NetSentBytes     uint64  `json:"net_sent_bytes"`
	NetRecvBytes     uint64  `json:"net_recv_bytes"`
	CPUCores         int     `json:"cpu_cores"`
	MemTotalGB       float64 `json:"mem_total_gb"`
	DiskTotalGB      float64 `json:"disk_total_gb"`
	DiskReadOps      uint64  `json:"disk_read_ops"`
	DiskWriteOps     uint64  `json:"disk_write_ops"`
	DiskIOPS         float64 `json:"disk_iops"`
	NetMBps          float64 `json:"net_mbps"`
	ConcurrentUsers  int     `json:"concurrent_users"`
	NetSentExternal  uint64  `json:"net_sent_external"`
	NetRecvExternal  uint64  `json:"net_recv_external"`
	NetSentInternal  uint64  `json:"net_sent_internal"`
	NetRecvInternal  uint64  `json:"net_recv_internal"`
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
	rangeParam := r.URL.Query().Get("range")
	if rangeParam == "" {
		rangeParam = "24h"
	}
	since := time.Now().Add(-parseRange(rangeParam))

	// In hub mode, filter by the tenant's API key so clients only see their own data.
	tenantID := ""
	if s.hubMode {
		tenantID = r.Header.Get("X-API-Key")
		if tenantID == "" {
			tenantID = r.URL.Query().Get("api_key")
		}
		if tenantID == "" {
			http.Error(w, `{"error":"X-API-Key header or api_key query param required"}`, http.StatusUnauthorized)
			return
		}
	}

	rows, err := s.db.QueryMetrics(since, tenantID)
	if err != nil {
		http.Error(w, `{"error":"db error"}`, http.StatusInternalServerError)
		log.Printf("[server] QueryMetrics error: %v", err)
		return
	}

	// Optionally filter by server_id
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
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
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
	rangeParam := r.URL.Query().Get("range")
	if rangeParam == "" {
		rangeParam = "24h"
	}
	since := time.Now().Add(-parseRange(rangeParam))

	// In hub mode, filter by tenant
	tenantID := ""
	if s.hubMode {
		tenantID = r.Header.Get("X-API-Key")
		if tenantID == "" {
			tenantID = r.URL.Query().Get("api_key")
		}
		if tenantID == "" {
			http.Error(w, `{"error":"X-API-Key header or api_key query param required"}`, http.StatusUnauthorized)
			return
		}
	}

	rows, err := s.db.QueryProcesses(since, tenantID)
	if err != nil {
		http.Error(w, `{"error":"db error"}`, http.StatusInternalServerError)
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
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	json.NewEncoder(w).Encode(resp)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	mc, _ := s.db.CountMetrics()
	pc, _ := s.db.CountProcesses()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"status":        "ok",
		"metric_rows":   mc,
		"process_rows":  pc,
		"timestamp":     time.Now().Unix(),
	})
}

func (s *Server) handleExportCSV(w http.ResponseWriter, r *http.Request) {
	rangeParam := r.URL.Query().Get("range")
	if rangeParam == "" {
		rangeParam = "24h"
	}
	since := time.Now().Add(-parseRange(rangeParam))

	// In hub mode, scope export to the requesting tenant's data
	tenantID := ""
	if s.hubMode {
		tenantID = r.Header.Get("X-API-Key")
		if tenantID == "" {
			tenantID = r.URL.Query().Get("api_key")
		}
		if tenantID == "" {
			http.Error(w, `{"error":"X-API-Key header or api_key query param required"}`, http.StatusUnauthorized)
			return
		}
	}

	w.Header().Set("Content-Type", "text/csv")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="wmonitor_export_%s.csv"`, time.Now().Format("20060102_150405")))

	_, err := export.WriteCSV(w, s.db, since, tenantID)
	if err != nil {
		log.Printf("[server] CSV export error: %v", err)
	}
}

// handleServers returns distinct server_id values seen in the DB (Phase 7).
func (s *Server) handleServers(w http.ResponseWriter, r *http.Request) {
	// In hub mode, filter by tenant so clients only see their own servers.
	tenantID := ""
	if s.hubMode {
		tenantID = r.Header.Get("X-API-Key")
		if tenantID == "" {
			tenantID = r.URL.Query().Get("api_key")
		}
		if tenantID == "" {
			http.Error(w, `{"error":"X-API-Key header or api_key query param required"}`, http.StatusUnauthorized)
			return
		}
	}
	servers, err := s.db.QueryServers(tenantID)
	if err != nil {
		http.Error(w, `{"error":"db error"}`, http.StatusInternalServerError)
		log.Printf("[server] QueryServers error: %v", err)
		return
	}
	if servers == nil {
		servers = []string{}
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	json.NewEncoder(w).Encode(map[string]interface{}{"servers": servers})
}

// handleIngest accepts JSON metric/process payloads from agents (hub mode).
// The X-API-Key header serves as both authentication and tenant identifier.
// Any non-empty key is accepted — each unique key is its own isolated tenant.
func (s *Server) handleIngest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	// API key = tenant identifier. Reject empty keys.
	tenantID := r.Header.Get("X-API-Key")
	if tenantID == "" {
		http.Error(w, `{"error":"X-API-Key header required"}`, http.StatusUnauthorized)
		log.Printf("[server] ingest: missing API key from %s", r.RemoteAddr)
		return
	}

	// Parse payload type
	payloadType := r.URL.Query().Get("type") // "metric" or "process"

	switch payloadType {
	case "metric":
		var m storage.MetricRow
		if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
			http.Error(w, `{"error":"bad request"}`, http.StatusBadRequest)
			return
		}
		m.TenantID = tenantID // tag with tenant before storing
		if err := s.db.InsertMetric(m); err != nil {
			http.Error(w, `{"error":"db error"}`, http.StatusInternalServerError)
			log.Printf("[server] ingest metric error: %v", err)
			return
		}
	case "process":
		var p storage.ProcessRow
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			http.Error(w, `{"error":"bad request"}`, http.StatusBadRequest)
			return
		}
		p.TenantID = tenantID // tag with tenant before storing
		if err := s.db.InsertProcess(p); err != nil {
			http.Error(w, `{"error":"db error"}`, http.StatusInternalServerError)
			log.Printf("[server] ingest process error: %v", err)
			return
		}
	default:
		http.Error(w, `{"error":"type must be 'metric' or 'process'"}`, http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	w.Write([]byte(`{"status":"accepted"}`))
}
