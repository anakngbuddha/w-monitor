package agent

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"Zeus/internal/fsroot"
)

// ErrNoCredentials is returned when no stored credentials exist on disk.
var ErrNoCredentials = errors.New("agent: no stored credentials found")

// EnvCredentialDir overrides the directory that holds the machine token file.
// Tests and disposable runners should set this (or call SetCredentialDir) rather
// than writing to OS-default credential paths.
const EnvCredentialDir = "WMONITOR_CREDENTIAL_DIR"

// StoredCredentials contains the machine's enrollment record.
type StoredCredentials struct {
	Token      string    `json:"token"`
	TenantID   string    `json:"tenant_id"`
	ServerID   string    `json:"server_id"`
	HubURL     string    `json:"hub_url"`
	EnrolledAt time.Time `json:"enrolled_at"`
}

var (
	credDirMu       sync.RWMutex
	credDirOverride string
)

func init() {
	fsroot.Register(DefaultCredentialDir())
}

// SetCredentialDir injects the credential directory. Empty restores OS default
// resolution. Production paths are rejected so tests cannot target them.
func SetCredentialDir(dir string) error {
	if dir != "" {
		if err := fsroot.RejectProductionPath(dir); err != nil {
			return err
		}
	}
	credDirMu.Lock()
	credDirOverride = dir
	credDirMu.Unlock()
	return nil
}

// CredentialDir returns the injected credential directory, or empty if unset.
func CredentialDir() string {
	credDirMu.RLock()
	defer credDirMu.RUnlock()
	return credDirOverride
}

// DefaultCredentialDir is the OS-default directory for the machine token.
// It does not create the directory.
func DefaultCredentialDir() string {
	return filepath.Dir(defaultTokenFilePath())
}

func tokenFileName() string {
	if runtime.GOOS == "windows" {
		return "token.dat"
	}
	return "token.json"
}

// tokenFilePath resolves the token file. Directory creation happens only on save.
func tokenFilePath() string {
	credDirMu.RLock()
	override := credDirOverride
	credDirMu.RUnlock()
	if override != "" {
		return filepath.Join(override, tokenFileName())
	}
	if dir := os.Getenv(EnvCredentialDir); dir != "" {
		if fsroot.IsolationEnabled() {
			if err := fsroot.RejectProductionPath(dir); err != nil {
				panic("agent: " + err.Error())
			}
		}
		return filepath.Join(dir, tokenFileName())
	}
	path := defaultTokenFilePath()
	if fsroot.IsolationEnabled() {
		panic(fmt.Sprintf("agent: production credential path %s used without test override", path))
	}
	return path
}

func ensureCredentialDir(path string) error {
	dir := filepath.Dir(path)
	if fsroot.IsolationEnabled() {
		if err := fsroot.RejectProductionPath(dir); err != nil {
			return err
		}
	}
	if err := rejectSymlink(dir); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create token dir: %w", err)
	}
	if err := rejectSymlink(dir); err != nil {
		return err
	}
	return nil
}

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

// atomicWriteFile writes data to path via a same-directory temp file + rename.
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
