package collector

import (
	"log"
	"net"
	"strings"
	"sync"
	"time"

	gopsutil_net "github.com/shirou/gopsutil/v3/net"
)

// ifaceCacheTTL bounds how often the default route is resolved. The external
// NIC can legitimately change (failover, VPN up/down, docking station), so this
// is cached rather than resolved once at startup, but it does not need to be
// recomputed on every 10s tick either.
const ifaceCacheTTL = 5 * time.Minute

var (
	ifaceMu       sync.Mutex
	ifaceCached   string
	ifaceCachedAt time.Time
	ifaceLoggedAs string
)

// detectExternalIface returns the name of the NIC that holds the default route.
//
// This is the interface through which traffic leaves for the internet, which is
// the only defensible definition of "external". Returns "" if it cannot be
// determined, in which case the caller treats all traffic as internal.
func detectExternalIface() string {
	ifaceMu.Lock()
	defer ifaceMu.Unlock()

	if ifaceCached != "" && time.Since(ifaceCachedAt) < ifaceCacheTTL {
		return ifaceCached
	}

	name := defaultRouteIface()
	if name == "" {
		name = busiestPhysicalIface()
		if name != "" {
			log.Printf("[collector] could not resolve default route; falling back to busiest NIC %q (override with -external-iface)", name)
		}
	}

	ifaceCached = name
	ifaceCachedAt = time.Now()

	// Log only on change, so a wrong pick is visible without spamming the log.
	if name != ifaceLoggedAs {
		if name == "" {
			log.Printf("[collector] external NIC undetermined — all traffic will be counted as internal (override with -external-iface)")
		} else {
			log.Printf("[collector] external NIC detected: %q", name)
		}
		ifaceLoggedAs = name
	}
	return name
}

// defaultRouteIface finds the interface the kernel would use to reach the
// public internet.
//
// Dialing a UDP address performs no I/O: the kernel merely consults the routing
// table and binds a local source address. Reading that source address back and
// matching it against the interface list tells us which NIC owns the default
// route, without parsing /proc/net/route on Linux or calling GetBestInterfaceEx
// on Windows.
func defaultRouteIface() string {
	localIP := outboundSourceIP()
	if localIP == nil {
		return ""
	}

	ifaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			var ip net.IP
			switch v := addr.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if ip != nil && ip.Equal(localIP) {
				return ifc.Name
			}
		}
	}
	return ""
}

// outboundSourceIP returns the local address the OS would use for outbound
// internet traffic. Tries IPv4 first, then IPv6.
func outboundSourceIP() net.IP {
	for _, target := range []string{"8.8.8.8:80", "[2001:4860:4860::8888]:80"} {
		conn, err := net.DialTimeout("udp", target, 2*time.Second)
		if err != nil {
			continue
		}
		udpAddr, ok := conn.LocalAddr().(*net.UDPAddr)
		conn.Close()
		if ok && udpAddr.IP != nil && !udpAddr.IP.IsUnspecified() {
			return udpAddr.IP
		}
	}
	return nil
}

// busiestPhysicalIface is the last-resort fallback: the non-loopback, non-virtual
// interface with the most traffic.
//
// This is the old heuristic, kept only for hosts where the routing lookup fails
// (air-gapped machines, for instance). It is a guess, and it is logged as one.
func busiestPhysicalIface() string {
	counters, err := gopsutil_net.IOCounters(true)
	if err != nil || len(counters) == 0 {
		return ""
	}

	loopbacks := loopbackNames()

	var bestName string
	var bestBytes uint64
	for _, ifc := range counters {
		if loopbacks[ifc.Name] || isVirtualIface(ifc.Name) {
			continue
		}
		if total := ifc.BytesSent + ifc.BytesRecv; total > bestBytes {
			bestBytes = total
			bestName = ifc.Name
		}
	}
	return bestName
}

// loopbackNames returns the set of loopback interface names as reported by the
// OS.
//
// The previous code string-matched "lo" and "Loopback Pseudo-Interface 1",
// which misses lo0 on BSD/macOS and any Windows loopback numbered other than 1.
// net.FlagLoopback is authoritative.
func loopbackNames() map[string]bool {
	names := make(map[string]bool)
	ifaces, err := net.Interfaces()
	if err != nil {
		// Fall back to the well-known names rather than returning nothing.
		names["lo"] = true
		names["lo0"] = true
		return names
	}
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagLoopback != 0 {
			names[ifc.Name] = true
		}
	}
	return names
}

// virtualIfacePrefixes are software interfaces that carry container, VM, VPN, or
// tunnel traffic. Counting them as candidates for "the external NIC" is how a
// Docker host ends up reporting br-xxxx as its internet uplink.
var virtualIfacePrefixes = []string{
	"docker", "br-", "veth", "virbr", "vmnet", "vboxnet",
	"tun", "tap", "wg", "utun", "zt", "tailscale",
	"isatap", "teredo", "bluetooth",
}

func isVirtualIface(name string) bool {
	lower := strings.ToLower(name)
	for _, p := range virtualIfacePrefixes {
		if strings.HasPrefix(lower, p) {
			return true
		}
	}
	// Windows friendly names, e.g. "VirtualBox Host-Only Network".
	return strings.Contains(lower, "virtual") || strings.Contains(lower, "pseudo-interface")
}

// resetIfaceCache clears the cached detection. Test helper.
func resetIfaceCache() {
	ifaceMu.Lock()
	defer ifaceMu.Unlock()
	ifaceCached = ""
	ifaceCachedAt = time.Time{}
	ifaceLoggedAs = ""
}
