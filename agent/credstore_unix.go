//go:build !windows

package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// defaultTokenFilePath returns the OS-default token path without creating it.
func defaultTokenFilePath() string {
	if os.Geteuid() == 0 {
		return filepath.Join("/etc/wmonitor", "token.json")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		home = "/tmp"
	}
	return filepath.Join(home, ".local", "share", "sysmon", "token.json")
}

// LoadCredentials loads stored agent credentials from a 0600 file.
func LoadCredentials() (*StoredCredentials, error) {
	path := tokenFilePath()
	if err := rejectSymlink(path); err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNoCredentials
		}
		return nil, err
	}
	fi, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNoCredentials
		}
		return nil, fmt.Errorf("stat token file: %w", err)
	}

	// Reject if permissions allow group or world read/write
	if fi.Mode().Perm()&0077 != 0 {
		return nil, fmt.Errorf("insecure permissions on token file %s (%o, must be 0600)", path, fi.Mode().Perm())
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

// SaveCredentials writes agent credentials to disk with strict 0600 permissions.
func SaveCredentials(creds StoredCredentials) error {
	data, err := json.MarshalIndent(creds, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal credentials: %w", err)
	}

	path := tokenFilePath()
	if err := ensureCredentialDir(path); err != nil {
		return err
	}
	if err := atomicWriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("write token file: %w", err)
	}
	_ = os.Chmod(path, 0600)
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

// ClearCredentials removes stored credentials from disk.
func ClearCredentials() error {
	path := tokenFilePath()
	err := os.Remove(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
