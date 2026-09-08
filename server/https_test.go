package server

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func TestRequestIsHTTPSIgnoresSpoofedForwardedProto(t *testing.T) {
	t.Setenv("WMONITOR_TRUSTED_PROXIES", "")
	req := httptest.NewRequest(http.MethodPost, "http://hub/api/enroll", nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	req.RemoteAddr = "203.0.113.9:4444"
	if requestIsHTTPS(req) {
		t.Fatal("spoofed X-Forwarded-Proto must not count as HTTPS")
	}
}

func TestRequestIsHTTPSTrustsConfiguredProxy(t *testing.T) {
	t.Setenv("WMONITOR_TRUSTED_PROXIES", "192.0.2.10,10.0.0.0/8")
	req := httptest.NewRequest(http.MethodPost, "http://hub/api/enroll", nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	req.RemoteAddr = "192.0.2.10:443"
	if !requestIsHTTPS(req) {
		t.Fatal("trusted proxy + X-Forwarded-Proto=https should be HTTPS")
	}
	req.RemoteAddr = "203.0.113.9:443"
	if requestIsHTTPS(req) {
		t.Fatal("untrusted peer must not use X-Forwarded-Proto")
	}
}

func TestRequestIsHTTPSDirectTLS(t *testing.T) {
	os.Unsetenv("WMONITOR_TRUSTED_PROXIES")
	req := httptest.NewRequest(http.MethodGet, "https://hub/", nil)
	req.TLS = &tls.ConnectionState{}
	if !requestIsHTTPS(req) {
		t.Fatal("direct TLS must be HTTPS")
	}
}
