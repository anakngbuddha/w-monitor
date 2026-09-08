package server

import (
	"net"
	"net/http"
	"os"
	"strings"
)

// requestIsHTTPS reports whether this request arrived over TLS.
// X-Forwarded-Proto is trusted only when the peer is in WMONITOR_TRUSTED_PROXIES
// (comma-separated IPs or CIDRs). An empty list never trusts spoofed headers.
func requestIsHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	if !strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")), "https") {
		return false
	}
	return trustedProxyPeer(r)
}

func trustedProxyPeer(r *http.Request) bool {
	raw := strings.TrimSpace(os.Getenv("WMONITOR_TRUSTED_PROXIES"))
	if raw == "" {
		return false
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if strings.Contains(part, "/") {
			_, n, err := net.ParseCIDR(part)
			if err == nil && n.Contains(ip) {
				return true
			}
			continue
		}
		if p := net.ParseIP(part); p != nil && p.Equal(ip) {
			return true
		}
	}
	return false
}
