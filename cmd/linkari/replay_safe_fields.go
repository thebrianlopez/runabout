package main

import (
	"crypto/sha256"
	"fmt"

	"github.com/google/uuid"
)

// ContentHash computes SHA-256 of raw fetched content bytes and returns a 64-char hex string.
// Must be called on raw bytes before any extraction or truncation.
func ContentHash(content []byte) string {
	h := sha256.Sum256(content)
	return fmt.Sprintf("%x", h)
}

func newTraceID() string {
	return uuid.New().String()
}
