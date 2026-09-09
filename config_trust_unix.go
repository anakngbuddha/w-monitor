//go:build !windows

package main

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

func checkConfigPermissions(path string, info os.FileInfo) error {
	if info.Mode().Perm()&0077 != 0 {
		return errors.New("config must not be accessible by group or other users")
	}
	for current := path; ; current = filepath.Dir(current) {
		item, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if item.Mode()&os.ModeSymlink != 0 {
			return errors.New("symlink in protected config path")
		}
		owner, ok := item.Sys().(*syscall.Stat_t)
		if !ok || (owner.Uid != 0 && int(owner.Uid) != os.Geteuid()) {
			return errors.New("untrusted config owner")
		}
		if item.Mode().Perm()&0022 != 0 && !(item.IsDir() && item.Mode()&os.ModeSticky != 0 && owner.Uid == 0) {
			return errors.New("config path is writable by another identity")
		}
		if filepath.Dir(current) == current {
			break
		}
	}
	return nil
}
