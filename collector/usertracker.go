package collector

import (
	"log"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	gopsutil_net "github.com/shirou/gopsutil/v3/net"
)

// defaultUserWindow is how long a client is considered "still here" after its
// last observed activity.
const defaultUserWindow = 60 * time.Second

// activeConnStates are the TCP states that represent a client that is actually
// present and able to exchange data.
//
// Deliberately excluded:
//   - CLOSE_WAIT: the peer has sent FIN; the connection is half-closed and the
//     local application has not yet closed its end. On Windows these can sit in
//     the table for minutes, so counting them inflated the user count badly.
//   - FIN_WAIT1 / FIN_WAIT2: local close initiated, teardown in progress.
//   - TIME_WAIT: fully closed, lingering only to absorb stray packets.
//
// SYN_RECV is included: the handshake is mid-flight but a real client is on the
// other end, and excluding it would undercount during connection bursts.
var activeConnStates = map[string]bool{
	"ESTABLISHED": true,
	"SYN_RECV":    true,
	"SYN_RECEIVED": true, // some platforms spell it out
}

// defaultExcludedPorts are listening ports that are never application traffic.
//
// With no -app-port configured the tracker has to guess which listeners are the
// monitored application. Previously it excluded only database ports, so an
// administrator's RDP session, a Windows file share, an SSH connection, or
// another monitoring system's poller each counted as a concurrent application
// user.
var defaultExcludedPorts = map[uint32]bool{
	// Databases and caches
	5432: true, // postgres
	3306: true, // mysql
	6379: true, // redis
	27017: true, // mongodb
	1433: true, // sql server
	1521: true, // oracle
	9200: true, // elasticsearch http
	9300: true, // elasticsearch transport
	9042: true, // cassandra
	11211: true, // memcached
	5672: true, // rabbitmq
	// Remote administration and file sharing — these are operators, not users
	22: true, // ssh
	23: true, // telnet
	3389: true, // rdp
	445: true, // smb
	139: true, // netbios session
	135: true, // msrpc / dcom endpoint mapper
	5985: true, // winrm http
	5986: true, // winrm https
	111: true, // rpcbind
	2049: true, // nfs
}

// connLister abstracts the OS connection table so the state-filtering logic can
// be tested deterministically.
type connLister func(kind string) ([]gopsutil_net.ConnectionStat, error)

// TCPUserTracker tracks concurrent active users by inspecting active TCP connections
// to listening application ports on the host.
type TCPUserTracker struct {
	mu           sync.Mutex
	appPorts     map[uint32]bool
	excludePorts map[uint32]bool
	recentUsers  map[string]time.Time // distinct client identities (IP, or manual key)
	recentConns  map[string]time.Time // distinct sockets (ip:port)
	window       time.Duration
	listConns    connLister
	warnedNoPort bool
}

// NewTCPUserTracker creates a tracker with default 60-second sliding activity window.
func NewTCPUserTracker() *TCPUserTracker {
	return &TCPUserTracker{
		appPorts:     make(map[uint32]bool),
		excludePorts: make(map[uint32]bool),
		recentUsers:  make(map[string]time.Time),
		recentConns:  make(map[string]time.Time),
		window:       defaultUserWindow,
		listConns:    gopsutil_net.Connections,
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

// SetWindow sets how long a client remains counted after its last activity.
// The window was previously a hardcoded constant with no way to tune it: too
// short and short-poll clients flicker, too long and the count lags reality.
func (t *TCPUserTracker) SetWindow(d time.Duration) {
	if d <= 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.window = d
}

// setConnLister injects a connection source. Test helper.
func (t *TCPUserTracker) setConnLister(l connLister) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.listConns = l
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

// GetConcurrentUsers returns the number of distinct client identities active in
// the last window.
//
// "Distinct identity" means distinct remote IP. Note the inherent limitation:
// every client behind a single NAT gateway shares one public IP and therefore
// counts as one user. Use GetActiveConnections for a socket-level figure.
func (t *TCPUserTracker) GetConcurrentUsers() int {
	t.refresh()
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.recentUsers)
}

// GetActiveConnections returns the number of distinct client sockets active in
// the last window.
//
// This is the companion to GetConcurrentUsers: a single browser typically opens
// several parallel sockets, so this over-counts humans, while GetConcurrentUsers
// under-counts them behind NAT. Reporting both is honest; reporting one of them
// as "users" without qualification is not.
func (t *TCPUserTracker) GetActiveConnections() int {
	t.refresh()
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.recentConns)
}

// refresh samples the OS connection table and expires stale entries.
func (t *TCPUserTracker) refresh() {
	t.mu.Lock()
	lister := t.listConns
	appPorts := make(map[uint32]bool, len(t.appPorts))
	for p := range t.appPorts {
		appPorts[p] = true
	}
	excluded := make(map[uint32]bool, len(t.excludePorts))
	for p := range t.excludePorts {
		excluded[p] = true
	}
	shouldWarn := len(appPorts) == 0 && !t.warnedNoPort
	if shouldWarn {
		t.warnedNoPort = true
	}
	t.mu.Unlock()

	if shouldWarn {
		log.Println("[usertracker] no -app-port configured: counting connections to ALL non-excluded listening ports. This is a rough upper bound, not an application user count. Set -app-port for an accurate figure.")
	}

	if lister == nil {
		lister = gopsutil_net.Connections
	}

	conns, err := lister("tcp")
	if err != nil {
		conns, err = lister("all")
	}

	now := time.Now()

	if err == nil {
		listening := listeningAppPorts(conns, appPorts, excluded)

		t.mu.Lock()
		for _, c := range conns {
			if !activeConnStates[strings.ToUpper(c.Status)] {
				continue
			}
			if !listening[c.Laddr.Port] {
				continue
			}

			remoteIP := c.Raddr.IP
			if remoteIP == "" || remoteIP == "0.0.0.0" || remoteIP == "::" {
				continue
			}

			userKey := remoteIP
			if isLoopback(remoteIP) {
				userKey = "local:" + remoteIP
			}
			t.recentUsers[userKey] = now

			// Socket identity includes the ephemeral remote port.
			t.recentConns[userKey+":"+strconv.FormatUint(uint64(c.Raddr.Port), 10)] = now
		}
		t.mu.Unlock()
	}

	t.mu.Lock()
	cutoff := now.Add(-t.window)
	for k, lastSeen := range t.recentUsers {
		if lastSeen.Before(cutoff) {
			delete(t.recentUsers, k)
		}
	}
	for k, lastSeen := range t.recentConns {
		if lastSeen.Before(cutoff) {
			delete(t.recentConns, k)
		}
	}
	t.mu.Unlock()
}

// listeningAppPorts determines which local ports count as application listeners.
func listeningAppPorts(conns []gopsutil_net.ConnectionStat, appPorts, excluded map[uint32]bool) map[uint32]bool {
	listening := make(map[uint32]bool)

	for _, c := range conns {
		if !strings.EqualFold(c.Status, "LISTEN") {
			continue
		}
		port := c.Laddr.Port
		if excluded[port] {
			continue
		}
		if len(appPorts) > 0 {
			if appPorts[port] {
				listening[port] = true
			}
			continue
		}
		if !defaultExcludedPorts[port] {
			listening[port] = true
		}
	}

	// Explicitly configured app ports count even if no LISTEN row was visible
	// for them (permissions can hide other users' listeners on Windows).
	for p := range appPorts {
		if !excluded[p] {
			listening[p] = true
		}
	}
	return listening
}

func isLoopback(ipStr string) bool {
	if ipStr == "127.0.0.1" || ipStr == "::1" || ipStr == "localhost" {
		return true
	}
	ip := net.ParseIP(ipStr)
	return ip != nil && ip.IsLoopback()
}
