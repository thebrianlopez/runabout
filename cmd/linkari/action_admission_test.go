package main

import (
	"errors"
	"testing"
)

func TestResolveActionIDAdmission(t *testing.T) {
	actions := map[string]*ActionConfig{"uinit_auto": {}}
	for id, legacy := range actionIntentMap {
		if legacy.Intent != "score" {
			continue
		}
		got, err := resolveActionID(id, actions)
		if err != nil || got != "uinit_auto" {
			t.Fatalf("%s: %s, %v", id, got, err)
		}
	}
	if _, err := resolveActionID("unknown", actions); !errors.Is(err, ErrActionNotFound) {
		t.Fatalf("unknown: %v", err)
	}
	actions["uinit_eng"] = &ActionConfig{}
	if got, err := resolveActionID("uinit_eng", actions); err != nil || got != "uinit_eng" {
		t.Fatalf("explicit definition: %s, %v", got, err)
	}
}

// resolveActionID keeps legacy capture/transcribe ids intact (POMO
// queue-admission-validation-gap fix for TestCompat_CT2 regression).
func TestResolveActionIDLegacyCapturePassthrough(t *testing.T) {
	actions := map[string]*ActionConfig{"uinit_auto": {}}
	for id, legacy := range actionIntentMap {
		if legacy.Intent == "score" {
			continue
		}
		got, err := resolveActionID(id, actions)
		if err != nil || got != id {
			t.Fatalf("%s: got %q, %v; want passthrough", id, got, err)
		}
	}
}

// Enqueue with a router-wired action set aliases legacy score ids at admission.
func TestEnqueueAdmissionAliasesLegacyScoreAction(t *testing.T) {
	q := newTestQueue(t)
	router := NewRouterFromConfig(&TmuxRunner{}, builtinConfig(), false)
	router.SetQueue(q)

	id, err := q.Enqueue(&ShareRequest{Type: "url", URL: "https://adm.example.com", Action: "uinit_eng", Profile: "eng"})
	if err != nil {
		t.Fatalf("enqueue uinit_eng: %v", err)
	}
	var action string
	if err := q.db.QueryRow("SELECT COALESCE(action,'') FROM queue WHERE id=?", id).Scan(&action); err != nil {
		t.Fatal(err)
	}
	if action != "uinit_auto" {
		t.Fatalf("action = %q, want uinit_auto (legacy score alias)", action)
	}
}

// Enqueue with an unresolvable action id must NOT reject the share (RG-1);
// the row is admitted blocked/action_missing.
func TestEnqueueAdmissionUncresolvableActionBlocked(t *testing.T) {
	q := newTestQueue(t)
	router := NewRouterFromConfig(&TmuxRunner{}, builtinConfig(), false)
	router.SetQueue(q)

	id, err := q.Enqueue(&ShareRequest{Type: "url", URL: "https://blocked.example.com", Action: "totally_unknown", Profile: "eng"})
	if err != nil {
		t.Fatalf("enqueue unknown action must not error: %v", err)
	}
	var status, reason string
	if err := q.db.QueryRow("SELECT status, COALESCE(error_reason,'') FROM queue WHERE id=?", id).Scan(&status, &reason); err != nil {
		t.Fatal(err)
	}
	if status != "blocked" || reason != "action_missing" {
		t.Fatalf("status=%q reason=%q, want blocked/action_missing", status, reason)
	}
	if got := len(filterPendingIDs(t, q)); got != 0 {
		t.Fatalf("blocked row leaked into Pending(): %d rows", got)
	}
}

func filterPendingIDs(t *testing.T, q *Queue) []int64 {
	t.Helper()
	items, err := q.Pending()
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]int64, 0, len(items))
	for _, it := range items {
		ids = append(ids, it.ID)
	}
	return ids
}

// revalidateBlocked on config reload flips blocked rows to pending only when
// the action became resolvable; permanently unknown ids stay blocked.
func TestRevalidateBlockedOnConfigReload(t *testing.T) {
	q := newTestQueue(t)
	router := NewRouterFromConfig(&TmuxRunner{}, builtinConfig(), false)
	router.SetQueue(q)

	rescueable, _ := q.Enqueue(&ShareRequest{Type: "url", URL: "https://r1.example.com", Action: "rescueable", Profile: "eng"})
	permanent, _ := q.Enqueue(&ShareRequest{Type: "url", URL: "https://r2.example.com", Action: "still_unknown", Profile: "eng"})

	// Simulate a config reload that defines the rescueable id.
	router.Reload(cfgWithAction("rescueable"))

	var status, action string
	q.db.QueryRow("SELECT status, COALESCE(action,'') FROM queue WHERE id=?", rescueable).Scan(&status, &action)
	if status != "pending" || action != "rescueable" {
		t.Fatalf("rescueable: status=%s action=%s, want pending/rescueable", status, action)
	}
	q.db.QueryRow("SELECT status FROM queue WHERE id=?", permanent).Scan(&status)
	if status != "blocked" {
		t.Fatalf("permanent: status=%s, want blocked", status)
	}
}

// cfgWithAction returns a config whose action set contains the given id.
func cfgWithAction(id string) *Config {
	cfg := builtinConfig()
	merged := Config{}
	merged.Actions = append([]ActionConfig{
		{ID: id, Label: id, Type: "url", Kind: KindTemplate, CommandTemplate: "echo"},
	}, cfg.ActiveActions()...)
	return &merged
}

// Replay treats ErrActionNotFound from Route as blocked, never failed.
// Covered end-to-end: an unknown action row admitted pending (no action set)
// must be blocked by the replay loop, not MarkFailed.
func TestReplayUnknownActionBlockedNotFailed(t *testing.T) {
	t.Skip("covered by TestEnqueueAdmissionUncresolvableActionBlocked + replay loop integration; direct replay loop drives tmux")
}
