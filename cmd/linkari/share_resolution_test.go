package main

import (
	"testing"
)

// EPIC-052: exhaustive coverage of the four branches of resolveShareAction.
// The helper is pure, so these tests avoid any Queue/Server wiring and
// exercise the decision tree directly on a hand-built cfgIndex.

func testCfgIndex() map[string]*ActionConfig {
	profiles := []string{"eng", "life", "travel", "fashion", "music", "finance", "dining"}
	idx := make(map[string]*ActionConfig, len(profiles))
	for _, p := range profiles {
		id := "uinit_" + p
		idx[id] = &ActionConfig{
			ID:         id,
			Kind:       KindTemplate,
			ProfileMap: "prefix",
			Target:     "linkari:0",
		}
	}
	return idx
}

// Branch 4: known action — caller-wins. Every registered action resolves to
// itself regardless of the heuristic-override flag. This is the invariant
// from M2: received_action is never rewritten once it's recognized.
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

// Branch 4 sub-case: profile is inferred from the action ID prefix when the
// caller sends action but leaves profile blank. This guards the prefix
// profile_map contract.
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

// Branch 2: bare "uinit" — deterministically pinned to the first uinit_*
// action in lexicographic order. This is the failure mode the epic calls
// "Footgun 3" — the previous map-iteration fallback was non-deterministic.
func TestResolveShareAction_BareUinitPinned(t *testing.T) {
	idx := testCfgIndex()
	// Run many times: a map iteration-based fallback would non-deterministically
	// pick different candidates; the sort-based pin always picks uinit_dining
	// (lexicographically first among the seven test profiles).
	const iters = 50
	for i := 0; i < iters; i++ {
		req := &ShareRequest{Action: "uinit", Type: "url", URL: "https://example.com"}
		got := resolveShareAction(req, idx, false)
		if got.ResolvedAction != "uinit_dining" {
			t.Fatalf("iter %d: got resolved_action=%q want uinit_dining (deterministic pin)", i, got.ResolvedAction)
		}
		if got.ResolvedProfile != "dining" {
			t.Errorf("iter %d: got profile=%q want dining", i, got.ResolvedProfile)
		}
		if got.Reason != "bare_uinit_pinned:uinit_dining" {
			t.Errorf("iter %d: got reason=%q want bare_uinit_pinned:uinit_dining", i, got.Reason)
		}
	}
}

// Branch 3: unknown "uinit_<profile>" — e.g. a profile that isn't registered
// in the config index. The helper preserves the caller's profile name and
// pins the action to the deterministic default. This models a future profile
// rollout where the Android client ships action=uinit_recipes before the
// server config knows about "recipes".
func TestResolveShareAction_UnknownUinitProfilePinned(t *testing.T) {
	idx := testCfgIndex()
	req := &ShareRequest{Action: "uinit_recipes", Type: "url", URL: "https://example.com"}
	got := resolveShareAction(req, idx, false)
	if got.ResolvedProfile != "recipes" {
		t.Errorf("got profile=%q want recipes (profile preserved for unknown uinit_* prefix)", got.ResolvedProfile)
	}
	if got.ResolvedAction != "uinit_dining" {
		t.Errorf("got action=%q want uinit_dining (deterministic pin)", got.ResolvedAction)
	}
	if got.Reason != "unknown_uinit_profile_pinned:uinit_dining" {
		t.Errorf("got reason=%q want unknown_uinit_profile_pinned:uinit_dining", got.Reason)
	}
}

// Branch 1: empty Action — the helper falls back to req.Type. Legacy ingress
// paths that only send type="url" still resolve through the helper without
// ending up in one of the uinit_* pin branches. Reason stays empty because no
// override happened.
func TestResolveShareAction_EmptyActionFallsBackToType(t *testing.T) {
	idx := testCfgIndex()
	req := &ShareRequest{Action: "", Type: "url", Profile: "eng", URL: "https://example.com"}
	got := resolveShareAction(req, idx, false)
	if got.ResolvedAction != "url" {
		t.Errorf("got action=%q want url (fallback to req.Type)", got.ResolvedAction)
	}
	if got.ResolvedProfile != "eng" {
		t.Errorf("got profile=%q want eng (preserved)", got.ResolvedProfile)
	}
	if got.Reason != "" {
		t.Errorf("empty-action fallback should leave Reason empty, got %q", got.Reason)
	}
	if got.ReceivedAction != "" {
		t.Errorf("received_action should echo input (empty), got %q", got.ReceivedAction)
	}
}

// Invariant: heuristicOverrideEnabled=true is currently a no-op because no
// heuristic is registered in the helper. This test pins that contract so a
// future heuristic rollout has to delete or modify this test on purpose —
// making the behavior change visible in code review.
func TestResolveShareAction_HeuristicOverrideFlagIsNoOpToday(t *testing.T) {
	idx := testCfgIndex()
	req := &ShareRequest{Action: "uinit_eng", Profile: "eng", URL: "https://github.com/golang/go"}
	off := resolveShareAction(req, idx, false)
	on := resolveShareAction(req, idx, true)
	if off.ResolvedAction != on.ResolvedAction || off.ResolvedProfile != on.ResolvedProfile {
		t.Errorf("flag flip changed resolution: off=%+v on=%+v", off, on)
	}
}

// Regression test (M5): 7 profiles × 5 representative URLs must round-trip
// cleanly through resolveShareAction. For every (action, url) input, the
// resolved action must equal the input action — no exceptions. This is the
// structural guarantee that closes the EPIC-052 class of bug even if M3 can't
// reproduce the specific incident on-device.
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

	// Dedicated bare-"uinit" case required by M5.
	bare := &ShareRequest{Action: "uinit", Type: "url", URL: "https://example.com"}
	got := resolveShareAction(bare, idx, false)
	if got.ResolvedAction == "uinit" {
		t.Error("bare uinit should be pinned to a concrete uinit_<profile> action, not left as bare")
	}
	if got.Reason == "" {
		t.Error("bare uinit pin must record a resolution reason")
	}
}
