package main

import (
	"fmt"
	"os"
	"path/filepath"
)

var profilePathOverride string

func SetProfilePathOverride(p string) { profilePathOverride = p }

// ProfileSearchPath returns the ordered list of directories to search for profile YAMLs.
// Priority: LINKARI_PROFILE_PATH env → profile_path override → XDG → ORG_PATH → testdata/profiles
func ProfileSearchPath() []string {
	var paths []string
	if envPath := os.Getenv("LINKARI_PROFILE_PATH"); envPath != "" {
		paths = append(paths, envPath)
	}
	if profilePathOverride != "" {
		paths = append(paths, profilePathOverride)
	}
	if cfgDir, err := os.UserConfigDir(); err == nil {
		paths = append(paths, filepath.Join(cfgDir, "linkari", "profiles"))
	}
	if orgPath := os.Getenv("ORG_PATH"); orgPath != "" {
		paths = append(paths, filepath.Join(orgPath, "docs", "prompts", "profiles"))
	}
	paths = append(paths, "testdata/profiles")
	return paths
}

// LoadProfile finds and parses a profile YAML by name from ProfileSearchPath.
// Returns an error containing all searched paths if not found.
func LoadProfile(name string) (*ProfileManifest, error) {
	dirs := ProfileSearchPath()
	var checked []string
	for _, d := range dirs {
		yamlPath := filepath.Join(d, name+".yaml")
		checked = append(checked, yamlPath)
		if _, err := os.Stat(yamlPath); err == nil {
			return LoadProfileManifest(yamlPath)
		}
	}
	return nil, fmt.Errorf("profile %q not found — checked: %v (set LINKARI_PROFILE_PATH or run make update-profiles)", name, checked)
}
