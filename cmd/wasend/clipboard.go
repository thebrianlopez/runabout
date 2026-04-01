package main

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"
)

// readClipboard returns the current clipboard contents via pbpaste (macOS).
func readClipboard() (string, error) {
	out, err := exec.Command("pbpaste").Output()
	if err != nil {
		return "", fmt.Errorf("pbpaste: %w", err)
	}
	return strings.TrimRight(string(out), "\n"), nil
}

// writeClipboard writes text to the system clipboard via pbcopy (macOS).
func writeClipboard(text string) error {
	cmd := exec.Command("pbcopy")
	cmd.Stdin = bytes.NewBufferString(text)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("pbcopy: %w", err)
	}
	return nil
}
