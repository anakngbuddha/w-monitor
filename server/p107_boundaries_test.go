package server

import (
	"crypto/tls"
	"net/http/httptest"
	"testing"
	"time"
)

func TestSessionOriginIncludesSchemeAndRejectsMalformedOrigins(t *testing.T) {
	s := &Server{allowedOrigins: []string{"https://other.example"}}
	for _, origin := range []string{"http://hub.example", "null", "https://hub.example/path", "https://user@hub.example", "https://hub.example?x=1", "https://other.example"} {
		r := httptest.NewRequest("POST", "https://hub.example/api/session", nil)
		r.TLS = &tls.ConnectionState{}
		r.Header.Set("Origin", origin)
		if s.originOK(r) {
			t.Errorf("accepted forbidden origin %q", origin)
		}
	}
	r := httptest.NewRequest("POST", "https://hub.example/api/session", nil)
	r.Header.Set("Origin", "https://hub.example")
	if !s.originOK(r) {
		t.Fatal("same origin rejected")
	}
}

func TestSessionStoreBoundsAndReclaimsExpired(t *testing.T) {
	st := newSessionStore()
	for i := 0; i < maxCredentialSessions+3; i++ {
		id, err := newSessionID()
		if err != nil {
			t.Fatal(err)
		}
		if !st.put(id, sessionEntry{credHash: "one", expires: time.Now().Add(time.Hour)}) {
			t.Fatal("per-credential replacement rejected")
		}
	}
	if len(st.m) != maxCredentialSessions {
		t.Fatalf("session count = %d", len(st.m))
	}
	st.put("expired", sessionEntry{credHash: "expired", expires: time.Now().Add(-time.Second)})
	st.put("new", sessionEntry{credHash: "new", expires: time.Now().Add(time.Hour)})
	if _, ok := st.m["expired"]; ok {
		t.Fatal("expired session was not reclaimed")
	}
}

func TestSessionCookieSecureAndDeletion(t *testing.T) {
	s := &Server{}
	r := httptest.NewRequest("POST", "https://hub.example/api/session", nil)
	w := httptest.NewRecorder()
	s.setSessionCookie(w, r, "opaque", 30)
	c := w.Result().Cookies()[0]
	if !c.Secure || !c.HttpOnly || c.Domain != "" || c.Path != "/" {
		t.Fatalf("unsafe session cookie: %+v", c)
	}
	w = httptest.NewRecorder()
	s.setSessionCookie(w, r, "", -1)
	c = w.Result().Cookies()[0]
	if c.MaxAge != -1 || c.Value != "" || !c.Expires.Before(time.Now()) {
		t.Fatal("logout cookie did not expire")
	}
}
