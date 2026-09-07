package agent

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

// ErrNoCredentials is returned when no stored credentials exist on disk.
var ErrNoCredentials = errors.New("agent: no stored credentials found")

// StoredCredentials contains the machine's enrollment record.
type StoredCredentials struct {
	Token      string    `json:"token"`
	TenantID   string    `json:"tenant_id"`
	ServerID   string    `json:"server_id"`
	HubURL     string    `json:"hub_url"`
	EnrolledAt time.Time `json:"enrolled_at"`
}

// CredentialStore owns one explicitly located credential file. Its path is
// immutable; creating a store does not read, create, or change any files.
// Callers must provide a trusted, absolute directory. Tests use t.TempDir().
// The zero value is invalid and never falls back to production locations.
type CredentialStore struct {
	path string
}

// NewCredentialStore selects a directory without consulting HOME, PROGRAMDATA,
// the current user, or the production credential path.
func NewCredentialStore(dir string) (*CredentialStore, error) {
	if dir == "" || !filepath.IsAbs(dir) {
		return nil, fmt.Errorf("agent: credential directory must be absolute")
	}
	name := "token.json"
	if runtime.GOOS == "windows" {
		name = "token.dat"
	}
	return &CredentialStore{path: filepath.Join(filepath.Clean(dir), name)}, nil
}

func (s *CredentialStore) filePath() (string, error) {
	if s == nil || s.path == "" || !filepath.IsAbs(s.path) {
		return "", fmt.Errorf("agent: invalid credential store")
	}
	return s.path, nil
}

// Clear removes only this store's credential file. It is safe to repeat.
func (s *CredentialStore) Clear() error {
	path, err := s.filePath()
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// LoadCredentials reads the production credential location. Tests must use an
// explicit CredentialStore instead of this compatibility entry point.
func LoadCredentials() (*StoredCredentials, error) {
	return (&CredentialStore{path: tokenFilePath()}).Load()
}

// SaveCredentials writes the production credential location.
func SaveCredentials(creds StoredCredentials) error {
	return (&CredentialStore{path: tokenFilePath()}).Save(creds)
}

// ClearCredentials removes the production credential file.
func ClearCredentials() error {
	return (&CredentialStore{path: tokenFilePath()}).Clear()
}
