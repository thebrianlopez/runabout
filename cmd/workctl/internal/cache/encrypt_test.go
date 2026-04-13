package cache

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLoadOrCreateIdentity_Creates verifies that a new key file is created with
// correct permissions when none exists.
func TestLoadOrCreateIdentity_Creates(t *testing.T) {
	dir := t.TempDir()
	id, err := loadOrCreateIdentity(dir, "correct-horse-battery-staple")
	if err != nil {
		t.Fatalf("loadOrCreateIdentity: %v", err)
	}
	if id == nil {
		t.Fatal("expected non-nil identity")
	}

	keyPath := filepath.Join(dir, keyFileName)
	info, err := os.Stat(keyPath)
	if err != nil {
		t.Fatalf("key file not created: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0600 {
		t.Errorf("key file permissions = %o, want 0600", mode)
	}
}

// TestLoadOrCreateIdentity_LoadsExisting verifies that loading with the same
// passphrase returns the same public key.
func TestLoadOrCreateIdentity_LoadsExisting(t *testing.T) {
	dir := t.TempDir()
	pass := "correct-horse-battery-staple"

	id1, err := loadOrCreateIdentity(dir, pass)
	if err != nil {
		t.Fatalf("first loadOrCreateIdentity: %v", err)
	}

	id2, err := loadOrCreateIdentity(dir, pass)
	if err != nil {
		t.Fatalf("second loadOrCreateIdentity: %v", err)
	}

	if id1.Recipient().String() != id2.Recipient().String() {
		t.Error("public keys differ on reload — expected same key")
	}
}

// TestLoadOrCreateIdentity_WrongPassphrase verifies that a wrong passphrase
// returns an error when the key file already exists.
func TestLoadOrCreateIdentity_WrongPassphrase(t *testing.T) {
	dir := t.TempDir()

	if _, err := loadOrCreateIdentity(dir, "right-passphrase"); err != nil {
		t.Fatalf("create: %v", err)
	}

	_, err := loadOrCreateIdentity(dir, "wrong-passphrase")
	if err == nil {
		t.Fatal("expected error for wrong passphrase, got nil")
	}
}

// TestLoadOrCreateIdentity_CorruptKeyFile verifies that a corrupt key file
// returns an error.
func TestLoadOrCreateIdentity_CorruptKeyFile(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, keyFileName)

	if err := os.WriteFile(keyPath, []byte("not-age-encrypted-garbage"), 0600); err != nil {
		t.Fatalf("write corrupt key: %v", err)
	}

	_, err := loadOrCreateIdentity(dir, "any-passphrase")
	if err == nil {
		t.Fatal("expected error for corrupt key file, got nil")
	}
}

// TestEncryptDecryptBlob_Roundtrip verifies that encrypting then decrypting
// recovers the original plaintext.
func TestEncryptDecryptBlob_Roundtrip(t *testing.T) {
	dir := t.TempDir()
	id, err := loadOrCreateIdentity(dir, "passphrase")
	if err != nil {
		t.Fatalf("loadOrCreateIdentity: %v", err)
	}

	plaintext := []byte(`{"hello":"world"}`)

	ciphertext, err := encryptBlob(id, plaintext)
	if err != nil {
		t.Fatalf("encryptBlob: %v", err)
	}

	got, err := decryptBlob(id, ciphertext)
	if err != nil {
		t.Fatalf("decryptBlob: %v", err)
	}
	if string(got) != string(plaintext) {
		t.Errorf("roundtrip = %q, want %q", got, plaintext)
	}
}

// TestEncryptDecryptBlob_WrongIdentity verifies that decryption with a
// different identity returns an error.
func TestEncryptDecryptBlob_WrongIdentity(t *testing.T) {
	dir := t.TempDir()
	id1, err := loadOrCreateIdentity(dir, "pass1")
	if err != nil {
		t.Fatalf("loadOrCreateIdentity 1: %v", err)
	}

	dir2 := t.TempDir()
	id2, err := loadOrCreateIdentity(dir2, "pass2")
	if err != nil {
		t.Fatalf("loadOrCreateIdentity 2: %v", err)
	}

	ciphertext, err := encryptBlob(id1, []byte("secret"))
	if err != nil {
		t.Fatalf("encryptBlob: %v", err)
	}

	_, err = decryptBlob(id2, ciphertext)
	if err == nil {
		t.Fatal("expected error decrypting with wrong identity, got nil")
	}
}

// TestIsAgeEncrypted_True verifies detection of a real age-encrypted blob.
func TestIsAgeEncrypted_True(t *testing.T) {
	dir := t.TempDir()
	id, err := loadOrCreateIdentity(dir, "pass")
	if err != nil {
		t.Fatalf("loadOrCreateIdentity: %v", err)
	}
	ciphertext, err := encryptBlob(id, []byte("data"))
	if err != nil {
		t.Fatalf("encryptBlob: %v", err)
	}
	if !isAgeEncrypted(ciphertext) {
		t.Error("isAgeEncrypted = false for age ciphertext, want true")
	}
}

// TestIsAgeEncrypted_False_Gzip verifies that gzip data is not detected as age.
func TestIsAgeEncrypted_False_Gzip(t *testing.T) {
	compressed, err := compress([]byte("hello"))
	if err != nil {
		t.Fatalf("compress: %v", err)
	}
	if isAgeEncrypted(compressed) {
		t.Error("isAgeEncrypted = true for gzip data, want false")
	}
}

// TestIsAgeEncrypted_False_Empty verifies that empty data is not detected as age.
func TestIsAgeEncrypted_False_Empty(t *testing.T) {
	if isAgeEncrypted([]byte{}) {
		t.Error("isAgeEncrypted = true for empty data, want false")
	}
	if isAgeEncrypted(nil) {
		t.Error("isAgeEncrypted = true for nil, want false")
	}
}
