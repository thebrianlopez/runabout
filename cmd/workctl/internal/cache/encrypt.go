package cache

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"filippo.io/age"
)

const (
	keyFileName     = "cache.key.age"
	ageHeaderPrefix = "age-encryption.org/v1"
)

// loadOrCreateIdentity loads the X25519 identity from configDir/cache.key.age,
// generating and saving a new one if the file doesn't exist. scrypt runs exactly once here.
func loadOrCreateIdentity(configDir, passphrase string) (*age.X25519Identity, error) {
	keyPath := filepath.Join(configDir, keyFileName)
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		return nil, fmt.Errorf("cache config dir: %w", err)
	}
	if _, err := os.Stat(keyPath); os.IsNotExist(err) {
		return generateAndSaveIdentity(keyPath, passphrase)
	} else if err != nil {
		return nil, fmt.Errorf("cache key file: %w", err)
	}
	return loadIdentity(keyPath, passphrase)
}

// generateAndSaveIdentity creates a new random X25519 keypair, encrypts the
// private key with the passphrase, and writes it atomically to keyPath (0600).
func generateAndSaveIdentity(keyPath, passphrase string) (*age.X25519Identity, error) {
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		return nil, fmt.Errorf("generate key: %w", err)
	}
	recip, err := age.NewScryptRecipient(passphrase)
	if err != nil {
		return nil, fmt.Errorf("scrypt recipient: %w", err)
	}
	var buf bytes.Buffer
	w, err := age.Encrypt(&buf, recip)
	if err != nil {
		return nil, fmt.Errorf("encrypt key: %w", err)
	}
	if _, err := io.WriteString(w, identity.String()); err != nil {
		return nil, fmt.Errorf("write key: %w", err)
	}
	if err := w.Close(); err != nil {
		return nil, fmt.Errorf("close key writer: %w", err)
	}
	// Atomic write via temp file + rename
	tmp, err := os.CreateTemp(filepath.Dir(keyPath), ".cache.key.age.tmp")
	if err != nil {
		return nil, fmt.Errorf("temp key file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		tmp.Close()
		os.Remove(tmpName) // no-op if rename succeeded
	}()
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return nil, err
	}
	if _, err := tmp.Write(buf.Bytes()); err != nil {
		return nil, fmt.Errorf("write temp key: %w", err)
	}
	tmp.Close()
	if err := os.Rename(tmpName, keyPath); err != nil {
		return nil, fmt.Errorf("install key file: %w", err)
	}
	return identity, nil
}

// loadIdentity decrypts the key file and parses the X25519 private key.
func loadIdentity(keyPath, passphrase string) (*age.X25519Identity, error) {
	raw, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, fmt.Errorf("reading cache key file: %w", err)
	}
	id, err := age.NewScryptIdentity(passphrase)
	if err != nil {
		return nil, fmt.Errorf("scrypt identity: %w", err)
	}
	r, err := age.Decrypt(bytes.NewReader(raw), id)
	if err != nil {
		// NoIdentityMatchError means wrong passphrase
		return nil, fmt.Errorf("wrong passphrase or corrupt cache key file: %w", err)
	}
	keyBytes, err := io.ReadAll(io.LimitReader(r, 256))
	if err != nil {
		return nil, fmt.Errorf("read key bytes: %w", err)
	}
	identity, err := age.ParseX25519Identity(strings.TrimSpace(string(keyBytes)))
	if err != nil {
		return nil, fmt.Errorf("corrupt cache key file: %w", err)
	}
	return identity, nil
}

// encryptBlob encrypts plaintext using the X25519 public key (no scrypt).
func encryptBlob(identity *age.X25519Identity, plaintext []byte) ([]byte, error) {
	var buf bytes.Buffer
	w, err := age.Encrypt(&buf, identity.Recipient())
	if err != nil {
		return nil, fmt.Errorf("age encrypt init: %w", err)
	}
	if _, err := w.Write(plaintext); err != nil {
		return nil, fmt.Errorf("age encrypt write: %w", err)
	}
	if err := w.Close(); err != nil {
		return nil, fmt.Errorf("age encrypt close: %w", err)
	}
	return buf.Bytes(), nil
}

// decryptBlob decrypts an age-encrypted blob using the X25519 private key.
func decryptBlob(identity *age.X25519Identity, ciphertext []byte) ([]byte, error) {
	r, err := age.Decrypt(bytes.NewReader(ciphertext), identity)
	if err != nil {
		return nil, fmt.Errorf("age decrypt: %w", err)
	}
	out, err := io.ReadAll(io.LimitReader(r, maxDecompressBytes+1))
	if err != nil {
		return nil, fmt.Errorf("age decrypt read: %w", err)
	}
	return out, nil
}

// isAgeEncrypted returns true if data begins with the age format header.
func isAgeEncrypted(data []byte) bool {
	return bytes.HasPrefix(data, []byte(ageHeaderPrefix))
}
