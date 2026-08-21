package collector

import (
	"testing"
	"time"

	gopsutil_net "github.com/shirou/gopsutil/v3/net"
)

func conn(status string, localPort, remotePort uint32, remoteIP string) gopsutil_net.ConnectionStat {
	return gopsutil_net.ConnectionStat{
		Status: status,
		Laddr:  gopsutil_net.Addr{IP: "0.0.0.0", Port: localPort},
		Raddr:  gopsutil_net.Addr{IP: remoteIP, Port: remotePort},
	}
}

func listener(port uint32) gopsutil_net.ConnectionStat {
	return gopsutil_net.ConnectionStat{
		Status: "LISTEN",
		Laddr:  gopsutil_net.Addr{IP: "0.0.0.0", Port: port},
	}
}

func trackerWith(conns []gopsutil_net.ConnectionStat, appPorts ...uint32) *TCPUserTracker {
	t := NewTCPUserTracker()
	if len(appPorts) > 0 {
		t.SetAppPorts(appPorts...)
	}
	t.setConnLister(func(string) ([]gopsutil_net.ConnectionStat, error) {
		return conns, nil
	})
	return t
}

// The core B7 regression: sockets in teardown are not users.
func TestClosingSocketsAreNotCounted(t *testing.T) {
	conns := []gopsutil_net.ConnectionStat{
		listener(8080),
		conn("CLOSE_WAIT", 8080, 51001, "10.0.0.5"),
		conn("FIN_WAIT1", 8080, 51002, "10.0.0.6"),
		conn("FIN_WAIT2", 8080, 51003, "10.0.0.7"),
		conn("TIME_WAIT", 8080, 51004, "10.0.0.8"),
	}
	tracker := trackerWith(conns, 8080)

	if got := tracker.GetConcurrentUsers(); got != 0 {
		t.Errorf("closing sockets counted as %d users, want 0", got)
	}
}

func TestEstablishedAndSynRecvAreCounted(t *testing.T) {
	conns := []gopsutil_net.ConnectionStat{
		listener(8080),
		conn("ESTABLISHED", 8080, 51001, "10.0.0.5"),
		conn("SYN_RECV", 8080, 51002, "10.0.0.6"),
	}
	tracker := trackerWith(conns, 8080)

	if got := tracker.GetConcurrentUsers(); got != 2 {
		t.Errorf("got %d users, want 2", got)
	}
}

// One client opening several sockets is one user but several connections.
func TestUsersAndConnectionsAreDistinctFigures(t *testing.T) {
	conns := []gopsutil_net.ConnectionStat{
		listener(8080),
		conn("ESTABLISHED", 8080, 51001, "203.0.113.9"),
		conn("ESTABLISHED", 8080, 51002, "203.0.113.9"),
		conn("ESTABLISHED", 8080, 51003, "203.0.113.9"),
	}
	tracker := trackerWith(conns, 8080)

	if users := tracker.GetConcurrentUsers(); users != 1 {
		t.Errorf("got %d users, want 1 (same IP)", users)
	}
	if socketCount := tracker.GetActiveConnections(); socketCount != 3 {
		t.Errorf("got %d connections, want 3", socketCount)
	}
}

// Administrative sessions must not be counted as application users when no
// -app-port is configured.
func TestAdminPortsExcludedByDefault(t *testing.T) {
	conns := []gopsutil_net.ConnectionStat{
		listener(22),   // ssh
		listener(3389), // rdp
		listener(445),  // smb
		listener(5432), // postgres
		listener(3000), // the actual application
		conn("ESTABLISHED", 22, 51001, "10.0.0.5"),
		conn("ESTABLISHED", 3389, 51002, "10.0.0.6"),
		conn("ESTABLISHED", 445, 51003, "10.0.0.7"),
		conn("ESTABLISHED", 5432, 51004, "10.0.0.8"),
		conn("ESTABLISHED", 3000, 51005, "10.0.0.9"),
	}
	tracker := trackerWith(conns) // no app ports: rely on defaults

	if got := tracker.GetConcurrentUsers(); got != 1 {
		t.Errorf("got %d users, want 1 (only the app on :3000)", got)
	}
}

func TestExplicitExcludePortWins(t *testing.T) {
	conns := []gopsutil_net.ConnectionStat{
		listener(8080),
		conn("ESTABLISHED", 8080, 51001, "10.0.0.5"),
	}
	tracker := trackerWith(conns, 8080)
	tracker.SetExcludePorts(8080) // wmonitor's own dashboard port

	if got := tracker.GetConcurrentUsers(); got != 0 {
		t.Errorf("got %d users, want 0 (port excluded)", got)
	}
}

func TestConnectionsToUnmonitoredPortsIgnored(t *testing.T) {
	conns := []gopsutil_net.ConnectionStat{
		listener(8080),
		listener(9999),
		conn("ESTABLISHED", 9999, 51001, "10.0.0.5"),
	}
	tracker := trackerWith(conns, 8080)

	if got := tracker.GetConcurrentUsers(); got != 0 {
		t.Errorf("got %d users, want 0 (traffic was on a non-app port)", got)
	}
}

func TestUsersExpireAfterWindow(t *testing.T) {
	conns := []gopsutil_net.ConnectionStat{
		listener(8080),
		conn("ESTABLISHED", 8080, 51001, "10.0.0.5"),
	}
	tracker := trackerWith(conns, 8080)
	tracker.SetWindow(50 * time.Millisecond)

	if got := tracker.GetConcurrentUsers(); got != 1 {
		t.Fatalf("got %d users, want 1", got)
	}

	// Client disappears from the connection table.
	tracker.setConnLister(func(string) ([]gopsutil_net.ConnectionStat, error) {
		return []gopsutil_net.ConnectionStat{listener(8080)}, nil
	})
	time.Sleep(80 * time.Millisecond)

	if got := tracker.GetConcurrentUsers(); got != 0 {
		t.Errorf("got %d users after window expiry, want 0", got)
	}
}

func TestSetWindowRejectsNonPositive(t *testing.T) {
	tracker := NewTCPUserTracker()
	tracker.SetWindow(0)
	if tracker.window != defaultUserWindow {
		t.Errorf("window = %v, want unchanged %v", tracker.window, defaultUserWindow)
	}
	tracker.SetWindow(-time.Second)
	if tracker.window != defaultUserWindow {
		t.Errorf("window = %v, want unchanged %v", tracker.window, defaultUserWindow)
	}
}

func TestLoopbackClientsAreLabelled(t *testing.T) {
	conns := []gopsutil_net.ConnectionStat{
		listener(8080),
		conn("ESTABLISHED", 8080, 51001, "127.0.0.1"),
	}
	tracker := trackerWith(conns, 8080)

	if got := tracker.GetConcurrentUsers(); got != 1 {
		t.Fatalf("got %d users, want 1", got)
	}
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	if _, ok := tracker.recentUsers["local:127.0.0.1"]; !ok {
		t.Errorf("loopback client not labelled: %v", tracker.recentUsers)
	}
}
