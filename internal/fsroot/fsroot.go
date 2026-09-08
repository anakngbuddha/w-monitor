// Package fsroot identifies production credential/data/spool locations so tests
// can refuse to use them as fixtures (V22).
package fsroot

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// EnvTestIsolation, when set to "1", requires injected test roots. CI sets this
// so a forgotten fixture cannot resolve to a machine credential or data path.
const EnvTestIsolation = "WMONITOR_TEST_ISOLATION"

var (
	extraMu    sync.Mutex
	extraRoots []string
)

// IsolationEnabled reports whether tests must use injected non-production roots.
func IsolationEnabled() bool {
	return os.Getenv(EnvTestIsolation) == "1"
}

// Register records an additional production root (package init of agent/storage).
func Register(path string) {
	if strings.TrimSpace(path) == "" {
		return
	}
	extraMu.Lock()
	extraRoots = append(extraRoots, path)
	extraMu.Unlock()
}

// WellKnownProductionRoots returns OS-default credential and data directories
// without creating them.
func WellKnownProductionRoots() []string {
	var roots []string
	if runtime.GOOS == "windows" {
		programData := os.Getenv("PROGRAMDATA")
		if programData == "" {
			programData = `C:\ProgramData`
		}
		roots = append(roots,
			filepath.Join(programData, "wmonitor"),
			filepath.Join(programData, "sysmon"),
			`C:\ProgramData\wmonitor`,
			`C:\ProgramData\sysmon`,
		)
		if localApp := os.Getenv("LOCALAPPDATA"); localApp != "" {
			roots = append(roots,
				filepath.Join(localApp, "sysmon"),
				filepath.Join(localApp, "wmonitor"),
			)
		}
		if home, err := os.UserHomeDir(); err == nil {
			roots = append(roots,
				filepath.Join(home, "AppData", "Local", "sysmon"),
				filepath.Join(home, "AppData", "Local", "wmonitor"),
			)
		}
	} else {
		roots = append(roots, "/etc/wmonitor", "/etc/sysmon")
		if home, err := os.UserHomeDir(); err == nil {
			roots = append(roots,
				filepath.Join(home, ".local", "share", "sysmon"),
				filepath.Join(home, ".local", "share", "wmonitor"),
			)
		}
		roots = append(roots, "/tmp/.local/share/sysmon")
	}
	return uniquePaths(roots)
}

// AllProductionRoots is the well-known set plus any Register() calls.
func AllProductionRoots() []string {
	extraMu.Lock()
	extra := append([]string(nil), extraRoots...)
	extraMu.Unlock()
	return uniquePaths(append(WellKnownProductionRoots(), extra...))
}

// ProductionSensitiveFiles are the live files a test suite must not mutate.
func ProductionSensitiveFiles() []string {
	var files []string
	for _, root := range AllProductionRoots() {
		files = append(files,
			filepath.Join(root, "token.json"),
			filepath.Join(root, "token.dat"),
			filepath.Join(root, "config.env"),
			filepath.Join(root, "agent_id"),
			filepath.Join(root, "wmonitor.db"),
			filepath.Join(root, "sysmon.db"),
		)
	}
	return uniquePaths(files)
}

// IsProductionPath reports whether path is a production root or inside one.
func IsProductionPath(path string) bool {
	c, err := canon(path)
	if err != nil {
		return false
	}
	for _, root := range AllProductionRoots() {
		r, err := canon(root)
		if err != nil {
			continue
		}
		if c == r {
			return true
		}
		sep := string(filepath.Separator)
		if strings.HasPrefix(c, r+sep) {
			return true
		}
	}
	return false
}

// RejectProductionPath fails when a test fixture would use a live machine path.
func RejectProductionPath(path string) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("empty path is not a valid test fixture root")
	}
	if IsProductionPath(path) {
		return fmt.Errorf("refusing production path %q as a test fixture (V22)", path)
	}
	return nil
}

// FileState is a byte-identity snapshot of a path.
type FileState struct {
	Path   string
	Exists bool
	IsDir  bool
	Size   int64
	SHA256 string
}

// SnapshotFiles records existence and content hashes. Missing paths are valid.
func SnapshotFiles(paths []string) ([]FileState, error) {
	out := make([]FileState, 0, len(paths))
	for _, p := range paths {
		st := FileState{Path: p}
		fi, err := os.Lstat(p)
		if err != nil {
			if os.IsNotExist(err) {
				out = append(out, st)
				continue
			}
			return nil, fmt.Errorf("stat %s: %w", p, err)
		}
		st.Exists = true
		st.IsDir = fi.IsDir()
		st.Size = fi.Size()
		if fi.Mode()&os.ModeSymlink != 0 {
			// Record the link target name rather than following it.
			target, _ := os.Readlink(p)
			sum := sha256.Sum256([]byte(target))
			st.SHA256 = hex.EncodeToString(sum[:])
			out = append(out, st)
			continue
		}
		if !fi.IsDir() {
			sum, err := HashFile(p)
			if err != nil {
				return nil, err
			}
			st.SHA256 = sum
		}
		out = append(out, st)
	}
	return out, nil
}

// VerifyUnchanged checks that every snapshotted path is byte-for-byte identical.
func VerifyUnchanged(before []FileState) error {
	paths := make([]string, len(before))
	for i, s := range before {
		paths[i] = s.Path
	}
	after, err := SnapshotFiles(paths)
	if err != nil {
		return err
	}
	if len(after) != len(before) {
		return fmt.Errorf("snapshot length changed: got %d want %d", len(after), len(before))
	}
	for i := range before {
		b, a := before[i], after[i]
		if b.Exists != a.Exists || b.IsDir != a.IsDir || b.Size != a.Size || b.SHA256 != a.SHA256 {
			return fmt.Errorf("path %s changed: before exists=%v size=%d sha=%s; after exists=%v size=%d sha=%s",
				b.Path, b.Exists, b.Size, b.SHA256, a.Exists, a.Size, a.SHA256)
		}
	}
	return nil
}

// HashFile returns the SHA-256 hex digest of a regular file.
func HashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// WriteExternalCanary plants a file outside any test TempDir. The suite must
// leave it byte-for-byte unchanged.
func WriteExternalCanary() (dir, file, sum string, err error) {
	dir, err = os.MkdirTemp("", "wmonitor-v22-canary-")
	if err != nil {
		return "", "", "", err
	}
	if IsProductionPath(dir) {
		_ = os.RemoveAll(dir)
		return "", "", "", fmt.Errorf("external canary directory %s is a production path", dir)
	}
	file = filepath.Join(dir, "canary.token")
	payload := []byte(fmt.Sprintf("v22-canary-%d\n", time.Now().UnixNano()))
	if err := os.WriteFile(file, payload, 0o600); err != nil {
		_ = os.RemoveAll(dir)
		return "", "", "", err
	}
	sum, err = HashFile(file)
	if err != nil {
		_ = os.RemoveAll(dir)
		return "", "", "", err
	}
	return dir, file, sum, nil
}

func canon(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	abs = filepath.Clean(abs)
	if runtime.GOOS == "windows" {
		abs = strings.ToLower(abs)
	}
	return abs, nil
}

// Suite is the V22 production-canary harness used from TestMain.
type Suite struct {
	Before      []FileState
	CanaryDir   string
	CanaryFile  string
	CanarySum   string
	FixtureRoot string
}

// BeginSuite enables isolation, snapshots production files, writes an external
// canary, and creates a temporary fixture root. Call Finish after m.Run().
func BeginSuite() (*Suite, error) {
	if err := os.Setenv(EnvTestIsolation, "1"); err != nil {
		return nil, err
	}
	before, err := SnapshotFiles(ProductionSensitiveFiles())
	if err != nil {
		return nil, fmt.Errorf("snapshot production: %w", err)
	}
	canaryDir, canaryFile, canarySum, err := WriteExternalCanary()
	if err != nil {
		return nil, fmt.Errorf("canary: %w", err)
	}
	root, err := os.MkdirTemp("", "wmonitor-p101-isolate-")
	if err != nil {
		_ = os.RemoveAll(canaryDir)
		return nil, err
	}
	if err := RejectProductionPath(root); err != nil {
		_ = os.RemoveAll(canaryDir)
		_ = os.RemoveAll(root)
		return nil, err
	}
	return &Suite{
		Before:      before,
		CanaryDir:   canaryDir,
		CanaryFile:  canaryFile,
		CanarySum:   canarySum,
		FixtureRoot: root,
	}, nil
}

// Finish verifies the canary and production files, then returns code or 1.
func (s *Suite) Finish(code int) int {
	if s == nil {
		return 1
	}
	defer os.RemoveAll(s.CanaryDir)
	defer os.RemoveAll(s.FixtureRoot)
	if err := VerifyUnchanged(s.Before); err != nil {
		fmt.Fprintf(os.Stderr, "V22 FAIL production path changed during tests: %v\n", err)
		return 1
	}
	got, err := HashFile(s.CanaryFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "V22 FAIL read external canary: %v\n", err)
		return 1
	}
	if got != s.CanarySum {
		fmt.Fprintf(os.Stderr, "V22 FAIL external canary %s was modified (before %s after %s)\n", s.CanaryFile, s.CanarySum, got)
		return 1
	}
	return code
}

func uniquePaths(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	var out []string
	for _, p := range in {
		if p == "" {
			continue
		}
		c, err := canon(p)
		key := p
		if err == nil {
			key = c
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, p)
	}
	return out
}
