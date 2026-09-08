package agent_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"Zeus/agent"
	"Zeus/internal/testisolate"
)

func TestSaveCredentialsRejectsSymlink(t *testing.T) {
	credDir, _ := testisolate.Dirs(t)
	target := filepath.Join(credDir, "decoy")
	if err := os.WriteFile(target, []byte("decoy"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(credDir, tokenFileName())
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink not available: %v", err)
	}
	err := agent.SaveCredentials(agent.StoredCredentials{
		Token:      "wma_symlink_probe",
		TenantID:   "t_sym",
		ServerID:   "srv-sym",
		HubURL:     "https://hub.example",
		EnrolledAt: time.Now(),
	})
	if err == nil {
		t.Fatal("SaveCredentials must refuse a symlink token path")
	}
}

func TestChangedHubDoesNotCompareEqual(t *testing.T) {
	if agent.SameHubOrigin("https://old.example", "https://new.example") {
		t.Fatal("different hubs must not compare equal")
	}
	if !agent.SameHubOrigin("https://hub.example/", "https://hub.example") {
		t.Fatal("trailing slash must still match")
	}
}
