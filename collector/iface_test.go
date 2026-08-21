package collector

import (
	"net"
	"testing"
)

// The detected external interface must be a real, non-loopback interface that
// actually exists on this host. The old byte-count heuristic could return a
// loopback or virtual device.
func TestDetectExternalIfaceIsRealAndNotLoopback(t *testing.T) {
	resetIfaceCache()
	name := detectExternalIface()
	if name == "" {
		t.Skip("no external interface on this host (sandboxed or air-gapped)")
	}

	ifaces, err := net.Interfaces()
	if err != nil {
		t.Fatalf("net.Interfaces: %v", err)
	}

	for _, ifc := range ifaces {
		if ifc.Name != name {
			continue
		}
		if ifc.Flags&net.FlagLoopback != 0 {
			t.Errorf("detected %q as external but it is a loopback interface", name)
		}
		if ifc.Flags&net.FlagUp == 0 {
			t.Errorf("detected %q as external but it is down", name)
		}
		return
	}
	t.Errorf("detected external interface %q does not exist in net.Interfaces()", name)
}

func TestDetectExternalIfaceIsCached(t *testing.T) {
	resetIfaceCache()
	first := detectExternalIface()
	second := detectExternalIface()
	if first != second {
		t.Errorf("cached detection disagreed: %q then %q", first, second)
	}
}

func TestLoopbackNamesUsesFlags(t *testing.T) {
	names := loopbackNames()
	if len(names) == 0 {
		t.Fatal("expected at least one loopback interface")
	}
	ifaces, err := net.Interfaces()
	if err != nil {
		t.Skip("cannot enumerate interfaces")
	}
	for _, ifc := range ifaces {
		isLoopback := ifc.Flags&net.FlagLoopback != 0
		if isLoopback != names[ifc.Name] {
			t.Errorf("%q: loopback flag = %v but set membership = %v", ifc.Name, isLoopback, names[ifc.Name])
		}
	}
}

func TestIsVirtualIface(t *testing.T) {
	virtual := []string{
		"docker0", "br-1a2b3c", "veth9f2a", "virbr0", "vmnet8",
		"tun0", "tap0", "wg0", "utun3", "tailscale0",
		"isatap.{GUID}", "Teredo Tunneling Pseudo-Interface",
		"VirtualBox Host-Only Network", "Loopback Pseudo-Interface 2",
	}
	for _, name := range virtual {
		if !isVirtualIface(name) {
			t.Errorf("%q should be classified as virtual", name)
		}
	}

	physical := []string{"eth0", "eno1", "enp3s0", "wlan0", "en0", "Ethernet", "Wi-Fi"}
	for _, name := range physical {
		if isVirtualIface(name) {
			t.Errorf("%q should NOT be classified as virtual", name)
		}
	}
}
