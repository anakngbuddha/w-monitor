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

// Server wraps the HTTP mux and storage DB.
type Server struct {
	db          *storage.DB
	port        string
	mux         *http.ServeMux
	mu          sync.Mutex
	activeUsers map[string]time.Time
}

// New creates a Server bound to the given port (e.g. "8080").
func New(db *storage.DB, port string) *Server {
	s := &Server{
		db:          db,
		port:        port,
		mux:         http.NewServeMux(),
		activeUsers: make(map[string]time.Time),
	}
	s.routes()
	return s
}

// routes registers all HTTP handlers.
func (s *Server) routes() {
	s.mux.HandleFunc("/api/metrics", s.handleMetrics)
	s.mux.HandleFunc("/api/processes", s.handleProcesses)
	s.mux.HandleFunc("/api/health", s.handleHealth)
	s.mux.HandleFunc("/api/export/csv", s.handleExportCSV)
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
	Timestamp       int64   `json:"ts"`
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

	rows, err := s.db.QueryMetrics(since)
	if err != nil {
		http.Error(w, `{"error":"db error"}`, http.StatusInternalServerError)
		log.Printf("[server] QueryMetrics error: %v", err)
		return
	}

	// Build response — empty slice (not nil) so JSON returns [] not null
	data := make([]metricDataPoint, 0, len(rows))
	for _, row := range rows {
		data = append(data, metricDataPoint{
			Timestamp:       row.Timestamp.Unix(),
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
		})
	}

	resp := metricsResponse{Range: rangeParam, Count: len(data), Data: data}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	json.NewEncoder(w).Encode(resp)
}

type processResponse struct {
	Range string               `json:"range"`
	Count int                  `json:"count"`
	Data  []processDataPoint   `json:"data"`
}

type processDataPoint struct {
	Timestamp int64   `json:"ts"`
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

	rows, err := s.db.QueryProcesses(since)
	if err != nil {
		http.Error(w, `{"error":"db error"}`, http.StatusInternalServerError)
		return
	}

	data := make([]processDataPoint, 0, len(rows))
	for _, row := range rows {
		data = append(data, processDataPoint{
			Timestamp: row.Timestamp.Unix(),
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
		"status":         "ok",
		"metric_rows":    mc,
		"process_rows":   pc,
		"timestamp":      time.Now().Unix(),
	})
}

func (s *Server) handleExportCSV(w http.ResponseWriter, r *http.Request) {
	rangeParam := r.URL.Query().Get("range")
	if rangeParam == "" {
		rangeParam = "24h"
	}
	since := time.Now().Add(-parseRange(rangeParam))

	w.Header().Set("Content-Type", "text/csv")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="wmonitor_export_%s.csv"`, time.Now().Format("20060102_150405")))

	_, err := export.WriteCSV(w, s.db, since)
	if err != nil {
		log.Printf("[server] CSV export error: %v", err)
	}
}
