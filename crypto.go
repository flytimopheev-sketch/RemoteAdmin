package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const encPrefix = "enc:v1:"

// secretKey returns a machine-local 32-byte key stored at
// ~/.config/RemoteAdmin/secret.key with 0600 permissions.
// The key is created on first use from a CSPRNG.
func secretKey() ([]byte, error) {
	dir, err := configDir()
	if err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "secret.key")
	if data, err := os.ReadFile(path); err == nil && len(data) >= 32 {
		// derive stable 32 bytes (file may contain arbitrary bytes)
		h := sha256.Sum256(data)
		return h[:], nil
	}
	key := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, key, 0600); err != nil {
		return nil, err
	}
	h := sha256.Sum256(key)
	return h[:], nil
}

// encryptString seals plaintext with AES-256-GCM and returns "enc:v1:<b64>".
func encryptString(plain string) (string, error) {
	if plain == "" {
		return "", nil
	}
	key, err := secretKey()
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	ct := gcm.Seal(nonce, nonce, []byte(plain), nil)
	return encPrefix + base64.StdEncoding.EncodeToString(ct), nil
}

// decryptString opens "enc:v1:<b64>" values. Anything without the prefix
// is returned as-is (plaintext migration from older states).
func decryptString(v string) (string, error) {
	if v == "" {
		return "", nil
	}
	if !strings.HasPrefix(v, encPrefix) {
		return v, nil // legacy plaintext, will be re-encrypted on next save
	}
	key, err := secretKey()
	if err != nil {
		return "", err
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(v, encPrefix))
	if err != nil {
		return "", fmt.Errorf("bad encrypted value: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(raw) < gcm.NonceSize() {
		return "", errors.New("ciphertext too short")
	}
	nonce, ct := raw[:gcm.NonceSize()], raw[gcm.NonceSize():]
	pt, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return "", fmt.Errorf("decrypt failed: %w", err)
	}
	return string(pt), nil
}
