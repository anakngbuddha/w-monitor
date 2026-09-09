package dashboard_test

import (
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"Zeus/dashboard"
	"Zeus/server"
	"Zeus/storage"
)

// Starts a loopback Hub for a real browser cookie/CSRF check. Skipped unless
// WMONITOR_PHASE1_BROWSER=1. The URL file is a disposable fixture, not a
// production credential export.
func TestP107LoopbackHubForBrowser(t *testing.T) {
	if os.Getenv("WMONITOR_PHASE1_BROWSER") != "1" {
		t.Skip("NOT RUN: real browser fixture not enabled")
	}
	db, err := storage.Open(filepath.Join(t.TempDir(), "browser.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	srv := server.New(db, "0")
	srv.EnableHubMode(db)
	if err := dashboard.Register(srv); err != nil {
		t.Fatal(err)
	}
	readToken, err := storage.GenerateToken(storage.KindRead)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertAPIKey(storage.APIKeyRecord{
		TenantID: "t_browser", ClientName: "BrowserCo", KeyHash: storage.HashAPIKey(readToken),
		KeyPrefix: storage.ExtractKeyPrefix(readToken), Kind: storage.KindRead, Scope: storage.ScopeRead,
		CreatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go http.Serve(ln, srv.Handler())
	payload, _ := json.Marshal(map[string]string{"url": "http://" + ln.Addr().String() + "/", "read_token": readToken})
	urlFile := os.Getenv("WMONITOR_PHASE1_BROWSER_URLFILE")
	if urlFile == "" {
		urlFile = filepath.Join(t.TempDir(), "browser-hub.json")
	}
	if err := os.WriteFile(urlFile, payload, 0600); err != nil {
		t.Fatal(err)
	}
	t.Logf("browser hub listening on %s", ln.Addr())
	<-t.Context().Done()
}
