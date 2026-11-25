package main

import (
	"context"
	"testing"
)

// boolPtr returns a pointer to b. Convenience for table tests.
func boolPtr(b bool) *bool { return &b }

// intPtr returns a pointer to i. Convenience for table tests.
func intPtr(i int) *int { return &i }

// TestResolveBoolField covers the 12 mandatory rows from EPIC-048 M1
// (blockers-to-95 analysis, blocker #5) plus env-invalid-falls-through.
func TestResolveBoolField(t *testing.T) {
	cases := []struct {
		name     string
		flag     bool
		flagSet  bool
		env      string
		yaml     *bool
		def      bool
		wantVal  bool
		wantTier string
	}{
		// Flag tier wins.
		{"flag-true-wins", true, true, "0", boolPtr(false), false, true, "flag"},
		{"flag-false-wins", false, true, "1", boolPtr(true), true, false, "flag"},
		{"flagSet-overrides-env", false, true, "1", nil, true, false, "flag"},
		{"flagSet-overrides-yaml", true, true, "", boolPtr(false), false, true, "flag"},

		// Env tier wins (flagSet=false).
		{"env-1-wins", false, false, "1", boolPtr(false), false, true, "env"},
		{"env-true-wins", false, false, "true", nil, false, true, "env"},
		{"env-TRUE-wins", false, false, "TRUE", nil, false, true, "env"},
		{"env-True-wins", false, false, "True", nil, false, true, "env"},
		{"env-0-is-false", false, false, "0", boolPtr(true), true, false, "env"},
		{"env-false-str-is-false", false, false, "false", boolPtr(true), true, false, "env"},
		{"env-wins-over-yaml", false, false, "1", boolPtr(false), false, true, "env"},

		// Yaml tier wins (flagSet=false, env="").
		{"yaml-true-wins", false, false, "", boolPtr(true), false, true, "yaml"},
		{"yaml-false-wins", false, false, "", boolPtr(false), true, false, "yaml"},

		// Default tier (flagSet=false, env="", yaml=nil).
		{"default-true", false, false, "", nil, true, true, "default"},
		{"default-false", false, false, "", nil, false, false, "default"},
		{"yaml-nil-falls-through-to-default", false, false, "", nil, true, true, "default"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v, tier, src := resolveBoolField(tc.flag, tc.flagSet, tc.env, tc.yaml, tc.def)
			if v != tc.wantVal {
				t.Errorf("value=%v want %v", v, tc.wantVal)
			}
			if tier != tc.wantTier {
				t.Errorf("tier=%q want %q", tier, tc.wantTier)
			}
			if src != tc.wantTier {
				t.Errorf("src=%q want %q", src, tc.wantTier)
			}
		})
	}
}

// TestResolveIntField covers the four-tier resolution for int fields.
func TestResolveIntField(t *testing.T) {
	cases := []struct {
		name     string
		flag     int
		flagSet  bool
		env      string
		yaml     *int
		def      int
		wantVal  int
		wantTier string
	}{
		{"flag-wins", 42, true, "10", intPtr(5), 0, 42, "flag"},
		{"flag-zero-explicit", 0, true, "10", intPtr(5), 3, 0, "flag"},
		{"env-wins", 0, false, "10", intPtr(5), 0, 10, "env"},
		{"env-invalid-skipped", 0, false, "notanint", intPtr(5), 0, 5, "yaml"},
		{"env-invalid-no-yaml-falls-to-default", 0, false, "notanint", nil, 7, 7, "default"},
		{"yaml-wins", 0, false, "", intPtr(5), 0, 5, "yaml"},
		{"default-wins", 0, false, "", nil, 3, 3, "default"},
		{"default-zero", 0, false, "", nil, 0, 0, "default"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v, tier, src := resolveIntField(tc.flag, tc.flagSet, tc.env, tc.yaml, tc.def)
			if v != tc.wantVal {
				t.Errorf("value=%d want %d", v, tc.wantVal)
			}
			if tier != tc.wantTier {
				t.Errorf("tier=%q want %q", tier, tc.wantTier)
			}
			if src != tc.wantTier {
				t.Errorf("src=%q want %q", src, tc.wantTier)
			}
		})
	}
}

// TestResolveStringField covers the four-tier resolution for string fields.
func TestResolveStringField(t *testing.T) {
	cases := []struct {
		name     string
		flag     string
		env      string
		yaml     string
		def      string
		wantVal  string
		wantTier string
	}{
		{"flag-wins", "f", "e", "y", "d", "f", "flag"},
		{"env-wins", "", "e", "y", "d", "e", "env"},
		{"yaml-wins", "", "", "y", "d", "y", "yaml"},
		{"default-wins", "", "", "", "d", "d", "default"},
		{"all-empty", "", "", "", "", "", "default"},
		{"flag-wins-over-all", "f", "e", "y", "d", "f", "flag"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v, tier, src := resolveStringField(tc.flag, tc.env, tc.yaml, tc.def)
			if v != tc.wantVal {
				t.Errorf("value=%q want %q", v, tc.wantVal)
			}
			if tier != tc.wantTier {
				t.Errorf("tier=%q want %q", tier, tc.wantTier)
			}
			if src != tc.wantTier {
				t.Errorf("src=%q want %q", src, tc.wantTier)
			}
		})
	}
}

// TestLoadConfig_LegacyTsnetKeysPermissive pins the EPIC-272 config
// compatibility contract: server.yaml files written before the tsnet fields
// were deleted (tsnet = true, tsnet_authkey, ...) must keep loading because
// TOML decoding is permissive (unknown keys are ignored, not rejected).
func TestLoadConfig_LegacyTsnetKeysPermissive(t *testing.T) {
	cfg, err := LoadConfig(context.Background(), "testdata/legacy_tsnet_keys.toml")
	if err != nil {
		t.Fatalf("legacy config with tsnet keys failed to load: %v", err)
	}
	sc := cfg.Server
	if sc.Token != "test-token" {
		t.Errorf("Token=%q want %q", sc.Token, "test-token")
	}
	if sc.LogFile != "/tmp/linkari-test.log" {
		t.Errorf("LogFile=%q want %q", sc.LogFile, "/tmp/linkari-test.log")
	}
	if sc.NotifyMinScore != 5 {
		t.Errorf("NotifyMinScore=%d want 5", sc.NotifyMinScore)
	}

	t.Run("file-not-found-returns-error", func(t *testing.T) {
		_, err := LoadConfig(context.Background(), "testdata/does_not_exist.toml")
		if err == nil {
			t.Error("expected error for missing file, got nil")
		}
	})
}
