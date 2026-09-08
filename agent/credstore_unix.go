//go:build !windows

package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// tokenFilePath returns the OS-default token path used by the production
// compatibility entry points (LoadCredentials/SaveCredentials/ClearCredentials).
// It does not create the directory.
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
	if err := rejectSymlink(path); err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	fi, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNoCredentials
		}
		return nil, fmt.Errorf("stat token file: %w", err)
	}
	if fi.Mode().Perm()&0077 != 0 {
		return nil, fmt.Errorf("insecure permissions on token file (%o, must be 0600)", fi.Mode().Perm())
	}
	if err := checkFileOwner(fi); err != nil {
		return nil, err
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

// Save writes the explicitly selected credential file using symlink rejection,
// an atomic temp-file+rename, and a post-write ownership check.
func (s *CredentialStore) Save(creds StoredCredentials) error {
	path, err := s.filePath()
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(creds, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal credentials: %w", err)
	}
	dir := filepath.Dir(path)
	if err := rejectSymlink(dir); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create credential directory: %w", err)
	}
	if err := rejectSymlink(dir); err != nil {
		return err
	}
	if err := atomicWriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("write token file: %w", err)
	}
	fi, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("stat token file: %w", err)
	}
	if err := checkFileOwner(fi); err != nil {
		return err
	}
	return nil
}

func checkFileOwner(fi os.FileInfo) error {
	sys, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return nil
	}
	if int(sys.Uid) != os.Geteuid() {
		return fmt.Errorf("token file owned by uid %d, not current euid %d", sys.Uid, os.Geteuid())
	}
	return nil
}
