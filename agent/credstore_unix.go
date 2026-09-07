//go:build !windows

package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

func tokenFilePath() string {
	if os.Geteuid() == 0 {
		return filepath.Join("/etc/wmonitor", "token.json")
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		// An invalid store fails closed; never use a shared /tmp location.
		return ""
	}
	return filepath.Join(home, ".local", "share", "sysmon", "token.json")
}

// Load reads the explicitly selected credential file.
func (s *CredentialStore) Load() (*StoredCredentials, error) {
	path, err := s.filePath()
	if err != nil {
		return nil, err
	}
	fi, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNoCredentials
		}
		return nil, fmt.Errorf("stat token file: %w", err)
	}
	if fi.Mode().Perm()&0077 != 0 {
		return nil, fmt.Errorf("insecure permissions on token file (%o, must be 0600)", fi.Mode().Perm())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read token file: %w", err)
	}
	var creds StoredCredentials
	if err := json.Unmarshal(data, &creds); err != nil {
		return nil, fmt.Errorf("unmarshal credentials: %w", err)
	}
	return &creds, nil
}

// Save writes the explicitly selected credential file. Full owner/symlink and
// atomic-write hardening is a separate Phase 1 ticket, not guaranteed here.
func (s *CredentialStore) Save(creds StoredCredentials) error {
	path, err := s.filePath()
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(creds, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal credentials: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("create credential directory: %w", err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("write token file: %w", err)
	}
	if err := os.Chmod(path, 0600); err != nil {
		return fmt.Errorf("restrict token file: %w", err)
	}
	return nil
}
