package agent

import (
	"crypto/tls"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

func TestRequireHTTPSHub(t *testing.T) {
	if err := RequireHTTPSHub("https://hub.example"); err != nil {
		t.Fatal(err)
	}
	if err := RequireHTTPSHub("http://127.0.0.1:8080"); err != nil {
		t.Fatal(err)
	}
	if err := RequireHTTPSHub("http://hub.example"); !errors.Is(err, ErrInsecureHub) {
		t.Fatalf("got %v, want ErrInsecureHub", err)
	}
}

func TestSameHubOrigin(t *testing.T) {
	if !SameHubOrigin("https://hub.example/", "https://hub.example") {
		t.Fatal("expected same origin")
	}
	if SameHubOrigin("https://hub.example", "https://other.example") {
		t.Fatal("different hosts")
	}
	if SameHubOrigin("https://hub.example", "http://hub.example") {
		t.Fatal("scheme mismatch")
	}
}

func TestNoCredentialRedirect(t *testing.T) {
	prev, _ := http.NewRequest(http.MethodPost, "https://hub.example/api/enroll", nil)
	nextHTTPS, _ := url.Parse("https://evil.example/api/enroll")
	req := &http.Request{URL: nextHTTPS}
	if err := noCredentialRedirect(req, []*http.Request{prev}); !errors.Is(err, ErrHubRedirect) {
		t.Fatalf("cross-origin: got %v", err)
	}
	down, _ := url.Parse("http://hub.example/api/enroll")
	req.URL = down
	if err := noCredentialRedirect(req, []*http.Request{prev}); !errors.Is(err, ErrHubRedirect) {
		t.Fatalf("downgrade: got %v", err)
	}
	same, _ := url.Parse("https://hub.example/api/ingest")
	req.URL = same
	if err := noCredentialRedirect(req, []*http.Request{prev}); err != nil {
		t.Fatalf("same origin: %v", err)
	}
}

func TestHubClientRejectsUntrustedTLS(t *testing.T) {
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()
	client := newHubHTTPClient(3 * time.Second)
	_, err := client.Get(ts.URL)
	if err == nil {
		t.Fatal("expected TLS verification failure against an untrusted httptest certificate")
	}
}

func TestHubClientDisablesInsecureSkipVerify(t *testing.T) {
	client := newHubHTTPClient(time.Second)
	tr, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatal("expected *http.Transport")
	}
	if tr.TLSClientConfig != nil && tr.TLSClientConfig.InsecureSkipVerify {
		t.Fatal("InsecureSkipVerify must be false")
	}
	if tr.TLSClientConfig == nil || tr.TLSClientConfig.MinVersion < tls.VersionTLS12 {
		t.Fatal("TLS MinVersion must be at least TLS 1.2")
	}
}
