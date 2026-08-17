package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

type ProfileSearchTier struct {
	Path       string
	Source     string
	Deprecated bool
}

// profilePathOverride holds the `profile_path` value from config.toml.
//
// EPIC-258 M2: this is a genuine process-wide singleton (same class as
// archiveThresholdCfg) because it backs a SIGHUP hot-reload contract - the
// signal handler at main.go re-reads config and calls SetProfilePathOverride
// while scoring goroutines may be inside LoadProfile. It cannot be threaded:
// its readers reach it through the free functions ProfileSearchPath /
// ProfileSearchPathAnnotated / LoadProfile, called from cobra RunE loops in
// cmd_triage.go and from cmd_doctor.go, none of which carry a deps value.
//
// It was previously an UNGUARDED string written from that signal-handler
// goroutine - a live production data race, not merely a test seam. The race
// detector never caught it because no test drives SIGHUP concurrently with
// scoring. archiveThresholdCfg got a mutex when EPIC-051 M6 added its reload;
// this one did not. The mutex below closes that gap.
//
// All access goes through SetProfilePathOverride / profilePathOverrideValue.
// Do not read or write the variable directly, including from tests.
var (
	profilePathMu       sync.RWMutex
	profilePathOverride string
)

// SetProfilePathOverride installs the configured profile_path. Safe to call
// from the SIGHUP handler: the write is guarded by the same mutex readers use.
func SetProfilePathOverride(p string) {
	profilePathMu.Lock()
	profilePathOverride = p
	profilePathMu.Unlock()
}

// profilePathOverrideValue reads the configured profile_path under the lock.
func profilePathOverrideValue() string {
	profilePathMu.RLock()
	defer profilePathMu.RUnlock()
	return profilePathOverride
}

// ProfileSearchPathAnnotated returns the ordered list of profile search tiers.
func ProfileSearchPathAnnotated() []ProfileSearchTier {
	tiers := []ProfileSearchTier{}
	if envPath := os.Getenv("LINKARI_PROFILE_PATH"); envPath != "" {
		tiers = append(tiers, ProfileSearchTier{Path: envPath, Source: "env LINKARI_PROFILE_PATH"})
	} else {
		tiers = append(tiers, ProfileSearchTier{Source: "env LINKARI_PROFILE_PATH"})
	}
	if override := profilePathOverrideValue(); override != "" {
		tiers = append(tiers, ProfileSearchTier{Path: override, Source: "toml profile_path"})
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
	return nil, fmt.Errorf("profile %q not found  -  checked: %v (set LINKARI_PROFILE_PATH or run make update-profiles)", name, checked)
}
