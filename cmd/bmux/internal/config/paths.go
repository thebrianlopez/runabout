// Package config provides XDG-compliant path resolution and YAML config
// loading for bmux. All bmux packages source their paths from this package —
// no os.UserConfigDir() or hardcoded ~/.bmux paths elsewhere.
package config

import (
	"os"
	"path/filepath"
)

// Paths provides XDG Base Directory compliant paths for bmux.
// All methods are pure: they return paths without creating directories.
// Callers are responsible for MkdirAll when writing files.
type Paths struct{}

// NewPaths returns a Paths instance.
func NewPaths() *Paths { return &Paths{} }

// ConfigHome returns $XDG_CONFIG_HOME/bmux or ~/.config/bmux.
func (p *Paths) ConfigHome() string {
	return xdgDir("XDG_CONFIG_HOME", filepath.Join(".config"))
}

// StateHome returns $XDG_STATE_HOME/bmux or ~/.local/state/bmux.
func (p *Paths) StateHome() string {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "bmux")
}

// CacheHome returns $XDG_CACHE_HOME/bmux or ~/.cache/bmux.
func (p *Paths) CacheHome() string {
	return xdgDir("XDG_CACHE_HOME", ".cache")
}

// ConfigFile returns the default config file path: ConfigHome/config.yaml.
func (p *Paths) ConfigFile() string {
	return filepath.Join(p.ConfigHome(), "config.yaml")
}

// PIDFile returns the PID file path: StateHome/bmux.pid.
func (p *Paths) PIDFile() string {
	return filepath.Join(p.StateHome(), "bmux.pid")
}

// SocketDir returns the directory used for the tmux socket: StateHome.
func (p *Paths) SocketDir() string {
	return p.StateHome()
}

// LogFile returns the daemon log file path: CacheHome/bmux.log.
func (p *Paths) LogFile() string {
	return filepath.Join(p.CacheHome(), "bmux.log")
}

// StatusFile returns the daemon state file path: StateHome/status.json.
func (p *Paths) StatusFile() string {
	return filepath.Join(p.StateHome(), "status.json")
}

// ReadyFile returns the daemon ready sentinel path: StateHome/ready.
func (p *Paths) ReadyFile() string {
	return filepath.Join(p.StateHome(), "ready")
}

func xdgDir(envVar, defaultSubdir string) string {
	base := os.Getenv(envVar)
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, defaultSubdir)
	}
	return filepath.Join(base, "bmux")
}
