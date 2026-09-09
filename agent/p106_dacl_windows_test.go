//go:build windows

package agent_test

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"Zeus/agent"
	"Zeus/internal/testisolate"

	"golang.org/x/sys/windows"
)

func TestCredentialFileDACLOmitsUsersAndEveryone(t *testing.T) {
	credDir, _ := testisolate.Dirs(t)
	store, err := agent.NewCredentialStore(credDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(agent.StoredCredentials{
		Token:      "wma_dacl_probe",
		TenantID:   "t_dacl",
		ServerID:   "srv-dacl",
		HubURL:     "https://hub.example",
		EnrolledAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(credDir, "token.dat")
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatalf("GetNamedSecurityInfo: %v", err)
	}
	sddl := sd.String()
	if !strings.Contains(sddl, "SY") || !strings.Contains(sddl, "BA") {
		t.Fatalf("DACL must grant SYSTEM and Administrators, got %s", sddl)
	}
	if strings.Contains(sddl, ";;;WD)") || strings.Contains(sddl, ";;;BU)") {
		t.Fatalf("DACL must not grant Everyone/Users, got %s", sddl)
	}
	if _, err := store.Load(); err != nil {
		t.Fatalf("current user must still load credentials: %v", err)
	}
}
