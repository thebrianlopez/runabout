// Package xdgpath resolves XDG Base Directory paths for the linkari binary.
//
// Layout:
//
//	ConfigDir() → $XDG_CONFIG_HOME/linkari  (default ~/.config/linkari)
//	CacheDir()  → $XDG_CACHE_HOME/linkari   (default ~/.cache/linkari)
//	StateDir()  → $XDG_STATE_HOME/linkari   (default ~/.local/state/linkari)
//
// All three accessors ensure the directory exists (0700) before returning.
package xdgpath

import (
	"fmt"
	"os"
	"path/filepath"
)

const appName = "linkari"

// ConfigDir returns the operator config directory (actions.yaml, server.yaml,
// client.env, profiles/). Created with 0700 if missing.
func ConfigDir() (string, error) {
	return resolve("XDG_CONFIG_HOME", ".config")
}

// CacheDir returns the materialized-secret cache directory (firebase-sa.json,
// .token-fp). Treated as ephemeral; safe to delete between runs. Created with
// 0700 if missing.
func CacheDir() (string, error) {
	return resolve("XDG_CACHE_HOME", ".cache")
}

// StateDir returns the runtime state directory (linkari-server.log, queue.db).
// Persistent across restarts. Created with 0700 if missing.
func StateDir() (string, error) {
	return resolveState()
}

func resolve(envVar, defaultSubdir string) (string, error) {
	base := os.Getenv(envVar)
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("xdgpath: resolve home: %w", err)
		}
		base = filepath.Join(home, defaultSubdir)
	}
	dir := filepath.Join(base, appName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("xdgpath: mkdir %s: %w", dir, err)
	}
	return dir, nil
}

func resolveState() (string, error) {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("xdgpath: resolve home: %w", err)
		}
		base = filepath.Join(home, ".local", "state")
	}
	dir := filepath.Join(base, appName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("xdgpath: mkdir %s: %w", dir, err)
	}
	return dir, nil
}
