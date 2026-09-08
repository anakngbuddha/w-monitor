package agent_test

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"Zeus/agent"
	"Zeus/internal/fsroot"
	"Zeus/internal/testisolate"
)

func TestCredentialsSaveLoadClear(t *testing.T) {
	credDir, _ := testisolate.Dirs(t)

	creds := agent.StoredCredentials{
		Token:      "wma_test_token_12345",
		TenantID:   "t_test_tenant",
		ServerID:   "srv-test-01",
		HubURL:     "https://hub.example.com",
		EnrolledAt: time.Now().Truncate(time.Second),
	}

	if err := agent.SaveCredentials(creds); err != nil {
		t.Fatalf("SaveCredentials failed: %v", err)
	}

	tokenPath := filepath.Join(credDir, tokenFileName())
	if fsroot.IsProductionPath(tokenPath) {
		t.Fatalf("token written to production path %s", tokenPath)
	}

	loaded, err := agent.LoadCredentials()
	if err != nil {
		t.Fatalf("LoadCredentials failed: %v", err)
	}

	if loaded.Token != creds.Token {
		t.Errorf("loaded.Token = %s, want %s", loaded.Token, creds.Token)
	}
	if loaded.TenantID != creds.TenantID {
		t.Errorf("loaded.TenantID = %s, want %s", loaded.TenantID, creds.TenantID)
	}
	if loaded.ServerID != creds.ServerID {
		t.Errorf("loaded.ServerID = %s, want %s", loaded.ServerID, creds.ServerID)
	}
	if loaded.HubURL != creds.HubURL {
		t.Errorf("loaded.HubURL = %s, want %s", loaded.HubURL, creds.HubURL)
	}

	if err := agent.ClearCredentials(); err != nil {
		t.Fatalf("ClearCredentials failed: %v", err)
	}

	_, err = agent.LoadCredentials()
	if err != agent.ErrNoCredentials {
		t.Errorf("expected ErrNoCredentials after clear, got %v", err)
	}
}

func TestSetCredentialDirRejectsProduction(t *testing.T) {
	for _, root := range fsroot.WellKnownProductionRoots() {
		if err := agent.SetCredentialDir(root); err == nil {
			t.Errorf("SetCredentialDir(%q) succeeded; production paths must be rejected", root)
		}
	}
}

func TestDefaultCredentialDirIsPlatformIsolatedFromFixtures(t *testing.T) {
	def := agent.DefaultCredentialDir()
	if def == "" {
		t.Fatal("DefaultCredentialDir is empty")
	}
	if !fsroot.IsProductionPath(def) {
		t.Fatalf("DefaultCredentialDir %q is not classified as production", def)
	}
	fixture := t.TempDir()
	if fsroot.IsProductionPath(fixture) {
		t.Fatalf("fixture %q classified as production", fixture)
	}

	if runtime.GOOS == "windows" {
		if filepath.Base(def) != "wmonitor" {
			t.Errorf("Windows credential dir base = %s, want wmonitor (ProgramData)", filepath.Base(def))
		}
	} else {
		base := filepath.Base(def)
		if base != "wmonitor" && base != "sysmon" {
			t.Errorf("Unix credential dir base = %s, want wmonitor or sysmon", base)
		}
	}
}

func TestCredentialFixtureDoesNotTouchProductionCanary(t *testing.T) {
	prodFiles := fsroot.ProductionSensitiveFiles()
	before, err := fsroot.SnapshotFiles(prodFiles)
	if err != nil {
		t.Fatal(err)
	}
	canaryDir, canaryFile, canarySum, err := fsroot.WriteExternalCanary()
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(canaryDir)

	testisolate.Dirs(t)
	creds := agent.StoredCredentials{
		Token:    "wma_canary_probe",
		TenantID: "t_canary",
		ServerID: "srv-canary",
		HubURL:   "https://hub.example.com",
	}
	if err := agent.SaveCredentials(creds); err != nil {
		t.Fatal(err)
	}
	if err := agent.ClearCredentials(); err != nil {
		t.Fatal(err)
	}

	if err := fsroot.VerifyUnchanged(before); err != nil {
		t.Fatalf("production files changed: %v", err)
	}
	got, err := fsroot.HashFile(canaryFile)
	if err != nil {
		t.Fatal(err)
	}
	if got != canarySum {
		t.Fatalf("external canary changed: before %s after %s", canarySum, got)
	}
}

func tokenFileName() string {
	if runtime.GOOS == "windows" {
		return "token.dat"
	}
	return "token.json"
}
