// Package testisolate injects credential/data/spool roots for tests (P1.01 / V22).
package testisolate

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"Zeus/agent"
	"Zeus/internal/fsroot"
	"Zeus/storage"
)

// Run wraps TestMain for external test packages (package foo_test).
func Run(m *testing.M) int {
	suite, err := fsroot.BeginSuite()
	if err != nil {
		fmt.Fprintf(os.Stderr, "testisolate: %v\n", err)
		return 1
	}
	cred := filepath.Join(suite.FixtureRoot, "credentials")
	data := filepath.Join(suite.FixtureRoot, "data")
	if err := os.MkdirAll(cred, 0o700); err != nil {
		fmt.Fprintf(os.Stderr, "testisolate: cred dir: %v\n", err)
		return 1
	}
	if err := os.MkdirAll(data, 0o700); err != nil {
		fmt.Fprintf(os.Stderr, "testisolate: data dir: %v\n", err)
		return 1
	}
	if err := agent.SetCredentialDir(cred); err != nil {
		fmt.Fprintf(os.Stderr, "testisolate: SetCredentialDir: %v\n", err)
		return 1
	}
	if err := storage.SetDataDir(data); err != nil {
		fmt.Fprintf(os.Stderr, "testisolate: SetDataDir: %v\n", err)
		return 1
	}
	return suite.Finish(m.Run())
}

// Dirs returns unique credential and data directories under t.TempDir(),
// installs them as process overrides, and restores the previous overrides.
func Dirs(t *testing.T) (credDir, dataDir string) {
	t.Helper()
	root := t.TempDir()
	if err := fsroot.RejectProductionPath(root); err != nil {
		t.Fatal(err)
	}
	credDir = filepath.Join(root, "credentials")
	dataDir = filepath.Join(root, "data")
	if err := os.MkdirAll(credDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}

	prevCred := agent.CredentialDir()
	prevData := storage.DataDirOverride()
	if err := agent.SetCredentialDir(credDir); err != nil {
		t.Fatal(err)
	}
	if err := storage.SetDataDir(dataDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = agent.SetCredentialDir(prevCred)
		_ = storage.SetDataDir(prevData)
	})
	return credDir, dataDir
}
