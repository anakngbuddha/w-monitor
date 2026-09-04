package agent_test

import (
	"testing"
	"time"

	"Zeus/agent"
)

func TestCredentialsSaveLoadClear(t *testing.T) {
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
	defer agent.ClearCredentials()

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
