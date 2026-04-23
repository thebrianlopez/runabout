package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// CT-1: ConfigHome uses $XDG_CONFIG_HOME when set
func TestConfigHome_XDGEnvSet(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/custom")
	p := NewPaths()
	assert.Equal(t, "/custom/bmux", p.ConfigHome())
}

// CT-2: ConfigHome falls back to ~/.config/bmux when XDG_CONFIG_HOME unset
func TestConfigHome_Fallback(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "")
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	p := NewPaths()
	assert.Equal(t, filepath.Join(home, ".config", "bmux"), p.ConfigHome())
}

// CT-3: StateHome uses $XDG_STATE_HOME when set
func TestStateHome_XDGEnvSet(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "/custom")
	p := NewPaths()
	assert.Equal(t, "/custom/bmux", p.StateHome())
}

// CT-4: StateHome falls back to ~/.local/state/bmux when XDG_STATE_HOME unset
func TestStateHome_Fallback(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "")
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	p := NewPaths()
	assert.Equal(t, filepath.Join(home, ".local", "state", "bmux"), p.StateHome())
}

// CT-5: CacheHome uses $XDG_CACHE_HOME when set
func TestCacheHome_XDGEnvSet(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", "/custom")
	p := NewPaths()
	assert.Equal(t, "/custom/bmux", p.CacheHome())
}

// CT-6: CacheHome falls back to ~/.cache/bmux when XDG_CACHE_HOME unset
func TestCacheHome_Fallback(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", "")
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	p := NewPaths()
	assert.Equal(t, filepath.Join(home, ".cache", "bmux"), p.CacheHome())
}

// Derived path methods use their base dirs correctly.
func TestConfigFile_UsesConfigHome(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/cfg")
	p := NewPaths()
	assert.Equal(t, "/cfg/bmux/config.yaml", p.ConfigFile())
}

func TestPIDFile_UsesStateHome(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "/state")
	p := NewPaths()
	assert.Equal(t, "/state/bmux/bmux.pid", p.PIDFile())
}

func TestSocketDir_EqualsStateHome(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "/state")
	p := NewPaths()
	assert.Equal(t, p.StateHome(), p.SocketDir())
}
