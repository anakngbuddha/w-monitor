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
	// Machine DPAPI does not replace filesystem ACL enforcement. That
	// hardening remains a separate Phase 1 acceptance gate.
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

// Save encrypts to the explicitly selected directory. This retains the current
// DPAPI format; it does not claim to implement Windows DACL hardening.
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
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("create credential directory: %w", err)
	}
	if err := os.WriteFile(path, cipher, 0600); err != nil {
		return fmt.Errorf("write token file: %w", err)
	}
	return nil
}
