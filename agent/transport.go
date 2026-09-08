package agent

import (
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var (
	// ErrInsecureHub is returned when a production agent would send secrets over HTTP.
	ErrInsecureHub = errors.New("agent: hub URL must be https")
	// ErrHubRedirect is returned when an authenticated request would follow a cross-origin or downgrade redirect.
	ErrHubRedirect = errors.New("agent: refusing redirect that would move credentials")
	// ErrHubMismatch is returned when stored credentials belong to a different hub origin.
	ErrHubMismatch = errors.New("agent: stored credentials do not match the configured hub URL")
)

// CanonicalHubURL trims trailing slashes for origin comparison.
func CanonicalHubURL(raw string) string {
	return strings.TrimRight(strings.TrimSpace(raw), "/")
}

// SameHubOrigin reports whether two hub URLs share scheme and host (including port).
func SameHubOrigin(a, b string) bool {
	ua, err := url.Parse(CanonicalHubURL(a))
	if err != nil || ua.Host == "" {
		return CanonicalHubURL(a) == CanonicalHubURL(b)
	}
	ub, err := url.Parse(CanonicalHubURL(b))
	if err != nil || ub.Host == "" {
		return false
	}
	return strings.EqualFold(ua.Scheme, ub.Scheme) && strings.EqualFold(ua.Host, ub.Host)
}

// RequireHTTPSHub rejects non-HTTPS hub URLs except loopback (tests / local).
func RequireHTTPSHub(raw string) error {
	u, err := url.Parse(CanonicalHubURL(raw))
	if err != nil || u.Scheme == "" {
		return fmt.Errorf("%w: %q", ErrInsecureHub, raw)
	}
	if strings.EqualFold(u.Scheme, "https") {
		return nil
	}
	host := strings.ToLower(u.Hostname())
	if host == "localhost" || strings.HasPrefix(host, "127.") || host == "::1" {
		return nil
	}
	return fmt.Errorf("%w: %s", ErrInsecureHub, u.Scheme)
}

func noCredentialRedirect(req *http.Request, via []*http.Request) error {
	if len(via) == 0 {
		return nil
	}
	prev := via[len(via)-1]
	if req.URL.Scheme != "https" && !isLoopbackHost(req.URL.Hostname()) {
		return ErrHubRedirect
	}
	if !strings.EqualFold(req.URL.Host, prev.URL.Host) || !strings.EqualFold(req.URL.Scheme, prev.URL.Scheme) {
		return ErrHubRedirect
	}
	return nil
}

func isLoopbackHost(host string) bool {
	h := strings.ToLower(host)
	return h == "localhost" || h == "127.0.0.1" || h == "::1"
}

func newHubHTTPClient(timeout time.Duration) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if transport.TLSClientConfig == nil {
		transport.TLSClientConfig = &tls.Config{}
	} else {
		transport.TLSClientConfig = transport.TLSClientConfig.Clone()
	}
	transport.TLSClientConfig.MinVersion = tls.VersionTLS12
	transport.TLSClientConfig.InsecureSkipVerify = false
	return &http.Client{
		Timeout:       timeout,
		CheckRedirect: noCredentialRedirect,
		Transport:     transport,
	}
}
