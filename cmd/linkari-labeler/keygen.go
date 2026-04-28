package main

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
)

// generateSigningKey generates a 32-byte random signing key encoded as base64.
// Uses "z" prefix as a multibase indicator (base58btc convention for AT Protocol).
// For MVP: base64-encoded random bytes. Full P-256 can replace this later.
func generateSigningKey() (string, error) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return "", fmt.Errorf("generate key: %w", err)
	}
	return "z" + base64.RawURLEncoding.EncodeToString(key), nil
}

// parseSigningKey decodes a multibase-encoded signing key.
// Returns the raw key bytes. Returns error for invalid/corrupt keys.
func parseSigningKey(multibase string) ([]byte, error) {
	if len(multibase) < 2 || multibase[0] != 'z' {
		return nil, fmt.Errorf("labeler_key_corrupt: expected 'z' prefix")
	}
	key, err := base64.RawURLEncoding.DecodeString(multibase[1:])
	if err != nil {
		return nil, fmt.Errorf("labeler_key_corrupt: %w", err)
	}
	if len(key) < 16 {
		return nil, fmt.Errorf("labeler_key_corrupt: key too short")
	}
	return key, nil
}

func runKeygen(cfgPath string) {
	key, err := generateSigningKey()
	if err != nil {
		fmt.Fprintf(os.Stderr, "labeler_key_missing: %v\n", err)
		os.Exit(1)
	}
	cfg, _ := loadLabelerConfig(cfgPath)
	cfg.LabelerSigningKeyMultibase = key
	if err := writeLabelerConfig(cfgPath, cfg); err != nil {
		fmt.Fprintf(os.Stderr, "labeler_key_missing: write config: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("labeler signing key written to %s\n", cfgPath)
}
