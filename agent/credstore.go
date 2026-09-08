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

// rejectSymlink refuses to operate through a symlinked path — defends against
// a symlink planted at the credential path before the file/directory exists.
func rejectSymlink(path string) error {
	fi, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("agent: refusing symlink path %s", path)
	}
	return nil
}

// atomicWriteFile writes data to path via a same-directory temp file + rename,
// so a crash or concurrent read never observes a half-written credential file.
func atomicWriteFile(path string, data []byte, perm os.FileMode) error {
	if err := rejectSymlink(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	dir := filepath.Dir(path)
	if err := rejectSymlink(dir); err != nil && !os.IsNotExist(err) {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".wmon-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpName := tmp.Name()
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(tmpName)
		}
	}()
	_ = tmp.Chmod(perm)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename temp file: %w", err)
	}
	ok = true
	_ = os.Chmod(path, perm)
	return nil
}
