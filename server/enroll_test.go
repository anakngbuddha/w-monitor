package server_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"Zeus/server"
	"Zeus/storage"
)

func hubEnrollFixture(t *testing.T) (*server.Server, *storage.DB, string) {
	t.Helper()
	db, err := storage.Open(filepath.Join(t.TempDir(), "enroll_test.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	srv := server.New(db, "0")
	srv.EnableHubMode(db)

	adminToken, err := storage.GenerateToken(storage.KindAdmin)
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}
	if err := db.UpsertAPIKey(storage.APIKeyRecord{
		TenantID:   "t_admin",
		ClientName: "AdminClient",
		KeyHash:    storage.HashAPIKey(adminToken),
		KeyPrefix:  "wmk_",
		Kind:       storage.KindAdmin,
		Scope:      storage.ScopeAdmin,
	}); err != nil {
		t.Fatalf("UpsertAPIKey: %v", err)
	}

	return srv, db, adminToken
}

func TestEnrollHandshakeAndScopeEnforcement(t *testing.T) {
	os.Setenv("WMONITOR_ALLOW_INSECURE_ENROLL", "1")
	defer os.Unsetenv("WMONITOR_ALLOW_INSECURE_ENROLL")

	srv, db, adminToken := hubEnrollFixture(t)

	// 1. Create an enrollment code via admin endpoint
	reqBody, _ := json.Marshal(map[string]interface{}{
		"client_name": "AcmeCorp",
		"max_uses":    2,
		"ttl_hours":   24,
	})
	rec := do(srv, "POST", "/api/admin/enroll-codes", adminToken, reqBody)
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201 for admin enroll code, got %d: %s", rec.Code, rec.Body.String())
	}
	var adminResp struct {
		Code       string `json:"code"`
		ClientName string `json:"client_name"`
		TenantID   string `json:"tenant_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &adminResp); err != nil {
		t.Fatalf("unmarshal admin response: %v", err)
	}
	if adminResp.Code == "" || adminResp.TenantID == "" {
		t.Fatalf("invalid admin response: %+v", adminResp)
	}

	// 2. Machine enrolls using the code
	enrollPayload, _ := json.Marshal(map[string]string{
		"enroll_code": adminResp.Code,
		"server_id":   "srv-app-01",
		"hostname":    "web01",
		"os":          "linux/amd64",
		"version":     "1.4.2",
	})
	enrollRec := do(srv, "POST", "/api/enroll", "", enrollPayload)
	if enrollRec.Code != http.StatusCreated {
		t.Fatalf("expected 201 for enrollment, got %d: %s", enrollRec.Code, enrollRec.Body.String())
	}

	var agentResp struct {
		Token    string `json:"token"`
		TenantID string `json:"tenant_id"`
		ServerID string `json:"server_id"`
	}
	if err := json.Unmarshal(enrollRec.Body.Bytes(), &agentResp); err != nil {
		t.Fatalf("unmarshal enroll response: %v", err)
	}
	if agentResp.Token == "" || agentResp.ServerID != "srv-app-01" {
		t.Fatalf("invalid agent token response: %+v", agentResp)
	}

	// 3. Agent token can POST to /api/ingest
	metricBody, _ := json.Marshal(storage.MetricRow{
		Timestamp:    time.Now(),
		ServerID:     agentResp.ServerID,
		Hostname:     "web01",
		CPUPct:       12.5,
		MemPct:       45.0,
		DiskFreeGB:   100.0,
		NetSentBytes: 1024,
		NetRecvBytes: 2048,
	})
	ingestRec := do(srv, "POST", "/api/ingest?type=metric", agentResp.Token, metricBody)
	if ingestRec.Code != http.StatusAccepted {
		t.Fatalf("agent token ingest failed: code %d: %s", ingestRec.Code, ingestRec.Body.String())
	}

	// 4. Agent token CANNOT read /api/metrics (must get 403 Forbidden)
	readRec := do(srv, "GET", "/api/metrics", agentResp.Token, nil)
	if readRec.Code != http.StatusForbidden {
		t.Errorf("agent token read /api/metrics expected 403 Forbidden, got %d", readRec.Code)
	}

	// 5. Create a read token for AcmeCorp
	readToken, _ := storage.GenerateToken(storage.KindRead)
	if err := db.UpsertAPIKey(storage.APIKeyRecord{
		TenantID:   adminResp.TenantID,
		ClientName: "AcmeCorp",
		KeyHash:    storage.HashAPIKey(readToken),
		KeyPrefix:  "wmr_",
		Kind:       storage.KindRead,
		Scope:      storage.ScopeRead,
	}); err != nil {
		t.Fatalf("UpsertAPIKey read token: %v", err)
	}

	// 6. Read token CAN read /api/metrics
	readOkRec := do(srv, "GET", "/api/metrics", readToken, nil)
	if readOkRec.Code != http.StatusOK {
		t.Errorf("read token GET /api/metrics expected 200, got %d", readOkRec.Code)
	}

	// 7. Read token CANNOT post to /api/ingest (must get 403 Forbidden)
	ingestForbiddenRec := do(srv, "POST", "/api/ingest?type=metric", readToken, metricBody)
	if ingestForbiddenRec.Code != http.StatusForbidden {
		t.Errorf("read token POST /api/ingest expected 403 Forbidden, got %d", ingestForbiddenRec.Code)
	}
}

func TestEnrollIdempotentRotation(t *testing.T) {
	os.Setenv("WMONITOR_ALLOW_INSECURE_ENROLL", "1")
	defer os.Unsetenv("WMONITOR_ALLOW_INSECURE_ENROLL")

	srv, db, _ := hubEnrollFixture(t)

	code, _ := storage.GenerateEnrollCode()
	db.UpsertAPIKey(storage.APIKeyRecord{
		TenantID:   "t_rotate_test",
		ClientName: "RotateClient",
		KeyHash:    storage.HashAPIKey(code),
		KeyPrefix:  "wme_",
		Kind:       storage.KindEnroll,
		Scope:      storage.ScopeIngest,
		MaxUses:    5,
		ExpiresAt:  time.Now().Add(24 * time.Hour),
	})

	enrollPayload, _ := json.Marshal(map[string]string{
		"enroll_code": code,
		"server_id":   "srv-unique-01",
		"hostname":    "host1",
	})

	// Run 1: first token issued
	rec1 := do(srv, "POST", "/api/enroll", "", enrollPayload)
	if rec1.Code != http.StatusCreated {
		t.Fatalf("enroll 1 failed: %d", rec1.Code)
	}
	var resp1 struct{ Token string }
	json.Unmarshal(rec1.Body.Bytes(), &resp1)

	// Run 2: re-enroll with same server_id rotates old token
	rec2 := do(srv, "POST", "/api/enroll", "", enrollPayload)
	if rec2.Code != http.StatusCreated {
		t.Fatalf("enroll 2 failed: %d", rec2.Code)
	}
	var resp2 struct{ Token string }
	json.Unmarshal(rec2.Body.Bytes(), &resp2)

	if resp1.Token == resp2.Token {
		t.Errorf("re-enrollment returned identical token, expected new rotated token")
	}

	// Old token should be revoked (returns 401)
	metricBody, _ := json.Marshal(storage.MetricRow{ServerID: "srv-unique-01", Timestamp: time.Now()})
	checkOld := do(srv, "POST", "/api/ingest?type=metric", resp1.Token, metricBody)
	if checkOld.Code != http.StatusUnauthorized {
		t.Errorf("old token still accepted after rotation: got %d, want 401", checkOld.Code)
	}

	// New token works
	checkNew := do(srv, "POST", "/api/ingest?type=metric", resp2.Token, metricBody)
	if checkNew.Code != http.StatusAccepted {
		t.Errorf("new token rejected: got %d, want 202", checkNew.Code)
	}
}

func TestSessionLoginAndCookieAuth(t *testing.T) {
	srv, db, _ := hubEnrollFixture(t)

	readToken, _ := storage.GenerateToken(storage.KindRead)
	db.UpsertAPIKey(storage.APIKeyRecord{
		TenantID:   "t_session_test",
		ClientName: "SessionClient",
		KeyHash:    storage.HashAPIKey(readToken),
		KeyPrefix:  "wmr_",
		Kind:       storage.KindRead,
		Scope:      storage.ScopeRead,
	})

	// Login
	loginBody, _ := json.Marshal(map[string]string{"read_token": readToken})
	loginReq := httptest.NewRequest("POST", "/api/session", bytes.NewReader(loginBody))
	loginReq.Header.Set("Content-Type", "application/json")
	loginW := httptest.NewRecorder()
	srv.Handler().ServeHTTP(loginW, loginReq)

	if loginW.Code != http.StatusOK {
		t.Fatalf("login failed: %d: %s", loginW.Code, loginW.Body.String())
	}

	cookies := loginW.Result().Cookies()
	var sessionCookie *http.Cookie
	for _, c := range cookies {
		if c.Name == "wmonitor_session" {
			sessionCookie = c
			break
		}
	}
	if sessionCookie == nil {
		t.Fatalf("no wmonitor_session cookie in response")
	}
	if !sessionCookie.HttpOnly {
		t.Errorf("cookie is not HttpOnly")
	}

	// Authenticate request using session cookie
	req := httptest.NewRequest("GET", "/api/metrics", nil)
	req.AddCookie(sessionCookie)
	w := httptest.NewRecorder()
	srv.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("session cookie GET /api/metrics failed: code %d, body %s", w.Code, w.Body.String())
	}

	// Logout
	logoutReq := httptest.NewRequest("DELETE", "/api/session", nil)
	logoutW := httptest.NewRecorder()
	srv.Handler().ServeHTTP(logoutW, logoutReq)

	if logoutW.Code != http.StatusOK {
		t.Errorf("logout failed: %d", logoutW.Code)
	}
	logoutCookies := logoutW.Result().Cookies()
	if len(logoutCookies) == 0 || logoutCookies[0].MaxAge != -1 {
		t.Errorf("logout did not clear cookie")
	}
}
