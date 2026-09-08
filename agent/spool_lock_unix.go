//go:build !windows

package agent

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

func lockSpool(path string) (*os.File, error) {
	fd, err := unix.Open(path, unix.O_CREAT|unix.O_RDWR|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		file.Close()
		return nil, err
	}
	return file, nil
}

func secureSpoolDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("spool: directory is not a regular directory")
	}
	if err := checkFileOwner(info); err != nil {
		return err
	}
	return os.Chmod(path, 0700)
}

func syncSpoolDir(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	return file.Sync()
}

func spoolReplace(path string, data []byte) error {
	if err := atomicWriteFile(path, data, 0600); err != nil {
		return err
	}
	return syncSpoolDir(filepath.Dir(path))
}
