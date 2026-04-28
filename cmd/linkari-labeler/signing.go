package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
)

// SignLabel signs label bytes using HMAC-SHA256 with the parsed signing key.
// Returns base64-encoded signature.
func SignLabel(keyMultibase string, data []byte) (string, error) {
	keyBytes, err := parseSigningKey(keyMultibase)
	if err != nil {
		return "", fmt.Errorf("labeler_signing_failed: %w", err)
	}
	mac := hmac.New(sha256.New, keyBytes)
	mac.Write(data)
	sig := mac.Sum(nil)
	return base64.RawURLEncoding.EncodeToString(sig), nil
}

// VerifyLabel verifies an HMAC-SHA256 label signature.
func VerifyLabel(keyMultibase string, data []byte, sigBase64 string) (bool, error) {
	keyBytes, err := parseSigningKey(keyMultibase)
	if err != nil {
		return false, err
	}
	sig, err := base64.RawURLEncoding.DecodeString(sigBase64)
	if err != nil {
		return false, errors.New("labeler_key_corrupt: invalid signature encoding")
	}
	mac := hmac.New(sha256.New, keyBytes)
	mac.Write(data)
	expected := mac.Sum(nil)
	return hmac.Equal(sig, expected), nil
}
