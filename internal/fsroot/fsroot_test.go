package fsroot

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestRejectsProductionRoots(t *testing.T) {
	for _, root := range WellKnownProductionRoots() {
		if err := RejectProductionPath(root); err == nil {
			t.Errorf("RejectProductionPath(%q) = nil, want error", root)
		}
		child := filepath.Join(root, "token.json")
		if err := RejectProductionPath(child); err == nil {
			t.Errorf("RejectProductionPath(%q) = nil, want error", child)
		}
	}
}

func TestAcceptsTempFixture(t *testing.T) {
	dir := t.TempDir()
	if err := RejectProductionPath(dir); err != nil {
		t.Fatalf("temp fixture rejected: %v", err)
	}
	if IsProductionPath(dir) {
		t.Fatalf("t.TempDir() %q classified as production", dir)
	}
}

func TestRejectsEmpty(t *testing.T) {
	if err := RejectProductionPath(""); err == nil {
		t.Fatal("empty path accepted")
	}
}

func TestPlatformProductionRootsPresent(t *testing.T) {
	roots := WellKnownProductionRoots()
	var want string
	if runtime.GOOS == "windows" {
		want = "wmonitor"
	} else {
		want = "wmonitor"
	}
	found := false
	for _, r := range roots {
		if filepath.Base(r) == want || filepath.Base(r) == "sysmon" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected a wmonitor/sysmon production root on %s, got %v", runtime.GOOS, roots)
	}
}

func TestCanaryRoundTrip(t *testing.T) {
	dir, file, sum, err := WriteExternalCanary()
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	got, err := HashFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if got != sum {
		t.Fatalf("canary hash %s != %s", got, sum)
	}
	snap, err := SnapshotFiles([]string{file})
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyUnchanged(snap); err != nil {
		t.Fatal(err)
	}
}

func TestSnapshotDetectsMutation(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "x")
	if err := os.WriteFile(p, []byte("a"), 0o600); err != nil {
		t.Fatal(err)
	}
	snap, err := SnapshotFiles([]string{p})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("b"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := VerifyUnchanged(snap); err == nil {
		t.Fatal("expected change detection")
	}
}
