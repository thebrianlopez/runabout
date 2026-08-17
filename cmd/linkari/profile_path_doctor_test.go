package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProfileDoctorCT1_SectionRendersAllTiers(t *testing.T) {
	dir := t.TempDir()
	xdg := filepath.Join(dir, "xdg")
	toml := filepath.Join(dir, "toml")
	org := filepath.Join(dir, "org", "docs", "prompts", "profiles")
	embed := filepath.Join(dir, "embed")
	for _, p := range []string{xdg, toml, org, embed} {
		if err := os.MkdirAll(p, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	_ = os.WriteFile(filepath.Join(xdg, "eng.yaml"), []byte("x"), 0o600)
	_ = os.WriteFile(filepath.Join(embed, "default.yaml"), []byte("x"), 0o600)
	orig := profilePathOverrideValue()
	SetProfilePathOverride(toml)
	t.Cleanup(func() { SetProfilePathOverride(orig) })
	t.Setenv("LINKARI_PROFILE_PATH", "")
	t.Setenv("ORG_PATH", filepath.Join(dir, "org"))
	tiers := ProfileSearchPathAnnotated()
	if len(tiers) != 5 {
		t.Fatalf("want 5 tiers, got %d", len(tiers))
	}
	checks, _ := checkProfiles(tiers)
	if len(checks) == 0 {
		t.Fatal("expected checks")
	}
}

func TestProfileDoctorCT2_WinningTierResolution(t *testing.T) {
	dir := t.TempDir()
	xdg := filepath.Join(dir, "xdg")
	embed := filepath.Join(dir, "embed")
	for _, p := range []string{xdg, embed} {
		_ = os.MkdirAll(p, 0o700)
	}
	_ = os.WriteFile(filepath.Join(xdg, "eng.yaml"), []byte("x"), 0o600)
	_ = os.WriteFile(filepath.Join(embed, "eng.yaml"), []byte("x"), 0o600)
	orig := profilePathOverrideValue()
	SetProfilePathOverride("")
	t.Cleanup(func() { SetProfilePathOverride(orig) })
	t.Setenv("LINKARI_PROFILE_PATH", "")
	t.Setenv("ORG_PATH", "")
	checks, resolved := checkProfiles([]ProfileSearchTier{{Path: xdg, Source: "xdg"}, {Path: "", Source: "toml profile_path"}, {Path: "", Source: "unused"}, {Path: "", Source: "unused2"}, {Source: "embedded", Path: "embedded"}})
	_ = checks
	if !strings.Contains(resolved, "eng [xdg]") {
		t.Fatalf("want xdg winner, got %q", resolved)
	}
}

func TestProfileDoctorCT3_ConfigAuthority(t *testing.T) {
	orig := profilePathOverrideValue()
	SetProfilePathOverride("/tmp/configured")
	t.Cleanup(func() { SetProfilePathOverride(orig) })
	t.Setenv("LINKARI_PROFILE_PATH", "")
	t.Setenv("ORG_PATH", "")
	_ = ProfileSearchPathAnnotated()
	// Pass if no panic and no env dependency at check time.
	checks, _ := checkProfiles(ProfileSearchPathAnnotated())
	if len(checks) == 0 {
		t.Fatal("expected checks")
	}
}

func TestProfileDoctorCT4_UnreadableDir(t *testing.T) {
	// Simulate by pointing at non-existent unreadable path.
	checks, _ := checkProfiles([]ProfileSearchTier{{Path: "/no/such/dir", Source: "xdg"}})
	if len(checks) == 0 {
		t.Fatal("expected checks")
	}
}

func TestProfileDoctorRG1_DeprecatedTierWarns(t *testing.T) {
	dir := t.TempDir()
	org := filepath.Join(dir, "org", "docs", "prompts", "profiles")
	_ = os.MkdirAll(org, 0o700)
	_ = os.WriteFile(filepath.Join(org, "legacy.yaml"), []byte("x"), 0o600)
	checks, _ := checkProfiles([]ProfileSearchTier{{Path: org, Source: "org_path (deprecated)", Deprecated: true}})
	warn := false
	for _, c := range checks {
		if c.Status == statusWarn {
			warn = true
		}
	}
	if !warn {
		t.Fatal("expected warning")
	}
}
