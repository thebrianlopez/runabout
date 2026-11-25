package main

import "fmt"

// resolveActionID resolves an action id against the loaded action set.
//
// Policy (POMO queue-admission-validation-gap):
//  1. An id with an exact action definition passes through unchanged.
//  2. A legacy score-intent id (uinit_*, intent "score") aliases to uinit_auto
//     when it is defined, so server-score rows route instead of dead-ending
//     on the replay lookup (20261124 incident, row 28992).
//  3. A legacy non-score id (ginit_* capture, vnote_auto transcribe) passes
//     through unchanged: intent routing carries the semantics and capture
//     branches resolve by intent, not by ActionConfig lookup.
//  4. Anything else is unresolvable: ErrActionNotFound. Callers must treat
//     that as BLOCKED, never terminal-failed (see replay.go and Enqueue).
func resolveActionID(id string, actions map[string]*ActionConfig) (string, error) {
	if id == "" {
		return "", fmt.Errorf("action %q: %w", id, ErrActionNotFound)
	}
	if actions[id] != nil {
		return id, nil
	}
	if legacy, ok := actionIntentMap[id]; ok {
		if legacy.Intent == "score" && actions["uinit_auto"] != nil {
			return "uinit_auto", nil
		}
		return id, nil
	}
	return "", fmt.Errorf("action %q: %w", id, ErrActionNotFound)
}

// revalidateBlocked returns resolvable blocked rows to replay admission.
func (q *Queue) revalidateBlocked(actions map[string]*ActionConfig) error {
	rows, err := q.db.Query("SELECT id, action FROM queue WHERE status='blocked' AND error_reason='action_missing'")
	if err != nil {
		return err
	}
	type item struct {
		id     int64
		action string
	}
	var items []item
	for rows.Next() {
		var it item
		if err := rows.Scan(&it.id, &it.action); err != nil {
			rows.Close()
			return err
		}
		items = append(items, it)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, it := range items {
		if action, err := resolveActionID(it.action, actions); err == nil {
			if _, err := q.db.Exec("UPDATE queue SET action=?, status='pending', error_reason='' WHERE id=? AND status='blocked'", action, it.id); err != nil {
				return err
			}
		}
	}
	return nil
}
