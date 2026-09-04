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
	dir := filepath.Join(base, "wmonitor")
	_ = os.MkdirAll(dir, 0755)
	return filepath.Join(dir, "token.dat")
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

// LoadCredentials decrypts and loads stored agent credentials from %PROGRAMDATA%\wmonitor\token.dat.
func LoadCredentials() (*StoredCredentials, error) {
	path := tokenFilePath()
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

// SaveCredentials encrypts and saves agent credentials to disk.
func SaveCredentials(creds StoredCredentials) error {
	plaintext, err := json.Marshal(creds)
	if err != nil {
		return fmt.Errorf("marshal credentials: %w", err)
	}

	cipher, err := protectBytes(plaintext)
	if err != nil {
		return err
	}

	path := tokenFilePath()
	if err := os.WriteFile(path, cipher, 0600); err != nil {
		return fmt.Errorf("write token file: %w", err)
	}
	return nil
}

// ClearCredentials removes stored credentials from disk.
func ClearCredentials() error {
	path := tokenFilePath()
	err := os.Remove(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}
