package main

// EPIC-264 M3: demand-side closure test.
//
// This is the regression guard for the "Undeclared Demand / Verified Supply"
// failure class (Instances 1-3, incl. the vnote_synopsis outage where
// voice-note FCM notifications were silently dropped). It quantifies over
// the DEMAND set (RequiredProfiles), not the supply artifact — so a demanded
// name that was never embedded fails the build instead of failing per-row at
// the most expensive point in the pipeline.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestProfileClosureEmbeddedSupply renders every required profile in every
// demanded mode from the embedded supply ALONE (no user tiers). A fresh
// binary on a fresh host must satisfy the full demand set.
func TestProfileClosureEmbeddedSupply(t *testing.T) {
	for name, demands := range RequiredProfiles {
		for _, d := range demands {
			d := d
			t.Run(name+"_mode="+d.Mode+"_json="+boolStr(d.JSON), func(t *testing.T) {
				if err := renderEmbeddedProfileDemand(name, d); err != nil {
					t.Errorf("demand not satisfied by embedded supply: %v", err)
				}
			})
		}
	}
}

// TestRequiredProfilesConstantsRegistered asserts every Profile* constant is
// present in RequiredProfiles — a constant with no demand entry means the
// registry has drifted from the call sites.
func TestRequiredProfilesConstantsRegistered(t *testing.T) {
	for _, name := range []string{ProfileDefault, ProfileEng, ProfileVnoteTriage, ProfileVnoteSynopsis} {
		if len(RequiredProfiles[name]) == 0 {
			t.Errorf("profile constant %q has no entry in RequiredProfiles", name)
		}
	}
}

// TestNoLiteralProfileNamesOutsideRegistry scans production sources for
// string-literal profile names at loadProfileTemplate* call sites. All
// static demand must go through the Profile* constants so the registry
// stays the single declared demand set.
func TestNoLiteralProfileNamesOutsideRegistry(t *testing.T) {
	// Matches e.g. loadProfileTemplateJSON("vnote_synopsis") or
	// loadProfileTemplateForModeJSON("vnote_triage", "audio").
	literalCall := regexp.MustCompile(`loadProfileTemplate\w*\(\s*"`)

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		fname := e.Name()
		if e.IsDir() || !strings.HasSuffix(fname, ".go") || strings.HasSuffix(fname, "_test.go") {
			continue
		}
		b, err := os.ReadFile(filepath.Clean(fname))
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(b), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "//") {
				continue // comments may quote call shapes for documentation
			}
			if literalCall.MatchString(line) {
				t.Errorf("%s:%d: literal profile name at loadProfileTemplate* call site — use a Profile* constant from profile_registry.go:\n\t%s",
					fname, i+1, strings.TrimSpace(line))
			}
		}
	}
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
