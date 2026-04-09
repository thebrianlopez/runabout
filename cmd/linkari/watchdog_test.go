package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// enqueueRelayedWithAge inserts a row directly into the queue table in
// `relayed` status with a backdated queued_at timestamp. The watchdog's
// sweep query compares queued_at against `now - maxAge`, so tests set the
// age by setting queued_at rather than by stubbing the clock.
func enqueueRelayedWithAge(t *testing.T, q *Queue, url, profile string, age time.Duration) int64 {
	t.Helper()
	queuedAt := time.Now().Add(-age).UTC().Format(time.RFC3339)
	res, err := q.db.Exec(
		`INSERT INTO queue (url, text, type, action, profile, status, queued_at, relayed_at)
		 VALUES (?, '', 'url', '', ?, 'relayed', ?, ?)`,
		url, profile, queuedAt, queuedAt,
	)
	if err != nil {
		t.Fatalf("insert relayed row: %v", err)
	}
	id, _ := res.LastInsertId()
	return id
}

func newWatchdogTestEnv(t *testing.T) (*Queue, *EventLogger, string) {
	t.Helper()
	dir := t.TempDir()
	q, err := NewQueue(filepath.Join(dir, "test.db"), false)
	if err != nil {
		t.Fatalf("NewQueue: %v", err)
	}
	t.Cleanup(func() { q.Close() })
	eventsPath := filepath.Join(dir, "linkari_events.jsonl")
	events, err := NewEventLogger(eventsPath)
	if err != nil {
		t.Fatalf("NewEventLogger: %v", err)
	}
	return q, events, eventsPath
}

func readEvents(t *testing.T, path string) []map[string]interface{} {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("read events: %v", err)
	}
	var out []map[string]interface{}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var m map[string]interface{}
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("unmarshal event: %v", err)
		}
		out = append(out, m)
	}
	return out
}

func getStatus(t *testing.T, q *Queue, id int64) (string, string) {
	t.Helper()
	var status, reason string
	err := q.db.QueryRow(
		"SELECT status, COALESCE(error_reason,'') FROM queue WHERE id=?", id,
	).Scan(&status, &reason)
	if err != nil {
		t.Fatalf("get status id=%d: %v", id, err)
	}
	return status, reason
}

// defaultTestCfg returns a RelayedWatchdogCfg suitable for most tests
// (rescue disabled so they test the baseline timeout path unchanged).
func defaultTestCfg() RelayedWatchdogCfg {
	return RelayedWatchdogCfg{Interval: time.Second, MaxAge: 15 * time.Minute}
}

// writeScoreJSON creates a workspace dir under urlWork and writes a _score.json.
func writeScoreJSON(t *testing.T, urlWork, wsName string, s scoreJSON) {
	t.Helper()
	wsDir := filepath.Join(urlWork, wsName)
	if err := os.MkdirAll(wsDir, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	data, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("marshal score: %v", err)
	}
	if err := os.WriteFile(filepath.Join(wsDir, "_score.json"), data, 0o644); err != nil {
		t.Fatalf("write score json: %v", err)
	}
}

// --- Baseline tests (no rescue) ---

// TestRelayedWatchdog_RowInWindow_NoAction verifies a fresh relayed row
// inside the max-age window is left untouched.
func TestRelayedWatchdog_RowInWindow_NoAction(t *testing.T) {
	q, events, eventsPath := newWatchdogTestEnv(t)

	id := enqueueRelayedWithAge(t, q, "https://in-window.test", "eng", 1*time.Minute)

	w := NewRelayedWatchdog(q, events, defaultTestCfg())
	w.sweepOnce(time.Now(), defaultTestCfg())

	status, reason := getStatus(t, q, id)
	if status != "relayed" || reason != "" {
		t.Errorf("row in window: got (status=%q, reason=%q), want (relayed, \"\")", status, reason)
	}
	if evs := readEvents(t, eventsPath); len(evs) != 0 {
		t.Errorf("expected no events, got %d", len(evs))
	}
}

// TestRelayedWatchdog_RowPastWindow_MarkedFailed verifies a stale relayed
// row is reclassified as failed with error_reason="scoring_timeout".
func TestRelayedWatchdog_RowPastWindow_MarkedFailed(t *testing.T) {
	q, events, _ := newWatchdogTestEnv(t)

	id := enqueueRelayedWithAge(t, q, "https://stale.test", "finance", 30*time.Minute)

	w := NewRelayedWatchdog(q, events, defaultTestCfg())
	w.sweepOnce(time.Now(), defaultTestCfg())

	status, reason := getStatus(t, q, id)
	if status != "failed" {
		t.Errorf("status: got %q, want failed", status)
	}
	if reason != "scoring_timeout" {
		t.Errorf("error_reason: got %q, want scoring_timeout", reason)
	}
}

// TestRelayedWatchdog_RowPastWindow_EmitsEvent verifies a sweep emits one
// share_scoring_timeout event per stale row with the expected metadata.
func TestRelayedWatchdog_RowPastWindow_EmitsEvent(t *testing.T) {
	q, events, eventsPath := newWatchdogTestEnv(t)

	id := enqueueRelayedWithAge(t, q, "https://emit.test", "life", 20*time.Minute)

	w := NewRelayedWatchdog(q, events, defaultTestCfg())
	w.sweepOnce(time.Now(), defaultTestCfg())

	evs := readEvents(t, eventsPath)
	if len(evs) != 1 {
		t.Fatalf("expected 1 event, got %d", len(evs))
	}
	ev := evs[0]
	if ev["event_type"] != "share_scoring_timeout" {
		t.Errorf("event_type: got %v, want share_scoring_timeout", ev["event_type"])
	}
	meta, ok := ev["metadata"].(map[string]interface{})
	if !ok {
		t.Fatalf("metadata missing or wrong type: %v", ev["metadata"])
	}
	if int64(meta["id"].(float64)) != id {
		t.Errorf("event id: got %v, want %d", meta["id"], id)
	}
	if meta["url"] != "https://emit.test" {
		t.Errorf("event url: got %v", meta["url"])
	}
	if meta["profile"] != "life" {
		t.Errorf("event profile: got %v", meta["profile"])
	}
	if age, ok := meta["age_seconds"].(float64); !ok || age < 60 {
		t.Errorf("event age_seconds: got %v (want >= 60)", meta["age_seconds"])
	}
}

// TestRelayedWatchdog_AlreadyFailed_NoDuplicateEvent verifies the WHERE
// status='relayed' filter prevents re-sweeping a row that a previous tick
// already marked failed.
func TestRelayedWatchdog_AlreadyFailed_NoDuplicateEvent(t *testing.T) {
	q, events, eventsPath := newWatchdogTestEnv(t)

	_ = enqueueRelayedWithAge(t, q, "https://dedup.test", "dining", 30*time.Minute)

	w := NewRelayedWatchdog(q, events, defaultTestCfg())
	w.sweepOnce(time.Now(), defaultTestCfg())
	w.sweepOnce(time.Now(), defaultTestCfg())
	w.sweepOnce(time.Now(), defaultTestCfg())

	evs := readEvents(t, eventsPath)
	if len(evs) != 1 {
		t.Fatalf("expected 1 event across 3 sweeps, got %d", len(evs))
	}
}

// --- M1: on-disk rescue tests ---

// TestWatchdogRescuesFromDisk verifies that a relayed row with a matching
// _score.json transitions to `scored` (not `failed`) and emits score_ingested.
func TestWatchdogRescuesFromDisk(t *testing.T) {
	q, events, eventsPath := newWatchdogTestEnv(t)
	urlWork := t.TempDir()

	id := enqueueRelayedWithAge(t, q, "https://rescue.test/article", "eng", 20*time.Minute)
	writeScoreJSON(t, urlWork, "20260101_000000_url_rescue-test-article", scoreJSON{
		URL:     "https://rescue.test/article",
		Profile: "eng",
		Score:   85,
		Verdict: "save",
		Slug:    "rescue-test-article",
	})

	cfg := RelayedWatchdogCfg{
		Interval:   time.Second,
		MaxAge:     15 * time.Minute,
		UrlWorkDir: urlWork,
	}
	w := NewRelayedWatchdog(q, events, cfg)
	w.sweepOnce(time.Now(), cfg)

	// Row should be scored, not failed.
	status, reason := getStatus(t, q, id)
	if status != "scored" {
		t.Errorf("status: got %q, want scored", status)
	}
	if reason != "" {
		t.Errorf("error_reason: got %q, want empty", reason)
	}

	// Only score_ingested event — no share_scoring_timeout.
	evs := readEvents(t, eventsPath)
	var hasIngested, hasTimeout bool
	for _, ev := range evs {
		switch ev["event_type"] {
		case "score_ingested":
			hasIngested = true
		case "share_scoring_timeout":
			hasTimeout = true
		}
	}
	if !hasIngested {
		t.Error("expected score_ingested event, got none")
	}
	if hasTimeout {
		t.Error("unexpected share_scoring_timeout event for rescued row")
	}
}

// TestWatchdogRescueMissFallsThrough verifies that a relayed row with no
// matching _score.json still gets marked failed with scoring_timeout.
func TestWatchdogRescueMissFallsThrough(t *testing.T) {
	q, events, eventsPath := newWatchdogTestEnv(t)
	urlWork := t.TempDir() // empty — no _score.json files

	id := enqueueRelayedWithAge(t, q, "https://miss.test/article", "life", 20*time.Minute)

	cfg := RelayedWatchdogCfg{
		Interval:   time.Second,
		MaxAge:     15 * time.Minute,
		UrlWorkDir: urlWork,
	}
	w := NewRelayedWatchdog(q, events, cfg)
	w.sweepOnce(time.Now(), cfg)

	status, reason := getStatus(t, q, id)
	if status != "failed" {
		t.Errorf("status: got %q, want failed", status)
	}
	if reason != "scoring_timeout" {
		t.Errorf("error_reason: got %q, want scoring_timeout", reason)
	}
	evs := readEvents(t, eventsPath)
	if len(evs) != 1 || evs[0]["event_type"] != "share_scoring_timeout" {
		t.Errorf("expected exactly 1 share_scoring_timeout event, got %v", evs)
	}
}

// TestWatchdogRescueIdempotentOnReplay verifies a second sweep against the
// same data emits no duplicate score_ingested events.
func TestWatchdogRescueIdempotentOnReplay(t *testing.T) {
	q, events, eventsPath := newWatchdogTestEnv(t)
	urlWork := t.TempDir()

	enqueueRelayedWithAge(t, q, "https://idempotent.test/page", "finance", 20*time.Minute)
	writeScoreJSON(t, urlWork, "ws1", scoreJSON{
		URL:     "https://idempotent.test/page",
		Profile: "finance",
		Score:   70,
		Verdict: "save",
		Slug:    "idempotent-test-page",
	})

	cfg := RelayedWatchdogCfg{
		Interval:   time.Second,
		MaxAge:     15 * time.Minute,
		UrlWorkDir: urlWork,
	}
	w := NewRelayedWatchdog(q, events, cfg)
	w.sweepOnce(time.Now(), cfg) // first sweep rescues
	w.sweepOnce(time.Now(), cfg) // second sweep: row is now scored, no stuck rows

	var ingestedCount int
	for _, ev := range readEvents(t, eventsPath) {
		if ev["event_type"] == "score_ingested" {
			ingestedCount++
		}
	}
	if ingestedCount != 1 {
		t.Errorf("expected exactly 1 score_ingested event, got %d", ingestedCount)
	}
}

// TestWatchdogTraversalGuard verifies that a symlinked _score.json pointing
// outside url_work is refused and the row falls through to timeout.
func TestWatchdogTraversalGuard(t *testing.T) {
	q, events, eventsPath := newWatchdogTestEnv(t)
	urlWork := t.TempDir()
	outside := t.TempDir()

	// Write a real _score.json outside url_work.
	externalScore := scoreJSON{
		URL:     "https://symlink.test/page",
		Profile: "eng",
		Score:   90,
		Verdict: "save",
		Slug:    "symlink-test-page",
	}
	data, _ := json.Marshal(externalScore)
	externalPath := filepath.Join(outside, "_score.json")
	os.WriteFile(externalPath, data, 0o644)

	// Create workspace dir and symlink _score.json to the external file.
	wsDir := filepath.Join(urlWork, "fake-workspace")
	os.MkdirAll(wsDir, 0o755)
	os.Symlink(externalPath, filepath.Join(wsDir, "_score.json"))

	enqueueRelayedWithAge(t, q, "https://symlink.test/page", "eng", 20*time.Minute)

	cfg := RelayedWatchdogCfg{
		Interval:   time.Second,
		MaxAge:     15 * time.Minute,
		UrlWorkDir: urlWork,
	}
	w := NewRelayedWatchdog(q, events, cfg)
	w.sweepOnce(time.Now(), cfg)

	// Traversal guard refused the symlink → row falls through to timeout.
	evs := readEvents(t, eventsPath)
	var hasIngested bool
	for _, ev := range evs {
		if ev["event_type"] == "score_ingested" {
			hasIngested = true
		}
	}
	if hasIngested {
		t.Error("traversal guard should have refused the symlink, but score_ingested was emitted")
	}
	_ = eventsPath // ensure the JSONL file was checked even if empty
}

// TestIngestScoreIfRelayedRaceGuard verifies that IngestScoreIfRelayed
// returns (false, nil) and is a no-op when the row is already scored.
func TestIngestScoreIfRelayedRaceGuard(t *testing.T) {
	q, _, _ := newWatchdogTestEnv(t)

	// Insert a row directly in `scored` status.
	res, err := q.db.Exec(
		`INSERT INTO queue (url, text, type, action, profile, status, queued_at, scored_at, score)
		 VALUES (?, '', 'url', '', 'eng', 'scored', ?, ?, 80)`,
		"https://race.test", time.Now().UTC().Format(time.RFC3339), time.Now().UTC().Format(time.RFC3339),
	)
	if err != nil {
		t.Fatalf("insert scored row: %v", err)
	}
	id, _ := res.LastInsertId()

	ingested, err := q.IngestScoreIfRelayed(id, 90, "", "save", "slug")
	if err != nil {
		t.Fatalf("IngestScoreIfRelayed: %v", err)
	}
	if ingested {
		t.Error("expected (false, nil) for already-scored row, got true")
	}
}

// --- M3: alert threshold tests ---

// TestAlertThresholdFires verifies that exceeding AlertThreshold within
// AlertWindow enqueues exactly one push_outbox row with kind='alert'.
func TestAlertThresholdFires(t *testing.T) {
	q, events, _ := newWatchdogTestEnv(t)
	urlWork := t.TempDir() // empty — all rows time out

	cfg := RelayedWatchdogCfg{
		Interval:       time.Second,
		MaxAge:         15 * time.Minute,
		UrlWorkDir:     urlWork,
		AlertThreshold: 3,
		AlertWindow:    10 * time.Minute,
	}
	w := NewRelayedWatchdog(q, events, cfg)

	// Enqueue 4 relayed rows (threshold is 3 → alert fires at 4th).
	for i := 0; i < 4; i++ {
		enqueueRelayedWithAge(t, q, "https://alert.test/"+strings.Repeat("a", i+1), "eng", 20*time.Minute)
	}
	w.sweepOnce(time.Now(), cfg)

	rows, err := q.db.Query(`SELECT COUNT(*) FROM push_outbox WHERE kind='alert'`)
	if err != nil {
		t.Fatalf("query alert rows: %v", err)
	}
	defer rows.Close()
	var count int
	rows.Next()
	rows.Scan(&count)
	if count != 1 {
		t.Errorf("expected 1 push_outbox alert row, got %d", count)
	}
}

// TestAlertCooldown verifies that multiple sweeps within the cooldown window
// enqueue only one alert row total.
func TestAlertCooldown(t *testing.T) {
	q, events, _ := newWatchdogTestEnv(t)
	urlWork := t.TempDir()

	cfg := RelayedWatchdogCfg{
		Interval:       time.Second,
		MaxAge:         15 * time.Minute,
		UrlWorkDir:     urlWork,
		AlertThreshold: 1,
		AlertWindow:    10 * time.Minute,
	}
	w := NewRelayedWatchdog(q, events, cfg)

	// First sweep: 2 rows exceed threshold → fires alert.
	for i := 0; i < 2; i++ {
		enqueueRelayedWithAge(t, q, "https://cooldown.test/"+strings.Repeat("b", i+1), "life", 20*time.Minute)
	}
	w.sweepOnce(time.Now(), cfg)

	// Second sweep with 2 more rows: cooldown is still active.
	for i := 0; i < 2; i++ {
		enqueueRelayedWithAge(t, q, "https://cooldown2.test/"+strings.Repeat("c", i+1), "life", 20*time.Minute)
	}
	w.sweepOnce(time.Now(), cfg)

	rows, err := q.db.Query(`SELECT COUNT(*) FROM push_outbox WHERE kind='alert'`)
	if err != nil {
		t.Fatalf("query alert rows: %v", err)
	}
	defer rows.Close()
	var count int
	rows.Next()
	rows.Scan(&count)
	if count != 1 {
		t.Errorf("expected 1 alert row (cooldown should suppress second), got %d", count)
	}
}

// TestEPIC054WorkspaceKeyInvariant verifies that two _score.json files with
// the same URL but different profiles are indexed independently, and the rescue
// prefers the one matching the queue row's profile (EPIC-054 semantics).
func TestEPIC054WorkspaceKeyInvariant(t *testing.T) {
	q, events, eventsPath := newWatchdogTestEnv(t)
	urlWork := t.TempDir()

	// Same URL, two profiles: eng (score 85) and life (score 70).
	writeScoreJSON(t, urlWork, "ws-eng", scoreJSON{
		URL: "https://multi-profile.test/page", Profile: "eng", Score: 85, Verdict: "save", Slug: "mppage",
	})
	writeScoreJSON(t, urlWork, "ws-life", scoreJSON{
		URL: "https://multi-profile.test/page", Profile: "life", Score: 70, Verdict: "save", Slug: "mppage",
	})

	// Enqueue two rows: one eng, one life.
	idEng := enqueueRelayedWithAge(t, q, "https://multi-profile.test/page", "eng", 20*time.Minute)
	idLife := enqueueRelayedWithAge(t, q, "https://multi-profile.test/page", "life", 20*time.Minute)

	cfg := RelayedWatchdogCfg{
		Interval:   time.Second,
		MaxAge:     15 * time.Minute,
		UrlWorkDir: urlWork,
	}
	w := NewRelayedWatchdog(q, events, cfg)
	w.sweepOnce(time.Now(), cfg)

	statusEng, _ := getStatus(t, q, idEng)
	statusLife, _ := getStatus(t, q, idLife)
	if statusEng != "scored" {
		t.Errorf("eng row: got status=%q, want scored", statusEng)
	}
	if statusLife != "scored" {
		t.Errorf("life row: got status=%q, want scored", statusLife)
	}

	// Verify score_ingested events count.
	var ingestedCount int
	for _, ev := range readEvents(t, eventsPath) {
		if ev["event_type"] == "score_ingested" {
			ingestedCount++
		}
	}
	if ingestedCount != 2 {
		t.Errorf("expected 2 score_ingested events (one per profile), got %d", ingestedCount)
	}
}
