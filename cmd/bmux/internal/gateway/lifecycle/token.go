package lifecycle

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// GenerateToken generates a 64-char hex token using crypto/rand and atomically
// writes it to the config file at path. Returns the generated token string.
//
// The write is atomic: the new content is written to a temp file in the same
// directory, then renamed into place (os.Rename is atomic on POSIX).
func GenerateToken(configPath string) (string, error) {
	// Generate 32 random bytes → 64-char hex string.
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	token := hex.EncodeToString(buf)

	// Read existing config content.
	data, err := os.ReadFile(configPath)
	if err != nil {
		return "", fmt.Errorf("read config %s: %w", configPath, err)
	}

	// Update or inject the token into the YAML.
	// Strategy: look for an existing "token:" line under "auth:" and replace it,
	// or append a gateway section if none exists.
	updated := injectToken(string(data), token)

	// Atomic write: write to temp file in same dir, then rename.
	dir := filepath.Dir(configPath)
	tmp, err := os.CreateTemp(dir, ".bmux-config-*.yaml")
	if err != nil {
		return "", fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmp.Name()

	if _, err := tmp.WriteString(updated); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("close temp file: %w", err)
	}

	if err := os.Rename(tmpPath, configPath); err != nil {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("rename config file: %w", err)
	}

	return token, nil
}

// injectToken inserts or replaces the gateway.auth.token value in yaml content.
// Simple line-based approach — sufficient for the bmux config format.
func injectToken(content, token string) string {
	lines := strings.Split(content, "\n")

	// Look for existing "token:" line.
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "token:") {
			// Preserve indentation.
			indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
			lines[i] = indent + "token: " + token
			return strings.Join(lines, "\n")
		}
	}

	// No existing token line — look for auth: section under gateway:.
	inGateway := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "gateway:" {
			inGateway = true
			continue
		}
		if inGateway && trimmed == "auth:" {
			// Insert token line after auth:
			indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
			newLines := make([]string, 0, len(lines)+1)
			newLines = append(newLines, lines[:i+1]...)
			newLines = append(newLines, indent+"  token: "+token)
			newLines = append(newLines, lines[i+1:]...)
			return strings.Join(newLines, "\n")
		}
		if inGateway && !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") && trimmed != "" && trimmed != "gateway:" {
			inGateway = false
		}
	}

	// No gateway section at all — append one.
	if !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	content += fmt.Sprintf("gateway:\n  auth:\n    token: %s\n", token)
	return content
}
