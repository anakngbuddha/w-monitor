package storage

import (
	"errors"
	"sync"
	"testing"
	"time"
)

func seedEnrollCode(t *testing.T, db *DB, tenant, name string, maxUses int) (code, hash string) {
	t.Helper()
	code, err := GenerateEnrollCode()
	if err != nil {
		t.Fatal(err)
	}
	hash = HashEnrollCode(code)
	if err := db.UpsertAPIKey(APIKeyRecord{
		TenantID:   tenant,
		ClientName: name,
		KeyHash:    hash,
		KeyPrefix:  "wme_",
		Kind:       KindEnroll,
		Scope:      ScopeEnroll,
		MaxUses:    maxUses,
		ExpiresAt:  time.Now().Add(time.Hour),
		CreatedAt:  time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	return code, hash
}

func issueReq(t *testing.T, codeHash, serverID, currentHash string) EnrollmentRequest {
	t.Helper()
	tok, err := GenerateToken(KindAgent)
	if err != nil {
		t.Fatal(err)
	}
	return EnrollmentRequest{
		CodeHash:         codeHash,
		ServerID:         serverID,
		CurrentTokenHash: currentHash,
		Token:            tok,
		TokenHash:        HashAPIKey(tok),
		TokenPrefix:      ExtractKeyPrefix(tok),
	}
}

func TestCompleteEnrollmentLostReplyRecoversSameToken(t *testing.T) {
	db := testDB(t)
	_, codeHash := seedEnrollCode(t, db, "t_hs", "HSClient", 5)

	first, err := db.CompleteEnrollment(issueReq(t, codeHash, "srv-hs", ""))
	if err != nil {
		t.Fatal(err)
	}
	second, err := db.CompleteEnrollment(issueReq(t, codeHash, "srv-hs", ""))
	if err != nil {
		t.Fatal(err)
	}
	if !second.Recovered {
		t.Fatal("second enroll should recover the handshake")
	}
	if first.Token != second.Token {
		t.Fatalf("lost reply issued a new token")
	}
	rec, err := db.LookupAPIKey(codeHash)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Uses != 1 {
		t.Fatalf("uses = %d, want 1 (recovery must not consume again)", rec.Uses)
	}
}

func TestCompleteEnrollmentReplacementRequiresProof(t *testing.T) {
	db := testDB(t)
	_, codeHash := seedEnrollCode(t, db, "t_rot", "RotClient", 5)

	first, err := db.CompleteEnrollment(issueReq(t, codeHash, "srv-rot", ""))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.ExpireEnrollmentHandshakes(); err != nil {
		t.Fatal(err)
	}

	_, err = db.CompleteEnrollment(issueReq(t, codeHash, "srv-rot", ""))
	if !errors.Is(err, ErrRotationRequiresProof) {
		t.Fatalf("got %v, want ErrRotationRequiresProof", err)
	}

	rotated, err := db.CompleteEnrollment(issueReq(t, codeHash, "srv-rot", first.TokenHash))
	if err != nil {
		t.Fatal(err)
	}
	if rotated.Token == first.Token {
		t.Fatal("rotation returned the old token")
	}
	if _, err := db.ResolveAPIKey(first.TokenHash); !errors.Is(err, ErrAPIKeyNotFound) {
		t.Fatalf("old token still resolvable: %v", err)
	}
	if _, err := db.ResolveAPIKey(rotated.TokenHash); err != nil {
		t.Fatalf("new token rejected: %v", err)
	}
}

func TestCompleteEnrollmentFailedInsertDoesNotConsume(t *testing.T) {
	db := testDB(t)
	_, codeHash := seedEnrollCode(t, db, "t_fail", "FailClient", 3)

	tok, err := GenerateToken(KindAgent)
	if err != nil {
		t.Fatal(err)
	}
	// Collide with the enroll-code hash so INSERT fails uniqueness.
	_, err = db.CompleteEnrollment(EnrollmentRequest{
		CodeHash:    codeHash,
		ServerID:    "srv-fail",
		Token:       tok,
		TokenHash:   codeHash,
		TokenPrefix: "wma_",
	})
	if err == nil {
		t.Fatal("expected insert failure")
	}

	rec, err := db.LookupAPIKey(codeHash)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Uses != 0 {
		t.Fatalf("failed insert burned a use: uses=%d", rec.Uses)
	}
	agents, err := db.ListAgents("t_fail")
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range agents {
		if a.ServerID == "srv-fail" && !a.Revoked {
			t.Fatal("failed insert left an active agent")
		}
	}
}

func TestCompleteEnrollmentConcurrentSameIdentity(t *testing.T) {
	db := testDB(t)
	_, codeHash := seedEnrollCode(t, db, "t_race", "RaceClient", 20)

	const n = 8
	var wg sync.WaitGroup
	wg.Add(n)
	errCh := make(chan error, n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			_, err := db.CompleteEnrollment(issueReq(t, codeHash, "srv-race", ""))
			errCh <- err
		}()
	}
	wg.Wait()
	close(errCh)

	var ok, denied, conflict int
	for err := range errCh {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, ErrRotationRequiresProof), errors.Is(err, ErrEnrollmentConflict):
			denied++
		default:
			t.Errorf("unexpected error: %v", err)
		}
	}
	if ok < 1 {
		t.Fatal("no enrollment succeeded")
	}
	agents, err := db.ListAgents("t_race")
	if err != nil {
		t.Fatal(err)
	}
	active := 0
	for _, a := range agents {
		if a.ServerID == "srv-race" && !a.Revoked {
			active++
		}
	}
	if active != 1 {
		t.Fatalf("active agents = %d, want 1 (ok=%d denied=%d conflict=%d)", active, ok, denied, conflict)
	}
}

func TestReplaceAgentOperatorPath(t *testing.T) {
	db := testDB(t)
	_, codeHash := seedEnrollCode(t, db, "t_op", "OpClient", 2)
	first, err := db.CompleteEnrollment(issueReq(t, codeHash, "srv-op", ""))
	if err != nil {
		t.Fatal(err)
	}
	tok, err := GenerateToken(KindAgent)
	if err != nil {
		t.Fatal(err)
	}
	replaced, err := db.ReplaceAgent("t_op", "srv-op", tok, HashAPIKey(tok), ExtractKeyPrefix(tok))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ResolveAPIKey(first.TokenHash); !errors.Is(err, ErrAPIKeyNotFound) {
		t.Fatalf("old token still active: %v", err)
	}
	if replaced.Token != tok {
		t.Fatal("replace did not return issued token")
	}
}

func TestAuthEpochBumpsOnRevoke(t *testing.T) {
	db := testDB(t)
	before, err := db.AuthEpoch()
	if err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertAPIKey(APIKeyRecord{
		TenantID: "t_ep", ClientName: "Ep", KeyHash: HashAPIKey("k"), Kind: KindAgent, Scope: ScopeIngest, ServerID: "s1",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.RevokeAgent("t_ep", "s1"); err != nil {
		t.Fatal(err)
	}
	after, err := db.AuthEpoch()
	if err != nil {
		t.Fatal(err)
	}
	if after <= before {
		t.Fatalf("epoch did not bump: before=%d after=%d", before, after)
	}
}
