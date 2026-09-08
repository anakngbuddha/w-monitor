//go:build windows

package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

// tokenFilePath returns the OS-default token path used by the production
// compatibility entry points (LoadCredentials/SaveCredentials/ClearCredentials).
func tokenFilePath() string {
	base := os.Getenv("PROGRAMDATA")
	if base == "" {
		base = `C:\ProgramData`
	}
	return filepath.Join(base, "wmonitor", "token.dat")
}

func protectBytes(plaintext []byte) ([]byte, error) {
	if len(plaintext) == 0 {
		return nil, fmt.Errorf("empty plaintext")
	}
	inBlob := windows.DataBlob{
		Size: uint32(len(plaintext)),
		Data: &plaintext[0],
	}
	var outBlob windows.DataBlob
	// CRYPTPROTECT_LOCAL_MACHINE (0x4) allows service running as LocalSystem
	// and admin installers to read the token.
	err := windows.CryptProtectData(&inBlob, nil, nil, 0, nil, windows.CRYPTPROTECT_LOCAL_MACHINE, &outBlob)
	if err != nil {
		return nil, fmt.Errorf("CryptProtectData: %w", err)
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(outBlob.Data)))

	cipher := make([]byte, outBlob.Size)
	copy(cipher, unsafe.Slice(outBlob.Data, outBlob.Size))
	return cipher, nil
}

func unprotectBytes(cipher []byte) ([]byte, error) {
	if len(cipher) == 0 {
		return nil, fmt.Errorf("empty ciphertext")
	}
	inBlob := windows.DataBlob{
		Size: uint32(len(cipher)),
		Data: &cipher[0],
	}
	var outBlob windows.DataBlob
	err := windows.CryptUnprotectData(&inBlob, nil, nil, 0, nil, windows.CRYPTPROTECT_LOCAL_MACHINE, &outBlob)
	if err != nil {
		return nil, fmt.Errorf("CryptUnprotectData: %w", err)
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(outBlob.Data)))

	plaintext := make([]byte, outBlob.Size)
	copy(plaintext, unsafe.Slice(outBlob.Data, outBlob.Size))
	return plaintext, nil
}

// Load decrypts the explicitly selected credential file, not a user-profile
// dependent fallback.
func (s *CredentialStore) Load() (*StoredCredentials, error) {
	path, err := s.filePath()
	if err != nil {
		return nil, err
	}
	if err := rejectSymlink(path); err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	cipher, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNoCredentials
		}
		return nil, fmt.Errorf("read token file: %w", err)
	}
	plaintext, err := unprotectBytes(cipher)
	if err != nil {
		return nil, fmt.Errorf("decrypt token: %w", err)
	}
	var creds StoredCredentials
	if err := json.Unmarshal(plaintext, &creds); err != nil {
		return nil, fmt.Errorf("unmarshal credentials: %w", err)
	}
	return &creds, nil
}

// Save encrypts to the explicitly selected directory using DPAPI, an atomic
// write, and a protected DACL restricting access to SYSTEM/Administrators/
// the current user.
func (s *CredentialStore) Save(creds StoredCredentials) error {
	path, err := s.filePath()
	if err != nil {
		return err
	}
	plaintext, err := json.Marshal(creds)
	if err != nil {
		return fmt.Errorf("marshal credentials: %w", err)
	}
	cipher, err := protectBytes(plaintext)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := rejectSymlink(dir); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create credential directory: %w", err)
	}
	if err := atomicWriteFile(path, cipher, 0600); err != nil {
		return fmt.Errorf("write token file: %w", err)
	}
	if err := applyTokenDACL(path); err != nil {
		return fmt.Errorf("restrict token ACL: %w", err)
	}
	return nil
}

// applyTokenDACL sets a protected DACL: SYSTEM, Administrators, and the
// current user. Built-in Users / Everyone are omitted so a standard account
// cannot read or replace the token.
func applyTokenDACL(path string) error {
	sid, err := currentUserSID()
	if err != nil {
		return err
	}
	sddl := fmt.Sprintf("D:P(A;;FA;;;SY)(A;;FA;;;BA)(A;;FA;;;%s)", sid)
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return fmt.Errorf("token SDDL: %w", err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return fmt.Errorf("token DACL: %w", err)
	}
	return windows.SetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, dacl, nil,
	)
}

func currentUserSID() (string, error) {
	proc := windows.CurrentProcess()
	var tok windows.Token
	if err := windows.OpenProcessToken(proc, windows.TOKEN_QUERY, &tok); err != nil {
		return "", err
	}
	defer tok.Close()
	tu, err := tok.GetTokenUser()
	if err != nil {
		return "", err
	}
	return tu.User.Sid.String(), nil
}
