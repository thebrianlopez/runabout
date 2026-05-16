package main

import (
	"os"
	"path/filepath"
	"testing"
)

// testProfileYAML is a minimal valid profile YAML for test fixtures.
// testProfileYAML is a minimal valid profile YAML for unit tests.
// writeTestProfile copies eng.yaml from testdata/profiles into the given dir with the given name.
// Uses the real profile YAML to satisfy all schema validation requirements.
func writeTestProfile(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	src := filepath.Join("testdata", "profiles", "eng.yaml")
	data, err := os.ReadFile(src)
	if err != nil {
		t.Skipf("writeTestProfile: testdata/profiles/eng.yaml not available: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".yaml"), data, 0o644); err != nil {
		t.Fatalf("write profile: %v", err)
	}
}

// CT-1: LINKARI_PROFILE_PATH env var takes precedence over all other paths
func TestProfilePathCT1_EnvVarPrecedence(t *testing.T) {
	envDir := t.TempDir()
	writeTestProfile(t, envDir, "eng")

	t.Setenv("LINKARI_PROFILE_PATH", envDir)

	m, err := LoadProfile("eng")
	if err != nil {
		t.Fatalf("CT-1: LoadProfile failed: %v", err)
	}
	if m == nil {
		t.Fatal("CT-1: LoadProfile returned nil manifest")
	}
}

// CT-2: Falls back to default paths when LINKARI_PROFILE_PATH is unset
func TestProfilePathCT2_FallbackToDefaultPaths(t *testing.T) {
	// Unset env var to test fallback
	t.Setenv("LINKARI_PROFILE_PATH", "")

	// Create a profile in ORG_PATH location
	orgDir := t.TempDir()
	profilesDir := filepath.Join(orgDir, "docs", "prompts", "profiles")
	writeTestProfile(t, profilesDir, "eng")
	t.Setenv("ORG_PATH", orgDir)

	m, err := LoadProfile("eng")
	if err != nil {
		t.Fatalf("CT-2: LoadProfile with ORG_PATH fallback failed: %v", err)
	}
	if m == nil {
		t.Fatal("CT-2: returned nil manifest")
	}
}

// CT-3: Repo-local testdata/profiles fallback
func TestProfilePathCT3_RepoDirFallback(t *testing.T) {
	// Unset all path overrides
	t.Setenv("LINKARI_PROFILE_PATH", "")
	t.Setenv("ORG_PATH", "")

	// ProfileSearchPath should include "testdata/profiles" as last fallback.
	// This test verifies the path is in the list (CT-5 tests actual loading).
	paths := ProfileSearchPath()
	hasTestdata := false
	for _, p := range paths {
		if filepath.Base(p) == "profiles" && filepath.Base(filepath.Dir(p)) == "testdata" {
			hasTestdata = true
			break
		}
	}
	if !hasTestdata {
		t.Errorf("CT-3: testdata/profiles not in ProfileSearchPath: %v", paths)
	}
}

// CT-4: profile_not_found error when profile missing from all paths
func TestProfilePathCT4_NotFoundError(t *testing.T) {
	emptyDir := t.TempDir()
	t.Setenv("LINKARI_PROFILE_PATH", emptyDir)
	t.Setenv("ORG_PATH", "")

	_, err := LoadProfile("nonexistent_profile_xyz")
	if err == nil {
		t.Fatal("CT-4: expected error for nonexistent profile, got nil")
	}
}

// CT-5: All 7 profiles loadable from testdata/profiles via LINKARI_PROFILE_PATH
func TestProfilePathCT5_AllSevenProfilesLoadable(t *testing.T) {
	// Point to the repo-local testdata/profiles directory
	t.Setenv("LINKARI_PROFILE_PATH", "testdata/profiles")

	profiles := []string{"eng", "life", "travel", "fashion", "music", "finance", "dining"}
	for _, p := range profiles {
		m, err := LoadProfile(p)
		if err != nil {
			t.Errorf("CT-5: LoadProfile(%q) failed: %v", p, err)
			continue
		}
		if m == nil {
			t.Errorf("CT-5: LoadProfile(%q) returned nil", p)
		}
	}
}
