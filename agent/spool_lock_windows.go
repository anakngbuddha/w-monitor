//go:build windows

package agent

import (
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

func lockSpool(path string) (*os.File, error) {
	if err := rejectSymlink(path); err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	var overlapped windows.Overlapped
	if err := windows.LockFileEx(windows.Handle(file.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &overlapped); err != nil {
		file.Close()
		return nil, err
	}
	return file, nil
}

func secureSpoolDirectory(path string) error {
	if err := rejectSymlink(path); err != nil {
		return err
	}
	return applyTokenDACL(path)
}

// Windows replacement uses MOVEFILE_WRITE_THROUGH below. Full power-loss
// guarantees still require native filesystem/volume validation at G1.
func syncSpoolDir(path string) error { return nil }

func spoolReplace(path string, data []byte) error {
	if err := rejectSymlink(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".checkpoint-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := applyTokenDACL(name); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	from, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(from, to, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}
