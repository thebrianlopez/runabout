package main

import (
	"fmt"
	"os"
	"path/filepath"
)

type ProfileSearchTier struct {
	Path       string
	Source     string
	Deprecated bool
}

var profilePathOverride string

func SetProfilePathOverride(p string) { profilePathOverride = p }

// ProfileSearchPathAnnotated returns the ordered list of profile search tiers.
func ProfileSearchPathAnnotated() []ProfileSearchTier {
	tiers := []ProfileSearchTier{}
	if envPath := os.Getenv("LINKARI_PROFILE_PATH"); envPath != "" {
		tiers = append(tiers, ProfileSearchTier{Path: envPath, Source: "env LINKARI_PROFILE_PATH"})
	} else {
		tiers = append(tiers, ProfileSearchTier{Source: "env LINKARI_PROFILE_PATH"})
	}
	if profilePathOverride != "" {
		tiers = append(tiers, ProfileSearchTier{Path: profilePathOverride, Source: "toml profile_path"})
	} else {
		tiers = append(tiers, ProfileSearchTier{Source: "toml profile_path"})
	}
	if cfgDir, err := os.UserConfigDir(); err == nil {
		tiers = append(tiers, ProfileSearchTier{Path: filepath.Join(cfgDir, "linkari", "profiles"), Source: "xdg"})
	} else {
		tiers = append(tiers, ProfileSearchTier{Source: "xdg"})
	}
	if orgPath := os.Getenv("ORG_PATH"); orgPath != "" {
		tiers = append(tiers, ProfileSearchTier{Path: filepath.Join(orgPath, "docs", "prompts", "profiles"), Source: "org_path (deprecated)", Deprecated: true})
	} else {
		tiers = append(tiers, ProfileSearchTier{Source: "org_path (deprecated)", Deprecated: true})
	}
	tiers = append(tiers, ProfileSearchTier{Path: "testdata/profiles", Source: "embedded"})
	return tiers
}

// ProfileSearchPath returns the ordered list of directories to search for profile YAMLs.
func ProfileSearchPath() []string {
	out := make([]string, 0, len(ProfileSearchPathAnnotated()))
	for _, t := range ProfileSearchPathAnnotated() {
		if t.Path != "" {
			out = append(out, t.Path)
		}
	}
	return out
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
