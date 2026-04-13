package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestXDGConfigHome(t *testing.T) {
	t.Run("env var set", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", "/custom/config")
		if got := XDGConfigHome(); got != "/custom/config" {
			t.Errorf("XDGConfigHome() = %q, want /custom/config", got)
		}
	})

	t.Run("env var unset uses default", func(t *testing.T) {
		t.Setenv("XDG_CONFIG_HOME", "")
		home, _ := os.UserHomeDir()
		want := filepath.Join(home, ".config")
		if got := XDGConfigHome(); got != want {
			t.Errorf("XDGConfigHome() = %q, want %q", got, want)
		}
	})
}

func TestXDGStateHome(t *testing.T) {
	t.Run("env var set", func(t *testing.T) {
		t.Setenv("XDG_STATE_HOME", "/custom/state")
		if got := XDGStateHome(); got != "/custom/state" {
			t.Errorf("XDGStateHome() = %q, want /custom/state", got)
		}
	})

	t.Run("env var unset uses default", func(t *testing.T) {
		t.Setenv("XDG_STATE_HOME", "")
		home, _ := os.UserHomeDir()
		want := filepath.Join(home, ".local", "state")
		if got := XDGStateHome(); got != want {
			t.Errorf("XDGStateHome() = %q, want %q", got, want)
		}
	})
}

func TestXDGDataHome(t *testing.T) {
	t.Run("env var set", func(t *testing.T) {
		t.Setenv("XDG_DATA_HOME", "/custom/data")
		if got := XDGDataHome(); got != "/custom/data" {
			t.Errorf("XDGDataHome() = %q, want /custom/data", got)
		}
	})

	t.Run("env var unset uses default", func(t *testing.T) {
		t.Setenv("XDG_DATA_HOME", "")
		home, _ := os.UserHomeDir()
		want := filepath.Join(home, ".local", "share")
		if got := XDGDataHome(); got != want {
			t.Errorf("XDGDataHome() = %q, want %q", got, want)
		}
	})
}

func TestXDGCacheHome(t *testing.T) {
	t.Run("env var set", func(t *testing.T) {
		t.Setenv("XDG_CACHE_HOME", "/custom/cache")
		if got := XDGCacheHome(); got != "/custom/cache" {
			t.Errorf("XDGCacheHome() = %q, want /custom/cache", got)
		}
	})

	t.Run("env var unset uses default", func(t *testing.T) {
		t.Setenv("XDG_CACHE_HOME", "")
		home, _ := os.UserHomeDir()
		want := filepath.Join(home, ".cache")
		if got := XDGCacheHome(); got != want {
			t.Errorf("XDGCacheHome() = %q, want %q", got, want)
		}
	})
}

func TestWorkctlCacheDir(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", "/test/cache")
	want := "/test/cache/workctl"
	if got := WorkctlCacheDir(); got != want {
		t.Errorf("WorkctlCacheDir() = %q, want %q", got, want)
	}
}

func TestWorkctlConfigDir(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/test/config")
	want := "/test/config/workctl"
	if got := WorkctlConfigDir(); got != want {
		t.Errorf("WorkctlConfigDir() = %q, want %q", got, want)
	}
}

func TestWorkctlStateDir(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "/test/state")
	want := "/test/state/workctl"
	if got := WorkctlStateDir(); got != want {
		t.Errorf("WorkctlStateDir() = %q, want %q", got, want)
	}
}

func TestDefaultOutputDir(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "/test/state")
	want := "/test/state/workctl"
	if got := DefaultOutputDir(); got != want {
		t.Errorf("DefaultOutputDir() = %q, want %q", got, want)
	}
}

func TestDefaultDebugLog(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "/test/state")
	want := "/test/state/workctl/debug.log"
	if got := DefaultDebugLog(); got != want {
		t.Errorf("DefaultDebugLog() = %q, want %q", got, want)
	}
}

func TestDefaultOutputDirIsAbsolute(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "")
	got := DefaultOutputDir()
	if !strings.HasPrefix(got, "/") {
		t.Errorf("DefaultOutputDir() = %q, want absolute path", got)
	}
}
