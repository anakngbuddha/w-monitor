//go:build !windows

package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

func tokenFilePath() string {
	// If root, use /etc/wmonitor/token.json
	if os.Geteuid() == 0 {
		dir := "/etc/wmonitor"
		_ = os.MkdirAll(dir, 0700)
		return filepath.Join(dir, "token.json")
	}

	// User-level fallback: ~/.local/share/sysmon/token.json
	home, err := os.UserHomeDir()
	if err != nil {
		home = "/tmp"
	}
	dir := filepath.Join(home, ".local", "share", "sysmon")
	_ = os.MkdirAll(dir, 0700)
	return filepath.Join(dir, "token.json")
}

// LoadCredentials loads stored agent credentials from a 0600 file.
func LoadCredentials() (*StoredCredentials, error) {
	path := tokenFilePath()
	fi, err := os.Stat(path)
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
	if err := os.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("write token file: %w", err)
	}
	// Re-enforce 0600 in case umask modified it
	_ = os.Chmod(path, 0600)
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
