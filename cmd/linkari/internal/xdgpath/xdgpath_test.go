package xdgpath

import (
	"os"
	"path/filepath"
	"testing"
)

func TestXDGPaths_RespectEnvOverrides(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(tmp, "cfg"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(tmp, "cache"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(tmp, "state"))

	cases := []struct {
		name string
		fn   func() (string, error)
		want string
	}{
		{"ConfigDir", ConfigDir, filepath.Join(tmp, "cfg", "linkari")},
		{"CacheDir", CacheDir, filepath.Join(tmp, "cache", "linkari")},
		{"StateDir", StateDir, filepath.Join(tmp, "state", "linkari")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.fn()
			if err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
			st, err := os.Stat(got)
			if err != nil {
				t.Fatalf("stat %s: %v", got, err)
			}
			if !st.IsDir() {
				t.Errorf("%s is not a directory", got)
			}
			if perm := st.Mode().Perm(); perm != 0o700 {
				t.Errorf("%s perm = %o, want 0700", got, perm)
			}
		})
	}
}

func TestXDGPaths_DefaultFallbacks(t *testing.T) {
	tmpHome := t.TempDir()
	t.Setenv("HOME", tmpHome)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("XDG_CACHE_HOME", "")
	t.Setenv("XDG_STATE_HOME", "")

	cases := []struct {
		name string
		fn   func() (string, error)
		want string
	}{
		{"ConfigDir", ConfigDir, filepath.Join(tmpHome, ".config", "linkari")},
		{"CacheDir", CacheDir, filepath.Join(tmpHome, ".cache", "linkari")},
		{"StateDir", StateDir, filepath.Join(tmpHome, ".local", "state", "linkari")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.fn()
			if err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}
