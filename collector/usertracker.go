package collector

import (
	"net"
	"strings"
	"sync"
	"time"

	gopsutil_net "github.com/shirou/gopsutil/v3/net"
)

// TCPUserTracker tracks concurrent active users by inspecting active TCP connections
// to listening application ports on the host.
type TCPUserTracker struct {
	mu           sync.Mutex
	appPorts     map[uint32]bool
	excludePorts map[uint32]bool
	recentUsers  map[string]time.Time
	window       time.Duration
}

// NewTCPUserTracker creates a tracker with default 60-second sliding activity window.
func NewTCPUserTracker() *TCPUserTracker {
	return &TCPUserTracker{
		appPorts:     make(map[uint32]bool),
		excludePorts: make(map[uint32]bool),
		recentUsers:  make(map[string]time.Time),
		window:       60 * time.Second,
	}
}

// SetAppPorts restricts connection counting to specific local application listening ports.
func (t *TCPUserTracker) SetAppPorts(ports ...uint32) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.appPorts = make(map[uint32]bool)
	for _, p := range ports {
		if p > 0 {
			t.appPorts[p] = true
		}
	}
}

// SetExcludePorts excludes certain ports (e.g. wmonitor's own dashboard port, database ports).
func (t *TCPUserTracker) SetExcludePorts(ports ...uint32) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, p := range ports {
		if p > 0 {
			t.excludePorts[p] = true
		}
	}
}

// RecordUser manually records an active user key (e.g. from an HTTP handler or middleware).
func (t *TCPUserTracker) RecordUser(userKey string) {
	if userKey == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.recentUsers[userKey] = time.Now()
}

// GetConcurrentUsers returns the number of active distinct users/sessions in the last window (60s).
func (t *TCPUserTracker) GetConcurrentUsers() int {
	t.mu.Lock()
	defer t.mu.Unlock()

	now := time.Now()

	// 1. Fetch current TCP connections
	conns, err := gopsutil_net.Connections("tcp")
	if err != nil {
		conns, err = gopsutil_net.Connections("all")
	}

	if err == nil {
		// Identify listening ports
		listening := make(map[uint32]bool)
		for _, c := range conns {
			if strings.EqualFold(c.Status, "LISTEN") {
				if len(t.appPorts) > 0 {
					if t.appPorts[c.Laddr.Port] && !t.excludePorts[c.Laddr.Port] {
						listening[c.Laddr.Port] = true
					}
				} else {
					if !t.excludePorts[c.Laddr.Port] && !isCommonDBPort(c.Laddr.Port) {
						listening[c.Laddr.Port] = true
					}
				}
			}
		}

		// If specific appPorts were configured, ensure they are in the listening map
		if len(t.appPorts) > 0 {
			for p := range t.appPorts {
				if !t.excludePorts[p] {
					listening[p] = true
				}
			}
		}

		// Check established / active incoming connections
		for _, c := range conns {
			status := strings.ToUpper(c.Status)
			if status != "ESTABLISHED" && status != "SYN_RECV" && status != "CLOSE_WAIT" && status != "FIN_WAIT1" && status != "FIN_WAIT2" {
				continue
			}

			// Must be inbound to a listening port
			if !listening[c.Laddr.Port] {
				continue
			}

			// Determine client identity key
			remoteIP := c.Raddr.IP
			if remoteIP == "" || remoteIP == "0.0.0.0" || remoteIP == "::" {
				continue
			}

			var clientKey string
			if isLoopback(remoteIP) {
				// Local client testing web app on localhost
				clientKey = "local:" + remoteIP
			} else {
				clientKey = remoteIP
			}

			t.recentUsers[clientKey] = now
		}
	}

	// 2. Clean up expired users older than window
	cutoff := now.Add(-t.window)
	for k, lastSeen := range t.recentUsers {
		if lastSeen.Before(cutoff) {
			delete(t.recentUsers, k)
		}
	}

	return len(t.recentUsers)
}

func isLoopback(ipStr string) bool {
	if ipStr == "127.0.0.1" || ipStr == "::1" || ipStr == "localhost" {
		return true
	}
	ip := net.ParseIP(ipStr)
	return ip != nil && ip.IsLoopback()
}

func isCommonDBPort(port uint32) bool {
	switch port {
	case 5432, 3306, 6379, 27017, 1433, 1521, 9200, 9300, 9042:
		return true
	default:
		return false
	}
}
