//go:build windows

package main

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"golang.org/x/sys/windows"
)

// Require a protected allowlist DACL on both the file and its immediate parent.
// This rejects broad inherited grants and directory delete/replace permissions.
// Native second-user service-identity validation is still required at G1.
func checkConfigPermissions(path string, info os.FileInfo) error {
	var token windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_QUERY, &token); err != nil {
		return err
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil {
		return err
	}
	allowed := map[string]bool{"SY": true, "BA": true, "S-1-5-18": true, "S-1-5-32-544": true, user.User.Sid.String(): true}
	ace := regexp.MustCompile(`\(([^()]*)\)`)
	for _, name := range []string{path, filepath.Dir(path)} {
		item, err := os.Lstat(name)
		if err != nil || item.Mode()&os.ModeSymlink != 0 {
			return errors.New("unsafe configuration path")
		}
		sd, err := windows.GetNamedSecurityInfo(name, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
		if err != nil {
			return err
		}
		sddl := sd.String()
		if !strings.Contains(sddl, "D:P") {
			return errors.New("config requires a protected DACL")
		}
		entries := ace.FindAllStringSubmatch(sddl, -1)
		if len(entries) == 0 {
			return errors.New("empty configuration DACL")
		}
		for _, entry := range entries {
			parts := strings.Split(entry[1], ";")
			if len(parts) != 6 || parts[0] != "A" || !allowed[parts[5]] {
				return errors.New("configuration DACL grants an unapproved identity")
			}
		}
	}
	return nil
}
