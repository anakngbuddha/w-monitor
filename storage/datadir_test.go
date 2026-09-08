package storage_test

import (
	"path/filepath"
	"runtime"
	"testing"

	"Zeus/internal/fsroot"
	"Zeus/storage"
)

func TestSetDataDirRejectsProduction(t *testing.T) {
	for _, root := range fsroot.WellKnownProductionRoots() {
		if err := storage.SetDataDir(root); err == nil {
			t.Errorf("SetDataDir(%q) succeeded; production paths must be rejected", root)
		}
	}
}

func TestDataDirOverrideUsesFixture(t *testing.T) {
	dir := t.TempDir()
	if err := fsroot.RejectProductionPath(dir); err != nil {
		t.Fatal(err)
	}
	prev := storage.DataDirOverride()
	if err := storage.SetDataDir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.SetDataDir(prev) })

	got, err := storage.DataDir()
	if err != nil {
		t.Fatal(err)
	}
	if got != dir {
		t.Fatalf("DataDir() = %q, want fixture %q", got, dir)
	}
	if fsroot.IsProductionPath(got) {
		t.Fatalf("DataDir returned production path %q", got)
	}
}

func TestDefaultDataDirPathIsProductionAndNotCreatedByLookup(t *testing.T) {
	def := storage.DefaultDataDirPath()
	if def == "" {
		t.Fatal("empty default data dir")
	}
	if !fsroot.IsProductionPath(def) {
		t.Fatalf("DefaultDataDirPath %q not classified as production", def)
	}
	if runtime.GOOS == "windows" {
		if filepath.Base(def) != "sysmon" {
			t.Errorf("Windows data dir base = %s, want sysmon", filepath.Base(def))
		}
	} else {
		if filepath.Base(def) != "sysmon" {
			t.Errorf("Unix data dir base = %s, want sysmon", filepath.Base(def))
		}
	}
}

func TestDataDirEnvOverrideRejectedWhenProductionAndIsolated(t *testing.T) {
	t.Setenv(fsroot.EnvTestIsolation, "1")
	prod := storage.DefaultDataDirPath()
	t.Setenv(storage.EnvDataDir, prod)
	prev := storage.DataDirOverride()
	if err := storage.SetDataDir(""); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.SetDataDir(prev) })
	if _, err := storage.DataDir(); err == nil {
		t.Fatal("expected DataDir to reject production env override under isolation")
	}
}

func TestDataDirRequiresOverrideWhenIsolated(t *testing.T) {
	t.Setenv(fsroot.EnvTestIsolation, "1")
	t.Setenv(storage.EnvDataDir, "")
	prev := storage.DataDirOverride()
	if err := storage.SetDataDir(""); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.SetDataDir(prev) })
	if _, err := storage.DataDir(); err == nil {
		t.Fatal("expected DataDir to fail without override under isolation")
	}
}

func TestDataDirEnvOverrideAcceptedForTemp(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(fsroot.EnvTestIsolation, "1")
	t.Setenv(storage.EnvDataDir, dir)
	prev := storage.DataDirOverride()
	if err := storage.SetDataDir(""); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = storage.SetDataDir(prev) })
	got, err := storage.DataDir()
	if err != nil {
		t.Fatal(err)
	}
	if got != dir {
		t.Fatalf("DataDir() = %q, want %q", got, dir)
	}
}
