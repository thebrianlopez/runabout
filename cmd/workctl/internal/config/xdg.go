package config

import (
	"os"
	"path/filepath"
)

// XDGConfigHome returns $XDG_CONFIG_HOME or ~/.config per the XDG Base Directory Spec 0.8.
func XDGConfigHome() string {
	if v := os.Getenv("XDG_CONFIG_HOME"); v != "" {
		return v
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config")
}

// XDGStateHome returns $XDG_STATE_HOME or ~/.local/state per the XDG Base Directory Spec 0.8.
func XDGStateHome() string {
	if v := os.Getenv("XDG_STATE_HOME"); v != "" {
		return v
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "state")
}

// XDGDataHome returns $XDG_DATA_HOME or ~/.local/share per the XDG Base Directory Spec 0.8.
func XDGDataHome() string {
	if v := os.Getenv("XDG_DATA_HOME"); v != "" {
		return v
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share")
}

// WorkctlConfigDir returns the workctl config directory: $XDG_CONFIG_HOME/workctl.
func WorkctlConfigDir() string {
	return filepath.Join(XDGConfigHome(), "workctl")
}

// XDGCacheHome returns $XDG_CACHE_HOME or ~/.cache per the XDG Base Directory Spec 0.8.
func XDGCacheHome() string {
	if v := os.Getenv("XDG_CACHE_HOME"); v != "" {
		return v
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".cache")
}

// WorkctlStateDir returns the workctl state directory: $XDG_STATE_HOME/workctl.
func WorkctlStateDir() string {
	return filepath.Join(XDGStateHome(), "workctl")
}

// WorkctlCacheDir returns the workctl cache directory: $XDG_CACHE_HOME/workctl.
func WorkctlCacheDir() string {
	return filepath.Join(XDGCacheHome(), "workctl")
}

// DefaultOutputDir returns the default output directory for workctl data files.
func DefaultOutputDir() string {
	return WorkctlStateDir()
}

// DefaultDebugLog returns the default debug log path: $XDG_STATE_HOME/workctl/debug.log.
func DefaultDebugLog() string {
	return filepath.Join(WorkctlStateDir(), "debug.log")
}
