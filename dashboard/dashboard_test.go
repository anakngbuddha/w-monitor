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
	tmp := filepath.Join(t.TempDir(), "dash_test.db")
	db, err := storage.Open(tmp)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer db.Close()

	srv := server.New(db, "9999")
	if err := dashboard.Register(srv); err != nil {
		t.Fatalf("Register: %v", err)
	}

	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	body := w.Body.String()
	if !strings.Contains(body, "W-Monitor") {
		t.Errorf("expected dashboard html to contain 'W-Monitor'")
	}
	if !strings.Contains(body, "minDiskFree") {
		t.Errorf("expected dashboard html to use 'minDiskFree'")
	}
	if !strings.Contains(body, "minRecordedIOPS") {
		t.Errorf("expected dashboard html to use 'minRecordedIOPS'")
	}
	if !strings.Contains(body, "<link rel=\"icon\"") {
		t.Errorf("expected dashboard html to have favicon link")
	}
}
