package main

import (
	"testing"
)

// EPIC-052: coverage of resolveShareAction caller-wins invariant.
// EPIC-060 M2: branches 1–3 (empty-action fallback, bare-"uinit" pin,
// unknown-"uinit_<profile>" pin) removed. All uinit_* actions are now in
// cfgIndex as ServerScore=true and resolve via the caller-wins branch directly.

func testCfgIndex() map[string]*ActionConfig {
	profiles := []string{"eng", "life", "travel", "fashion", "music", "finance", "dining"}
	idx := make(map[string]*ActionConfig, len(profiles)*2)
	for _, p := range profiles {
		id := "uinit_" + p
		idx[id] = &ActionConfig{
			ID:          id,
			Kind:        KindTemplate,
			ProfileMap:  "prefix",
			Target:      "linkari:0",
			ServerScore: true,
		}
	}
	// EPIC-057: ginit_* actions use the same profile prefix scheme.
	for _, p := range profiles {
		id := "ginit_" + p
		idx[id] = &ActionConfig{
			ID:              id,
			Kind:            KindTemplate,
			ProfileMap:      "prefix",
			Target:          "linkari:0",
			CommandTemplate: "ginit {{.Text}}",
			AutoScore:       true,
		}
	}
	return idx
}

// Caller-wins: every registered action resolves to itself regardless of the
// heuristic-override flag. This is the invariant from EPIC-052 M2: received_action
// is never rewritten once it's recognized.
func TestResolveShareAction_CallerWins(t *testing.T) {
	idx := testCfgIndex()
	cases := []struct {
		action  string
		profile string
	}{
		{"uinit_eng", "eng"},
		{"uinit_life", "life"},
		{"uinit_travel", "travel"},
		{"uinit_fashion", "fashion"},
		{"uinit_music", "music"},
		{"uinit_finance", "finance"},
		{"uinit_dining", "dining"},
		// EPIC-057: ginit_* actions also resolve via caller-wins.
		{"ginit_eng", "eng"},
		{"ginit_life", "life"},
		{"ginit_travel", "travel"},
		{"ginit_fashion", "fashion"},
		{"ginit_music", "music"},
		{"ginit_finance", "finance"},
		{"ginit_dining", "dining"},
	}
	for _, c := range cases {
		req := &ShareRequest{Action: c.action, Profile: c.profile, Type: "url", URL: "https://example.com"}
		for _, flag := range []bool{false, true} {
			got := resolveShareAction(req, idx, flag)
			if got.ResolvedAction != c.action {
				t.Errorf("action=%s flag=%v: got resolved_action=%q want %q", c.action, flag, got.ResolvedAction, c.action)
			}
			if got.ResolvedProfile != c.profile {
				t.Errorf("action=%s flag=%v: got resolved_profile=%q want %q", c.action, flag, got.ResolvedProfile, c.profile)
			}
			if got.Reason != "" {
				t.Errorf("action=%s: caller-wins path should have empty Reason, got %q", c.action, got.Reason)
			}
		}
	}
}

// Profile is inferred from the action ID prefix when the caller sends action
// but leaves profile blank. Guards the prefix profile_map contract.
func TestResolveShareAction_InferProfileFromPrefix(t *testing.T) {
	idx := testCfgIndex()
	req := &ShareRequest{Action: "uinit_finance", Type: "url", URL: "https://example.com"}
	got := resolveShareAction(req, idx, false)
	if got.ResolvedProfile != "finance" {
		t.Errorf("got profile=%q want finance", got.ResolvedProfile)
	}
	if got.ResolvedAction != "uinit_finance" {
		t.Errorf("got action=%q want uinit_finance", got.ResolvedAction)
	}
}

// Unknown action — returned as-is; Route fails fast on lookup miss.
func TestResolveShareAction_UnknownActionPassThrough(t *testing.T) {
	idx := testCfgIndex()
	req := &ShareRequest{Action: "unknown_action", Type: "url", URL: "https://example.com", Profile: "eng"}
	got := resolveShareAction(req, idx, false)
	if got.ResolvedAction != "unknown_action" {
		t.Errorf("got action=%q want unknown_action (pass-through)", got.ResolvedAction)
	}
	if got.Reason != "" {
		t.Errorf("pass-through should leave Reason empty, got %q", got.Reason)
	}
}

// Invariant: heuristicOverrideEnabled=true is currently a no-op because no
// heuristic is registered in the helper. This test pins that contract so a
// future heuristic rollout has to delete or modify this test on purpose.
func TestResolveShareAction_HeuristicOverrideFlagIsNoOpToday(t *testing.T) {
	idx := testCfgIndex()
	req := &ShareRequest{Action: "uinit_eng", Profile: "eng", URL: "https://github.com/golang/go"}
	off := resolveShareAction(req, idx, false)
	on := resolveShareAction(req, idx, true)
	if off.ResolvedAction != on.ResolvedAction || off.ResolvedProfile != on.ResolvedProfile {
		t.Errorf("flag flip changed resolution: off=%+v on=%+v", off, on)
	}
}

// Regression test (M5 / EPIC-060 M2): 7 profiles × 5 representative URLs must
// round-trip cleanly through resolveShareAction. For every (action, url) input,
// the resolved action must equal the input action — no exceptions. This is the
// structural guarantee that closes the EPIC-052 class of bug.
func TestShareActionRoundTrip(t *testing.T) {
	idx := testCfgIndex()
	profiles := []string{"eng", "life", "travel", "fashion", "music", "finance", "dining"}
	urls := []string{
		"https://github.com/golang/go",
		"https://arxiv.org/abs/1706.03762",
		"https://www.nytimes.com/section/food",
		"https://www.bloomberg.com/markets",
		"https://open.spotify.com/track/abc",
	}
	for _, p := range profiles {
		action := "uinit_" + p
		for _, u := range urls {
			req := &ShareRequest{Action: action, Profile: p, Type: "url", URL: u}
			got := resolveShareAction(req, idx, false)
			if got.ResolvedAction != action {
				t.Errorf("round-trip violation: profile=%s url=%s resolved=%q want %q",
					p, u, got.ResolvedAction, action)
			}
			if got.ResolvedProfile != p {
				t.Errorf("round-trip profile drift: profile=%s url=%s resolved=%q want %q",
					p, u, got.ResolvedProfile, p)
			}
			if got.Reason != "" {
				t.Errorf("round-trip should not emit override reason: got %q for profile=%s url=%s",
					got.Reason, p, u)
			}
		}
	}
}
