package dashboard_test

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"Zeus/dashboard"
	"Zeus/server"
	"Zeus/storage"
)

func TestDashboardServing(t *testing.T) {
	db, err := storage.Open(filepath.Join(t.TempDir(), "dashboard.db"))
	if err != nil { t.Fatal(err) }
	defer db.Close()
	srv := server.New(db, "9999")
	if err := dashboard.Register(srv); err != nil { t.Fatal(err) }
	get := func(path string) *httptest.ResponseRecorder { w := httptest.NewRecorder(); srv.Handler().ServeHTTP(w, httptest.NewRequest("GET", path, nil)); return w }
	html := get("/")
	if html.Code != http.StatusOK { t.Fatalf("HTML status: %d", html.Code) }
	for _, snippet := range []string{"W-Monitor", "server-badge", `<link rel="icon"`, `/dashboard.js`, `/chart.umd.min.js`} { if !strings.Contains(html.Body.String(), snippet) { t.Errorf("HTML missing %q", snippet) } }
	if strings.Contains(html.Body.String(), "<script>") || strings.Contains(html.Header().Get("Content-Security-Policy"), "script-src 'self' 'unsafe-inline'") { t.Fatal("inline scripts remain executable") }
	js := get("/dashboard.js")
	if js.Code != http.StatusOK { t.Fatalf("script status: %d", js.Code) }
	for _, snippet := range []string{"updateMultiServerChart", "updateMultiServerNetChart", "SERVER_COLORS", "minDiskFree", "renderProcesses", "Object.create(null)", "credentials: 'same-origin'"} { if !strings.Contains(js.Body.String(), snippet) { t.Errorf("script missing %q", snippet) } }
	if w := get("/static/index.html"); w.Code != http.StatusNotFound { t.Fatal("legacy source template is accessible") }
}
