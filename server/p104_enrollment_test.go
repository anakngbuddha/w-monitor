package server_test

import (
	"encoding/json"
	"net/http"
	"os"
	"sync"
	"testing"
	"time"

	"Zeus/server"
	"Zeus/storage"
)

func TestP104ConcurrentEnrollOneActiveToken(t *testing.T) {
	os.Setenv("WMONITOR_ALLOW_INSECURE_ENROLL", "1")
	t.Cleanup(func() { os.Unsetenv("WMONITOR_ALLOW_INSECURE_ENROLL") })

	srv, db, _ := hubEnrollFixture(t)
	code, _ := storage.GenerateEnrollCode()
	if err := db.UpsertAPIKey(storage.APIKeyRecord{
		TenantID: "t_p104", ClientName: "P104", KeyHash: storage.HashEnrollCode(code),
		KeyPrefix: "wme_", Kind: storage.KindEnroll, Scope: storage.ScopeEnroll,
		MaxUses: 25, ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	body, _ := json.Marshal(map[string]string{"enroll_code": code, "server_id": "srv-p104"})
	const n = 8
	var wg sync.WaitGroup
	wg.Add(n)
	codes := make([]int, n)
	tokens := make([]string, n)
	for i := 0; i < n; i++ {
		i := i
		go func() {
			defer wg.Done()
			rec := do(srv, "POST", "/api/enroll", "", body)
			codes[i] = rec.Code
			var resp struct{ Token string }
			_ = json.Unmarshal(rec.Body.Bytes(), &resp)
			tokens[i] = resp.Token
		}()
	}
	wg.Wait()

	var created int
	uniq := map[string]struct{}{}
	for i, c := range codes {
		if c == http.StatusCreated {
			created++
			if tokens[i] != "" {
				uniq[tokens[i]] = struct{}{}
			}
		}
	}
	if created < 1 {
		t.Fatalf("no enroll succeeded: %v", codes)
	}
	// Lost-reply recovery can return the same token; distinct active hashes must still be 1.
	agents, err := db.ListAgents("t_p104")
	if err != nil {
		t.Fatal(err)
	}
	active := 0
	for _, a := range agents {
		if a.ServerID == "srv-p104" && !a.Revoked {
			active++
		}
	}
	if active != 1 {
		t.Fatalf("active agents = %d created_http=%d distinct_tokens=%d", active, created, len(uniq))
	}
}

func TestP104WarmedCacheReplicaRevoke(t *testing.T) {
	os.Setenv("WMONITOR_ALLOW_INSECURE_ENROLL", "1")
	t.Cleanup(func() { os.Unsetenv("WMONITOR_ALLOW_INSECURE_ENROLL") })

	srv, db, admin := hubEnrollFixture(t)
	srv.SetAuthEpochInterval(0)

	readTok, _ := storage.GenerateToken(storage.KindRead)
	if err := db.UpsertAPIKey(storage.APIKeyRecord{
		TenantID: "t_cache", ClientName: "CacheCo", KeyHash: storage.HashAPIKey(readTok),
		KeyPrefix: "wmr_", Kind: storage.KindRead, Scope: storage.ScopeRead,
	}); err != nil {
		t.Fatal(err)
	}

	replica := server.New(db, "0")
	replica.EnableHubMode(db)
	replica.SetAuthEpochInterval(0)

	if w := do(srv, "GET", "/api/servers", readTok, nil); w.Code != http.StatusOK {
		t.Fatalf("warm primary: %d %s", w.Code, w.Body.String())
	}
	if w := do(replica, "GET", "/api/servers", readTok, nil); w.Code != http.StatusOK {
		t.Fatalf("warm replica: %d %s", w.Code, w.Body.String())
	}

	if _, err := db.RevokeAPIKey("CacheCo"); err != nil {
		t.Fatal(err)
	}
	replica.SyncAuthEpoch()
	srv.SyncAuthEpoch()

	if w := do(replica, "GET", "/api/servers", readTok, nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("replica accepted revoked key: %d", w.Code)
	}
	if w := do(srv, "GET", "/api/servers", readTok, nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("primary accepted revoked key: %d", w.Code)
	}
	_ = admin
}

func TestP104OperatorRotateInvalidatesOldToken(t *testing.T) {
	os.Setenv("WMONITOR_ALLOW_INSECURE_ENROLL", "1")
	t.Cleanup(func() { os.Unsetenv("WMONITOR_ALLOW_INSECURE_ENROLL") })

	srv, _, admin := hubEnrollFixture(t)
	codeBody, _ := json.Marshal(map[string]interface{}{"client_name": "RotCo", "max_uses": 2, "ttl_hours": 1})
	rec := do(srv, "POST", "/api/admin/enroll-codes", admin, codeBody)
	if rec.Code != http.StatusCreated {
		t.Fatalf("code: %d %s", rec.Code, rec.Body.String())
	}
	var adminResp struct {
		Code     string `json:"code"`
		TenantID string `json:"tenant_id"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &adminResp)

	enrollBody, _ := json.Marshal(map[string]string{"enroll_code": adminResp.Code, "server_id": "srv-op"})
	enrollRec := do(srv, "POST", "/api/enroll", "", enrollBody)
	if enrollRec.Code != http.StatusCreated {
		t.Fatalf("enroll: %d %s", enrollRec.Code, enrollRec.Body.String())
	}
	var agent struct{ Token string }
	_ = json.Unmarshal(enrollRec.Body.Bytes(), &agent)

	rotBody, _ := json.Marshal(map[string]string{"server_id": "srv-op", "tenant_id": adminResp.TenantID})
	rot := do(srv, "POST", "/api/admin/agents/rotate", admin, rotBody)
	if rot.Code != http.StatusCreated {
		t.Fatalf("rotate: %d %s", rot.Code, rot.Body.String())
	}
	var rotated struct{ Token string }
	_ = json.Unmarshal(rot.Body.Bytes(), &rotated)

	metricBody, _ := json.Marshal(storage.MetricRow{ServerID: "srv-op", Timestamp: time.Now()})
	if w := do(srv, "POST", "/api/ingest?type=metric", agent.Token, metricBody); w.Code != http.StatusUnauthorized {
		t.Fatalf("old token after operator rotate: %d", w.Code)
	}
	if w := do(srv, "POST", "/api/ingest?type=metric", rotated.Token, metricBody); w.Code != http.StatusAccepted {
		t.Fatalf("new token after operator rotate: %d %s", w.Code, w.Body.String())
	}
}

func TestP104ExpiredCredentialNotCachedPastExpiry(t *testing.T) {
	srv, db, _ := hubEnrollFixture(t)
	tok, _ := storage.GenerateToken(storage.KindRead)
	if err := db.UpsertAPIKey(storage.APIKeyRecord{
		TenantID: "t_exp", ClientName: "ExpCo", KeyHash: storage.HashAPIKey(tok),
		KeyPrefix: "wmr_", Kind: storage.KindRead, Scope: storage.ScopeRead,
		ExpiresAt: time.Now().Add(2 * time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	if w := do(srv, "GET", "/api/servers", tok, nil); w.Code != http.StatusOK {
		t.Fatalf("before expiry: %d %s", w.Code, w.Body.String())
	}
	time.Sleep(3 * time.Second)
	if w := do(srv, "GET", "/api/servers", tok, nil); w.Code != http.StatusUnauthorized {
		t.Fatalf("after expiry warmed cache: %d", w.Code)
	}
}
