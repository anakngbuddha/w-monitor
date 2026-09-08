package agent

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestSpoolAppendWhileDrainingPreservesNewSegment(t *testing.T) {
	sp, err := NewSpool(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer sp.Close()
	if err := sp.Append("metric", []byte(`{"old":true}`)); err != nil {
		t.Fatal(err)
	}
	count, err := sp.Drain(func(_ string, _ []byte) error {
		return sp.Append("metric", []byte(`{"new":true}`))
	})
	if err != nil || count != 1 {
		t.Fatalf("drain: count=%d err=%v", count, err)
	}
	depth, err := sp.Depth()
	if err != nil || depth != 1 {
		t.Fatalf("new append lost: depth=%d err=%v", depth, err)
	}
}

func TestSpoolOwnershipAndCheckpointSurviveReopen(t *testing.T) {
	root := t.TempDir()
	sp, err := NewSpool(root)
	if err != nil {
		t.Fatal(err)
	}
	if other, err := NewSpool(root); err == nil {
		other.Close()
		t.Fatal("second queue owner admitted")
	}
	for i := 0; i < 2; i++ {
		if err := sp.Append("metric", []byte(`{}`)); err != nil {
			t.Fatal(err)
		}
	}
	calls := 0
	stop := errors.New("lost connection")
	_, err = sp.Drain(func(_ string, _ []byte) error {
		calls++
		if calls == 2 {
			return stop
		}
		return nil
	})
	if !errors.Is(err, stop) {
		t.Fatalf("drain error: %v", err)
	}
	if err := sp.Close(); err != nil {
		t.Fatal(err)
	}
	sp, err = NewSpool(root)
	if err != nil {
		t.Fatal(err)
	}
	defer sp.Close()
	count, err := sp.Drain(func(_ string, _ []byte) error { return nil })
	if err != nil || count != 1 {
		t.Fatalf("checkpoint lost: count=%d err=%v", count, err)
	}
}

func TestSpoolCorruptSegmentIsRetainedNotSilentlyDeleted(t *testing.T) {
	root := t.TempDir()
	sp, err := NewSpool(root)
	if err != nil {
		t.Fatal(err)
	}
	defer sp.Close()
	path := filepath.Join(root, spoolDirName, "seg-000.ndjson")
	if err := os.WriteFile(path, []byte(`{"torn":`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := sp.Drain(func(_ string, _ []byte) error { t.Fatal("corrupt data delivered"); return nil }); err == nil {
		t.Fatal("corruption not reported")
	}
	if _, err := os.Stat(filepath.Join(root, spoolDirName, "quarantine", filepath.Base(path))); err != nil {
		t.Fatalf("corrupt source not retained: %v", err)
	}
}
