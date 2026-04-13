package main

import (
	"context"
	crand "crypto/rand"
	"database/sql"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"sync/atomic"
	"time"

	_ "modernc.org/sqlite"
)

const maxQueueSize = 200

// validStatuses enumerates every legal status value a client may filter on.
// Used by /queue and /archive query-param validation.
var validStatuses = map[string]bool{
	"pending":  true,
	"relayed":  true,
	"scored":   true,
	"archived": true,
	"failed":   true,
	"all":      true,
}

// QueueItem represents a persisted share request.
type QueueItem struct {
	ID         int64  `json:"id"`
	URL        string `json:"url"`
	Text       string `json:"text"`
	Type       string `json:"type"`
	Action     string `json:"action"`
	Profile    string `json:"profile"`
	Status     string `json:"status"`
	Score      *int   `json:"score,omitempty"`
	Tags       string `json:"tags,omitempty"`
	QueuedAt   string `json:"queued_at"`
	RelayedAt  string `json:"relayed_at,omitempty"`
	ScoredAt   string `json:"scored_at,omitempty"`
	ArchivedAt string `json:"archived_at,omitempty"`
	Verdict    string `json:"verdict,omitempty"`
	Slug       string `json:"slug,omitempty"`
	Progress   string `json:"progress,omitempty"`
	SkipReason string `json:"skip_reason,omitempty"`
	Outcome    string `json:"outcome,omitempty"`
	OutcomeAt  string `json:"outcome_at,omitempty"`
	Feedback   string `json:"feedback,omitempty"`
	FeedbackAt string `json:"feedback_at,omitempty"`
}

// Queue persists share requests in SQLite for deferred replay.
type Queue struct {
	db    *sql.DB
	debug bool

	// pushCfg is the live push config used by EnqueueDigestIfDue.
	// Loaded atomically so SIGHUP reloads (EPIC-051 M6) can swap it in
	// without blocking in-flight writer paths. Nil is treated as the
	// zero-value PushConfig (1h throttle, no min score).
	pushCfg atomic.Pointer[PushConfig]
}

// SetPushConfig atomically swaps in a new push config. Safe to call from
// signal handlers and config-reload goroutines. A nil argument resets to
// the zero-value defaults.
func (q *Queue) SetPushConfig(cfg *PushConfig) {
	q.pushCfg.Store(cfg)
}

// PushConfig returns the currently live push config, or a zero-value
// PushConfig if none has been set.
func (q *Queue) PushConfig() *PushConfig {
	if p := q.pushCfg.Load(); p != nil {
		return p
	}
	return &PushConfig{}
}

// NewQueue opens (or creates) the SQLite database at dbPath.
func NewQueue(dbPath string, debug bool) (*Queue, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open queue db: %w", err)
	}

	// Integrity check before any schema work — catches SQLITE_CORRUPT (11) that
	// would otherwise surface as non-fatal errors during later startup steps
	// (e.g. seed invite codes) and leave all DB-backed endpoints returning 500.
	// Returns an error so the caller (serve command) treats this as fatal and
	// halts rather than accepting traffic with a broken database.
	var integrityResult string
	if err := db.QueryRow("PRAGMA integrity_check").Scan(&integrityResult); err != nil {
		db.Close()
		return nil, fmt.Errorf("queue.db integrity check: %w", err)
	}
	if integrityResult != "ok" {
		db.Close()
		return nil, fmt.Errorf("queue.db is corrupt (%s) — recover with: sqlite3 ~/.config/linkari/queue.db \".recover\" | sqlite3 ~/.config/linkari/queue_recovered.db && mv queue.db queue.db.bak && mv queue_recovered.db queue.db", integrityResult)
	}

	// WAL mode for concurrent reads during replay.
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("set WAL mode: %w", err)
	}

	// FULL synchronous mode: fsync after every WAL write. Slightly slower
	// than the WAL default (NORMAL) but eliminates the corruption window on
	// unclean shutdown (lid-close, OOM kill, force-quit). WAL+NORMAL is safe
	// for data durability but can leave queue.db-wal in a state SQLite cannot
	// recover — the 2026-04-13 SQLITE_CORRUPT (11) incident occurred with WAL
	// on and synchronous at its default.
	if _, err := db.Exec("PRAGMA synchronous=FULL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("set synchronous mode: %w", err)
	}

	const schema = `CREATE TABLE IF NOT EXISTS queue (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		url        TEXT NOT NULL DEFAULT '',
		text       TEXT NOT NULL DEFAULT '',
		type       TEXT NOT NULL,
		action     TEXT NOT NULL DEFAULT '',
		profile    TEXT NOT NULL DEFAULT '',
		status     TEXT NOT NULL DEFAULT 'pending',
		queued_at  TEXT NOT NULL,
		relayed_at TEXT DEFAULT NULL
	)`
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("create queue table: %w", err)
	}

	// Idempotent schema migration — add scoring/archive columns.
	migrations := []string{
		"ALTER TABLE queue ADD COLUMN score INTEGER DEFAULT NULL",
		"ALTER TABLE queue ADD COLUMN tags TEXT DEFAULT ''",
		"ALTER TABLE queue ADD COLUMN scored_at TEXT DEFAULT NULL",
		"ALTER TABLE queue ADD COLUMN archived_at TEXT DEFAULT NULL",
		"ALTER TABLE queue ADD COLUMN verdict TEXT DEFAULT ''",
		"ALTER TABLE queue ADD COLUMN slug TEXT DEFAULT ''",
		// EPIC-054 M3: error_reason column for relayed-state watchdog and
		// other failure classifiers. Nullable, empty string on legacy rows.
		"ALTER TABLE queue ADD COLUMN error_reason TEXT DEFAULT ''",
		// EPIC-067 200MB: progress column for long-running audio transcription.
		"ALTER TABLE queue ADD COLUMN progress TEXT DEFAULT ''",
		// EPIC-070 M1: outcome tracking columns.
		"ALTER TABLE queue ADD COLUMN outcome TEXT DEFAULT NULL",
		"ALTER TABLE queue ADD COLUMN outcome_at TEXT DEFAULT NULL",
		// EPIC-070 M2: score feedback columns.
		"ALTER TABLE queue ADD COLUMN feedback TEXT DEFAULT NULL",
		"ALTER TABLE queue ADD COLUMN feedback_at TEXT DEFAULT NULL",
		// EPIC-070 M4: indexes for filtered archive queries.
		"CREATE INDEX IF NOT EXISTS idx_queue_status_score ON queue(status, score)",
		"CREATE INDEX IF NOT EXISTS idx_queue_status_scored_at ON queue(status, scored_at)",
	}
	for _, m := range migrations {
		db.Exec(m) // Ignore "duplicate column" errors.
	}

	// EPIC-045 M1: push_outbox and devices tables.
	const pushSchema = `
		CREATE TABLE IF NOT EXISTS push_outbox (
			id            INTEGER PRIMARY KEY AUTOINCREMENT,
			score         INTEGER NOT NULL,
			slug          TEXT NOT NULL DEFAULT '',
			verdict       TEXT NOT NULL DEFAULT '',
			url           TEXT NOT NULL DEFAULT '',
			kind          TEXT NOT NULL DEFAULT 'notify',
			status        TEXT NOT NULL DEFAULT 'pending',
			attempts      INTEGER NOT NULL DEFAULT 0,
			next_attempt  INTEGER NOT NULL DEFAULT 0,
			created_at    INTEGER NOT NULL,
			updated_at    INTEGER NOT NULL,
			last_error    TEXT NOT NULL DEFAULT ''
		);
		CREATE INDEX IF NOT EXISTS idx_push_outbox_status_next
			ON push_outbox(status, next_attempt);

		CREATE TABLE IF NOT EXISTS devices (
			token      TEXT PRIMARY KEY,
			updated_at INTEGER NOT NULL
		);
	`
	if _, err := db.Exec(pushSchema); err != nil {
		db.Close()
		return nil, fmt.Errorf("create push_outbox/devices: %w", err)
	}

	// EPIC-051 M2: per-profile throttle support. Add a `profile` column to
	// push_outbox so EnqueueDigestIfDue can query MAX(created_at) scoped to
	// a profile, and add a composite index to keep the throttle query flat
	// as push_outbox grows. Both statements are idempotent.
	pushMigrations := []string{
		"ALTER TABLE push_outbox ADD COLUMN profile TEXT NOT NULL DEFAULT ''",
		"ALTER TABLE push_outbox ADD COLUMN gap_summary TEXT NOT NULL DEFAULT ''",    // EPIC-058 M7
		"ALTER TABLE push_outbox ADD COLUMN content_type TEXT NOT NULL DEFAULT ''", // EPIC-071 M3
	}
	for _, m := range pushMigrations {
		db.Exec(m) // ignore "duplicate column" errors
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_push_outbox_kind_profile_created
		ON push_outbox(kind, profile, created_at)`); err != nil {
		db.Close()
		return nil, fmt.Errorf("create push_outbox kind/profile index: %w", err)
	}

	// EPIC-001: auth tables — users, invite_codes, sessions.
	const authSchema = `
		CREATE TABLE IF NOT EXISTS users (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			google_sub TEXT NOT NULL UNIQUE,
			email      TEXT NOT NULL DEFAULT '',
			name       TEXT NOT NULL DEFAULT '',
			active     INTEGER NOT NULL DEFAULT 1,
			created_at INTEGER NOT NULL,
			updated_at INTEGER NOT NULL
		);

		CREATE TABLE IF NOT EXISTS invite_codes (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			code       TEXT NOT NULL UNIQUE,
			used       INTEGER NOT NULL DEFAULT 0,
			used_by    TEXT DEFAULT NULL,
			used_at    INTEGER DEFAULT NULL,
			created_at INTEGER NOT NULL
		);

		CREATE TABLE IF NOT EXISTS sessions (
			token      TEXT PRIMARY KEY,
			user_id    INTEGER NOT NULL REFERENCES users(id),
			google_sub TEXT NOT NULL,
			created_at INTEGER NOT NULL,
			expires_at INTEGER NOT NULL
		);

		CREATE INDEX IF NOT EXISTS idx_sessions_expires ON sessions(expires_at);
	`
	if _, err := db.Exec(authSchema); err != nil {
		db.Close()
		return nil, fmt.Errorf("create auth tables: %w", err)
	}

	// EPIC-001: add user_id column to devices for session association.
	db.Exec("ALTER TABLE devices ADD COLUMN user_id INTEGER DEFAULT NULL")

	// FTS5 full-text search index over queue content.
	const fts5Setup = `
		CREATE VIRTUAL TABLE IF NOT EXISTS queue_fts
			USING fts5(url, tags, profile, verdict, content='queue', content_rowid='id');

		CREATE TRIGGER IF NOT EXISTS queue_fts_insert AFTER INSERT ON queue BEGIN
			INSERT INTO queue_fts(rowid, url, tags, profile, verdict)
			VALUES (new.id, new.url, COALESCE(new.tags,''), COALESCE(new.profile,''), COALESCE(new.verdict,''));
		END;

		CREATE TRIGGER IF NOT EXISTS queue_fts_update AFTER UPDATE ON queue BEGIN
			INSERT INTO queue_fts(queue_fts, rowid, url, tags, profile, verdict)
			VALUES ('delete', old.id, old.url, COALESCE(old.tags,''), COALESCE(old.profile,''), COALESCE(old.verdict,''));
			INSERT INTO queue_fts(rowid, url, tags, profile, verdict)
			VALUES (new.id, new.url, COALESCE(new.tags,''), COALESCE(new.profile,''), COALESCE(new.verdict,''));
		END;

		CREATE TRIGGER IF NOT EXISTS queue_fts_delete AFTER DELETE ON queue BEGIN
			INSERT INTO queue_fts(queue_fts, rowid, url, tags, profile, verdict)
			VALUES ('delete', old.id, old.url, COALESCE(old.tags,''), COALESCE(old.profile,''), COALESCE(old.verdict,''));
		END;
	`
	if _, err := db.Exec(fts5Setup); err != nil {
		db.Close()
		return nil, fmt.Errorf("create fts5 index: %w", err)
	}

	q := &Queue{db: db, debug: debug}

	// Backfill FTS5 index with any existing rows not yet indexed.
	if err := q.initFTS5(); err != nil {
		slog.Warn("fts5 backfill failed", "error", err)
	}

	if err := q.Prune(); err != nil {
		slog.Warn("queue prune on startup failed", "error", err)
	}
	return q, nil
}

// Enqueue inserts a share request into the queue with status=pending.
func (q *Queue) Enqueue(req *ShareRequest) (int64, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := q.db.Exec(
		`INSERT INTO queue (url, text, type, action, profile, status, queued_at)
		 VALUES (?, ?, ?, ?, ?, 'pending', ?)`,
		req.URL, req.Text, req.Type, req.Action, req.Profile, now,
	)
	if err != nil {
		return 0, fmt.Errorf("enqueue: %w", err)
	}
	id, _ := res.LastInsertId()
	slog.Debug("queue enqueued",
		"event_type", "queue_enqueue",
		"id", id,
		"type", req.Type,
	)
	return id, nil
}

// EnqueueScored inserts a share request as pre-scored (status=scored, score=0).
// Used by auto_score actions (EPIC-057 ginit_*) so the RelayedWatchdog never
// sweeps these rows — they skip the pending→relayed→scored progression.
func (q *Queue) EnqueueScored(req *ShareRequest, verdict string) (int64, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := q.db.Exec(
		`INSERT INTO queue (url, text, type, action, profile, status, score, verdict, queued_at, scored_at)
		 VALUES (?, ?, ?, ?, ?, 'scored', 0, ?, ?, ?)`,
		req.URL, req.Text, req.Type, req.Action, req.Profile, verdict, now, now,
	)
	if err != nil {
		return 0, fmt.Errorf("enqueue scored: %w", err)
	}
	id, _ := res.LastInsertId()
	slog.Debug("queue enqueued (auto-scored)",
		"event_type", "queue_enqueue_scored",
		"id", id,
		"type", req.Type,
		"verdict", verdict,
	)
	return id, nil
}

const queueCols = "id, url, text, type, action, profile, status, COALESCE(score,0), COALESCE(tags,''), queued_at, COALESCE(relayed_at,''), COALESCE(scored_at,''), COALESCE(archived_at,''), COALESCE(verdict,''), COALESCE(slug,''), COALESCE(progress,''), COALESCE(outcome,''), COALESCE(outcome_at,''), COALESCE(feedback,''), COALESCE(feedback_at,'')"

// Pending returns all items with status=pending, ordered by id ASC (FIFO).
func (q *Queue) Pending() ([]QueueItem, error) {
	return q.query("SELECT "+queueCols+" FROM queue WHERE status='pending' ORDER BY id ASC")
}

// List returns items filtered by status (empty string = all), limited to n rows.
func (q *Queue) List(status string, limit int) ([]QueueItem, error) {
	return q.ListCursor(status, math.MaxInt64, limit)
}

// ListCursor returns items filtered by status with id-based cursor pagination.
// status="" or "all" means no status filter. beforeID is exclusive upper bound
// (use math.MaxInt64 for the first page).
func (q *Queue) ListCursor(status string, beforeID int64, limit int) ([]QueueItem, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if beforeID <= 0 {
		beforeID = math.MaxInt64
	}
	if status == "" || status == "all" {
		return q.query("SELECT "+queueCols+" FROM queue WHERE id<? ORDER BY id DESC LIMIT ?", beforeID, limit)
	}
	return q.query("SELECT "+queueCols+" FROM queue WHERE status=? AND id<? ORDER BY id DESC LIMIT ?", status, beforeID, limit)
}

// MarkRelayed updates an item to status=relayed with the current timestamp.
func (q *Queue) MarkRelayed(id int64) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := q.db.Exec("UPDATE queue SET status='relayed', relayed_at=? WHERE id=?", now, id)
	return err
}

// MarkFailed updates an item to status=failed.
func (q *Queue) MarkFailed(id int64) error {
	_, err := q.db.Exec("UPDATE queue SET status='failed' WHERE id=?", id)
	return err
}

// SetText updates the text column for a queue row. Used by scoreAudioAsync
// to backfill the whisper transcript after transcription completes (EPIC-067).
func (q *Queue) SetText(id int64, text string) error {
	_, err := q.db.Exec("UPDATE queue SET text=? WHERE id=?", text, id)
	return err
}

// SetProgress updates the progress column for a queue row. Used by
// scoreAudioAsync to report chunk transcription progress (EPIC-067 200MB).
func (q *Queue) SetProgress(id int64, progress string) error {
	_, err := q.db.Exec("UPDATE queue SET progress=? WHERE id=?", progress, id)
	return err
}

// MarkFailedWithReason sets status=failed and records an error_reason.
// EPIC-054 M3: used by the relayed-state watchdog to classify timeouts.
func (q *Queue) MarkFailedWithReason(id int64, reason string) error {
	_, err := q.db.Exec(
		"UPDATE queue SET status='failed', error_reason=? WHERE id=?",
		reason, id,
	)
	return err
}

// TimedOutRelayed represents a single queue row swept by the watchdog.
// Populated by SweepRelayedTimeouts for the caller to emit provenance events.
type TimedOutRelayed struct {
	ID       int64
	URL      string
	Profile  string
	QueuedAt string
	AgeSecs  int64
}

// SelectStuckRelayed returns relayed rows older than maxAge relative to now,
// without marking them failed. Split from SweepRelayedTimeouts so the watchdog
// can interpose a rescue attempt between selection and fail-marking.
// EPIC-055 M1.
func (q *Queue) SelectStuckRelayed(now time.Time, maxAge time.Duration) ([]TimedOutRelayed, error) {
	if maxAge <= 0 {
		return nil, nil
	}
	cutoff := now.Add(-maxAge).UTC().Format(time.RFC3339)
	rows, err := q.db.Query(
		`SELECT id, url, COALESCE(profile,''), queued_at
		 FROM queue WHERE status='relayed' AND queued_at < ?`,
		cutoff,
	)
	if err != nil {
		return nil, fmt.Errorf("select stuck relayed: %w", err)
	}
	var stuck []TimedOutRelayed
	for rows.Next() {
		var t TimedOutRelayed
		if err := rows.Scan(&t.ID, &t.URL, &t.Profile, &t.QueuedAt); err != nil {
			rows.Close()
			return nil, fmt.Errorf("select stuck scan: %w", err)
		}
		if parsed, perr := time.Parse(time.RFC3339, t.QueuedAt); perr == nil {
			t.AgeSecs = int64(now.Sub(parsed).Seconds())
		}
		stuck = append(stuck, t)
	}
	rows.Close()
	return stuck, rows.Err()
}

// MarkRelayedTimedOut marks each id as failed with error_reason="scoring_timeout".
// Called only for rows that the rescue path could not recover. EPIC-055 M1.
func (q *Queue) MarkRelayedTimedOut(ids []int64) error {
	for _, id := range ids {
		if err := q.MarkFailedWithReason(id, "scoring_timeout"); err != nil {
			return fmt.Errorf("mark timed out id=%d: %w", id, err)
		}
	}
	return nil
}

// IngestScoreIfRelayed conditionally promotes a queue row from relayed → scored.
// The WHERE id=? AND status='relayed' predicate is the race guard: if a
// concurrent writer (real-time callback or another watchdog sweep) already
// scored the row, rowsAffected==0 and this call is a no-op.
//
// Returns (true, nil) when the row was rescued; (false, nil) when the row was
// already scored or not found (lost the race — safe to ignore).
// EPIC-055 M1.
func (q *Queue) IngestScoreIfRelayed(id int64, score int, tags, verdict, slug string) (bool, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := q.db.Exec(
		`UPDATE queue SET status='scored', score=?, tags=?, verdict=?, slug=?, scored_at=?
		 WHERE id=? AND status='relayed'`,
		score, tags, verdict, slug, now, id,
	)
	if err != nil {
		return false, fmt.Errorf("ingest score if relayed id=%d: %w", id, err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// SweepRelayedTimeouts finds `relayed` rows whose queued_at is older than
// maxAge relative to `now`, marks each one failed with error_reason
// "scoring_timeout", and returns the swept rows (for event emission by the
// caller). The WHERE status='relayed' filter guarantees idempotency — a row
// that was already marked failed on a previous tick is never re-processed.
//
// Thin wrapper around SelectStuckRelayed + MarkRelayedTimedOut, preserved for
// backward compatibility with existing callers and tests. New code should use
// the split helpers when interposing rescue logic.
//
// EPIC-054 M3.
func (q *Queue) SweepRelayedTimeouts(now time.Time, maxAge time.Duration) ([]TimedOutRelayed, error) {
	stuck, err := q.SelectStuckRelayed(now, maxAge)
	if err != nil {
		return nil, err
	}
	if len(stuck) == 0 {
		return nil, nil
	}
	ids := make([]int64, len(stuck))
	for i, t := range stuck {
		ids[i] = t.ID
	}
	if err := q.MarkRelayedTimedOut(ids); err != nil {
		return stuck, err
	}
	return stuck, nil
}

// UpdateScore sets the score, tags, verdict, and slug on a queue item, promoting to 'scored' status.
func (q *Queue) UpdateScore(id int64, score int, tags, verdict, slug string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := q.db.Exec(
		"UPDATE queue SET status='scored', score=?, tags=?, verdict=?, slug=?, scored_at=? WHERE id=?",
		score, tags, verdict, slug, now, id,
	)
	return err
}

// Archive promotes an item to 'archived' status.
func (q *Queue) Archive(id int64) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := q.db.Exec(
		"UPDATE queue SET status='archived', archived_at=? WHERE id=?",
		now, id,
	)
	return err
}

// validOutcomes enumerates accepted outcome values for POST /queue/{id}/outcome.
var validOutcomes = map[string]bool{"acted": true, "ignored": true, "deferred": true}

// validFeedbacks enumerates accepted feedback values for POST /queue/{id}/feedback.
var validFeedbacks = map[string]bool{"accurate": true, "too_high": true, "too_low": true}

// UpdateOutcome sets the outcome and outcome_at on a queue item.
func (q *Queue) UpdateOutcome(id int64, outcome string) error {
	if !validOutcomes[outcome] {
		return fmt.Errorf("invalid outcome %q: must be acted, ignored, or deferred", outcome)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := q.db.Exec("UPDATE queue SET outcome=?, outcome_at=? WHERE id=?", outcome, now, id)
	return err
}

// UpdateFeedback sets the feedback and feedback_at on a queue item.
func (q *Queue) UpdateFeedback(id int64, feedback string) error {
	if !validFeedbacks[feedback] {
		return fmt.Errorf("invalid feedback %q: must be accurate, too_high, or too_low", feedback)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := q.db.Exec("UPDATE queue SET feedback=?, feedback_at=? WHERE id=?", feedback, now, id)
	return err
}

// ProfileStat holds aggregate scoring/feedback stats for a single profile.
type ProfileStat struct {
	Profile       string `json:"profile"`
	Count         int    `json:"count"`
	AvgScore      float64 `json:"avg_score"`
	AccurateCount int    `json:"accurate_count"`
	TooHighCount  int    `json:"too_high_count"`
	TooLowCount   int    `json:"too_low_count"`
	FeedbackCount int    `json:"feedback_count"`
}

// ProfileStats returns aggregate scoring and feedback stats, optionally filtered by profile.
func (q *Queue) ProfileStats(profile string) ([]ProfileStat, error) {
	sqlStr := `SELECT profile, COUNT(*), COALESCE(AVG(score),0),
		SUM(CASE WHEN feedback='accurate' THEN 1 ELSE 0 END),
		SUM(CASE WHEN feedback='too_high' THEN 1 ELSE 0 END),
		SUM(CASE WHEN feedback='too_low' THEN 1 ELSE 0 END),
		SUM(CASE WHEN feedback!='' AND feedback IS NOT NULL THEN 1 ELSE 0 END)
		FROM queue WHERE status IN ('scored','archived')`
	args := []any{}
	if profile != "" {
		sqlStr += " AND profile=?"
		args = append(args, profile)
	}
	sqlStr += " GROUP BY profile"
	rows, err := q.db.Query(sqlStr, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var stats []ProfileStat
	for rows.Next() {
		var s ProfileStat
		if err := rows.Scan(&s.Profile, &s.Count, &s.AvgScore, &s.AccurateCount, &s.TooHighCount, &s.TooLowCount, &s.FeedbackCount); err != nil {
			return nil, err
		}
		stats = append(stats, s)
	}
	return stats, rows.Err()
}

// ListArchived returns archived items, optionally filtered by profile.
// Back-compat wrapper; new callers should prefer ListArchivedCursor.
func (q *Queue) ListArchived(profile string, limit int) ([]QueueItem, error) {
	return q.ListArchivedCursor(profile, "archived", math.MaxInt64, limit)
}

// ListArchivedCursor paginates by id (monotonic, stable) with status + profile
// filters. status="" defaults to "archived"; status="all" disables the filter.
// beforeID is exclusive; use math.MaxInt64 for the first page.
func (q *Queue) ListArchivedCursor(profile, status string, beforeID int64, limit int) ([]QueueItem, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if status == "" {
		status = "archived"
	}
	if beforeID <= 0 {
		beforeID = math.MaxInt64
	}
	sqlStr := "SELECT " + queueCols + " FROM queue WHERE id<?"
	args := []any{beforeID}
	if status != "all" {
		sqlStr += " AND status=?"
		args = append(args, status)
	}
	if profile != "" {
		sqlStr += " AND profile=?"
		args = append(args, profile)
	}
	sqlStr += " ORDER BY id DESC LIMIT ?"
	args = append(args, limit)
	return q.query(sqlStr, args...)
}

// ArchiveFilter holds optional score/date range filters for archive queries (EPIC-070 M4).
type ArchiveFilter struct {
	ScoreMin *int
	ScoreMax *int
	Since    string // RFC3339
	Until    string // RFC3339
}

// ListArchivedCursorTyped extends ListArchivedCursor with type and score/date filters.
// itemType "jira" matches ginit_* actions; "url" matches non-ginit actions;
// empty string disables the filter. No schema migration required — type is
// synthesized from the action column prefix at query time (EPIC-057).
// filter may be nil to skip score/date filtering (EPIC-070 M4).
func (q *Queue) ListArchivedCursorTyped(profile, status, itemType string, beforeID int64, limit int, filter *ArchiveFilter) ([]QueueItem, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	if status == "" {
		status = "archived"
	}
	if beforeID <= 0 {
		beforeID = math.MaxInt64
	}
	sqlStr := "SELECT " + queueCols + " FROM queue WHERE id<?"
	args := []any{beforeID}
	if status != "all" {
		sqlStr += " AND status=?"
		args = append(args, status)
	}
	if profile != "" {
		sqlStr += " AND profile=?"
		args = append(args, profile)
	}
	switch itemType {
	case "jira":
		sqlStr += " AND action LIKE 'ginit_%'"
	case "url":
		sqlStr += " AND action NOT LIKE 'ginit_%'"
	}
	if filter != nil {
		if filter.ScoreMin != nil {
			sqlStr += " AND score >= ?"
			args = append(args, *filter.ScoreMin)
		}
		if filter.ScoreMax != nil {
			sqlStr += " AND score <= ?"
			args = append(args, *filter.ScoreMax)
		}
		if filter.Since != "" {
			sqlStr += " AND scored_at >= ?"
			args = append(args, filter.Since)
		}
		if filter.Until != "" {
			sqlStr += " AND scored_at <= ?"
			args = append(args, filter.Until)
		}
	}
	sqlStr += " ORDER BY id DESC LIMIT ?"
	args = append(args, limit)
	return q.query(sqlStr, args...)
}

// RecentScored returns items scored since the given time, ranked by score descending.
func (q *Queue) RecentScored(since time.Time, limit int) ([]QueueItem, error) {
	if limit <= 0 {
		limit = 20
	}
	return q.query("SELECT "+queueCols+" FROM queue WHERE status IN ('scored','archived') AND scored_at >= ? ORDER BY score DESC LIMIT ?", since.UTC().Format(time.RFC3339), limit)
}

// GetByID returns a single queue item by ID.
func (q *Queue) GetByID(id int64) (*QueueItem, error) {
	items, err := q.query("SELECT "+queueCols+" FROM queue WHERE id=?", id)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, fmt.Errorf("queue item %d not found", id)
	}
	return &items[0], nil
}

// Prune deletes the oldest rows when the queue exceeds maxQueueSize.
func (q *Queue) Prune() error {
	_, err := q.db.Exec(
		"DELETE FROM queue WHERE id IN (SELECT id FROM queue ORDER BY id ASC LIMIT MAX(0, (SELECT COUNT(*) FROM queue) - ?))",
		maxQueueSize,
	)
	return err
}

// Close closes the database connection.
func (q *Queue) Close() error {
	return q.db.Close()
}

// Snapshot writes a clean, defragmented copy of the database to destPath using
// VACUUM INTO. The destination is overwritten if it already exists. Intended
// for periodic point-in-time backups — if queue.db becomes corrupt, the last
// snapshot is the recovery baseline before attempting sqlite3 .recover.
func (q *Queue) Snapshot(destPath string) error {
	_, err := q.db.Exec("VACUUM INTO ?", destPath)
	if err != nil {
		return fmt.Errorf("snapshot to %s: %w", destPath, err)
	}
	return nil
}

// Ping checks that the database is reachable and structurally sound.
// Used by /healthz to surface mid-session DB failures before clients
// discover them as 500s on /archive or /digest.
func (q *Queue) Ping() error {
	var n int
	return q.db.QueryRow("SELECT COUNT(*) FROM queue LIMIT 1").Scan(&n)
}

// initFTS5 rebuilds the FTS5 index from the content table.
// This ensures existing rows (inserted before triggers existed) are indexed,
// and prevents "disk image is malformed" errors from stale index state.
func (q *Queue) initFTS5() error {
	// Rebuild completely re-reads all content from the queue table.
	_, err := q.db.Exec("INSERT INTO queue_fts(queue_fts) VALUES('rebuild')")
	return err
}

// ScoreByURL finds a relayed item by URL and updates it to scored, or inserts
// a new scored row if no relayed item exists. Returns the item and whether it
// was an insert (true) or update (false).
func (q *Queue) ScoreByURL(url string, score int, verdict, tags, profile, slug string) (*QueueItem, bool, error) {
	now := time.Now().UTC().Format(time.RFC3339)

	// Check for already-scored/archived item to prevent duplicates on re-run.
	existing, err := q.query(
		"SELECT "+queueCols+" FROM queue WHERE url=? AND status IN ('scored','archived') ORDER BY id DESC LIMIT 1",
		url,
	)
	if err != nil {
		return nil, false, err
	}
	if len(existing) > 0 {
		return &existing[0], false, nil
	}

	// Try to find an existing relayed item by URL.
	relayed, err := q.query(
		"SELECT "+queueCols+" FROM queue WHERE url=? AND status='relayed' ORDER BY id DESC LIMIT 1",
		url,
	)
	if err != nil {
		return nil, false, err
	}

	if len(relayed) > 0 {
		id := relayed[0].ID
		_, err = q.db.Exec(
			"UPDATE queue SET status='scored', score=?, tags=?, verdict=?, slug=?, scored_at=? WHERE id=?",
			score, tags, verdict, slug, now, id,
		)
		if err != nil {
			return nil, false, fmt.Errorf("ScoreByURL update: %w", err)
		}
		item, err := q.GetByID(id)
		return item, false, err
	}

	// INSERT path — CLI-originated score with no prior relayed row.
	res, err := q.db.Exec(
		`INSERT INTO queue (url, text, type, action, profile, status, queued_at, scored_at, score, tags, verdict, slug)
		 VALUES (?, '', 'url', '', ?, 'scored', ?, ?, ?, ?, ?, ?)`,
		url, profile, now, now, score, tags, verdict, slug,
	)
	if err != nil {
		return nil, false, fmt.Errorf("ScoreByURL insert: %w", err)
	}
	id, _ := res.LastInsertId()
	item, err := q.GetByID(id)
	return item, true, err
}

// SearchFTS5 runs a full-text search against the queue_fts index.
func (q *Queue) SearchFTS5(query string, profile string, limit int) ([]QueueItem, error) {
	if limit <= 0 {
		limit = 10
	}
	if profile != "" {
		return q.query(
			"SELECT "+queueCols+" FROM queue WHERE id IN (SELECT rowid FROM queue_fts WHERE queue_fts MATCH ? ORDER BY rank LIMIT ?) AND profile=?",
			query, limit, profile,
		)
	}
	return q.query(
		"SELECT "+queueCols+" FROM queue WHERE id IN (SELECT rowid FROM queue_fts WHERE queue_fts MATCH ? ORDER BY rank LIMIT ?)",
		query, limit,
	)
}

// --- EPIC-045: push_outbox + devices ---

// PushItem is a row in the push_outbox table.
type PushItem struct {
	ID          int64
	Score       int
	Slug        string
	Verdict     string
	URL         string
	Kind        string
	Profile     string // EPIC-061: auto-classified profile for FCM payload
	Status      string
	Attempts    int
	NextAttempt int64
	CreatedAt   int64
	UpdatedAt   int64
	LastError   string
	GapSummary  string // EPIC-058 M7
	ContentType string // EPIC-071 M3: "voice_note" for audio shares
}

// EnqueuePush inserts a pending row into push_outbox and returns its id.
// Profile defaults to "" for legacy callers. Prefer EnqueuePushWithProfile
// or EnqueueDigestIfDue for new code.
func (q *Queue) EnqueuePush(kind string, score int, slug, verdict, url string) (int64, error) {
	return q.EnqueuePushWithProfile(kind, "", score, slug, verdict, url, "")
}

// EnqueuePushWithProfile is the profile-aware primitive used by
// EnqueueDigestIfDue. Direct callers are discouraged outside the unified
// helper — EPIC-051 M3 will consolidate call sites behind EnqueueDigestIfDue.
func (q *Queue) EnqueuePushWithProfile(kind, profile string, score int, slug, verdict, url, gapSummary string) (int64, error) {
	now := time.Now().Unix()
	res, err := q.db.Exec(
		`INSERT INTO push_outbox (score, slug, verdict, url, kind, profile, gap_summary, status, attempts, next_attempt, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, 'pending', 0, ?, ?, ?)`,
		score, slug, verdict, url, kind, profile, gapSummary, now, now, now,
	)
	if err != nil {
		return 0, fmt.Errorf("enqueue push: %w", err)
	}
	id, _ := res.LastInsertId()
	return id, nil
}

// EnqueueDigestResult holds the outcome of EnqueueDigestIfDue. A successful
// enqueue populates ID; a suppressed call leaves ID zero and records Reason.
type EnqueueDigestResult struct {
	Enqueued              bool
	Reason                string // "enqueued", "throttled", "below_min_score"
	ID                    int64  // row id when enqueued; 0 otherwise
	SecondsUntilAllowed   int64  // populated on throttled
	ThrottleRemainingMs   int64  // populated on enqueued (throttle window length ms)
}

// EnqueueDigestIfDue is the single sanctioned entry point for writing a
// digest row to push_outbox. It applies the NotifyMinScore floor, consults
// the per-profile throttle from the live PushConfig, and atomically inserts
// a new row iff the window has elapsed. Safe for concurrent use across
// multiple processes — the throttle check + insert happen inside a single
// SQLite IMMEDIATE transaction so two racing linkari processes can't both
// write a digest row inside the same window.
//
// EPIC-051 M2. See M1 decision in the epic Notes for the NotifyMinScore
// rationale (Position B — honor as a uniform floor).
func (q *Queue) EnqueueDigestIfDue(ctx context.Context, profile string, score int, slug, verdict, url string, gapSummary ...string) (EnqueueDigestResult, error) {
	cfg := q.PushConfig()

	// Uniform min-score floor (M1 decision).
	if cfg.NotifyMinScore > 0 && score < cfg.NotifyMinScore {
		return EnqueueDigestResult{Reason: "below_min_score"}, nil
	}

	throttle := cfg.ThrottleFor(profile)
	now := time.Now().Unix()
	cutoff := now - int64(throttle.Seconds())

	tx, err := q.db.BeginTx(ctx, nil)
	if err != nil {
		return EnqueueDigestResult{}, fmt.Errorf("begin digest tx: %w", err)
	}
	defer tx.Rollback()

	var last sql.NullInt64
	if err := tx.QueryRowContext(ctx,
		`SELECT MAX(created_at) FROM push_outbox WHERE kind='digest' AND profile=?`,
		profile,
	).Scan(&last); err != nil {
		return EnqueueDigestResult{}, fmt.Errorf("query last digest: %w", err)
	}

	if last.Valid && last.Int64 > cutoff {
		// Throttled: allowed time is lastPush + throttle.
		secondsUntil := (last.Int64 + int64(throttle.Seconds())) - now
		if secondsUntil < 0 {
			secondsUntil = 0
		}
		if err := tx.Commit(); err != nil {
			return EnqueueDigestResult{}, fmt.Errorf("commit throttled tx: %w", err)
		}
		return EnqueueDigestResult{
			Reason:              "throttled",
			SecondsUntilAllowed: secondsUntil,
		}, nil
	}

	gs := ""
	if len(gapSummary) > 0 {
		gs = gapSummary[0]
	}
	ct := ""
	if len(gapSummary) > 1 {
		ct = gapSummary[1] // EPIC-071 M3: optional content_type (e.g. "voice_note")
	}
	res, err := tx.ExecContext(ctx,
		`INSERT INTO push_outbox (score, slug, verdict, url, kind, profile, gap_summary, content_type, status, attempts, next_attempt, created_at, updated_at)
		 VALUES (?, ?, ?, ?, 'digest', ?, ?, ?, 'pending', 0, ?, ?, ?)`,
		score, slug, verdict, url, profile, gs, ct, now, now, now,
	)
	if err != nil {
		return EnqueueDigestResult{}, fmt.Errorf("insert digest: %w", err)
	}
	id, _ := res.LastInsertId()
	if err := tx.Commit(); err != nil {
		return EnqueueDigestResult{}, fmt.Errorf("commit digest tx: %w", err)
	}
	return EnqueueDigestResult{
		Enqueued:            true,
		Reason:              "enqueued",
		ID:                  id,
		ThrottleRemainingMs: throttle.Milliseconds(),
	}, nil
}

// LastDigestPushAt returns the unix timestamp of the most recent digest row
// in push_outbox, or 0 if none exist. Used by the CLI score path to throttle
// digest pushes without in-process state.
func (q *Queue) LastDigestPushAt() (int64, error) {
	var ts sql.NullInt64
	err := q.db.QueryRow(`SELECT MAX(created_at) FROM push_outbox WHERE kind='digest'`).Scan(&ts)
	if err != nil {
		return 0, err
	}
	if !ts.Valid {
		return 0, nil
	}
	return ts.Int64, nil
}

// PendingPushes returns up to limit pending rows whose next_attempt <= now.
func (q *Queue) PendingPushes(limit int) ([]PushItem, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := q.db.Query(
		`SELECT id, score, slug, verdict, url, kind, profile, status, attempts, next_attempt, created_at, updated_at, last_error, gap_summary, content_type
		 FROM push_outbox WHERE status='pending' AND next_attempt <= ? ORDER BY id ASC LIMIT ?`,
		time.Now().Unix(), limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []PushItem
	for rows.Next() {
		var p PushItem
		if err := rows.Scan(&p.ID, &p.Score, &p.Slug, &p.Verdict, &p.URL, &p.Kind, &p.Profile, &p.Status, &p.Attempts, &p.NextAttempt, &p.CreatedAt, &p.UpdatedAt, &p.LastError, &p.GapSummary, &p.ContentType); err != nil {
			return nil, err
		}
		items = append(items, p)
	}
	return items, rows.Err()
}

// MarkPushSent marks a push row as sent.
func (q *Queue) MarkPushSent(id int64) error {
	now := time.Now().Unix()
	_, err := q.db.Exec(
		`UPDATE push_outbox SET status='sent', updated_at=?, last_error='' WHERE id=?`,
		now, id,
	)
	return err
}

// BumpPushAttempt increments attempts and schedules next_attempt after backoff seconds.
func (q *Queue) BumpPushAttempt(id int64, backoffSeconds int64, lastErr string) error {
	now := time.Now().Unix()
	_, err := q.db.Exec(
		`UPDATE push_outbox SET attempts = attempts + 1, next_attempt = ?, updated_at = ?, last_error = ? WHERE id = ?`,
		now+backoffSeconds, now, lastErr, id,
	)
	return err
}

// ParkPush bumps next_attempt without incrementing attempts (missing-token park).
func (q *Queue) ParkPush(id int64, backoffSeconds int64) error {
	now := time.Now().Unix()
	_, err := q.db.Exec(
		`UPDATE push_outbox SET next_attempt = ?, updated_at = ?, last_error = 'parked: no fcm token' WHERE id = ?`,
		now+backoffSeconds, now, id,
	)
	return err
}

// MarkPushDead marks a push row as dead-lettered.
func (q *Queue) MarkPushDead(id int64, reason string) error {
	now := time.Now().Unix()
	_, err := q.db.Exec(
		`UPDATE push_outbox SET status='dead', updated_at=?, last_error=? WHERE id=?`,
		now, reason, id,
	)
	return err
}

// PrunePushes deletes sent rows older than 7 days.
func (q *Queue) PrunePushes() error {
	cutoff := time.Now().Add(-7 * 24 * time.Hour).Unix()
	_, err := q.db.Exec(
		`DELETE FROM push_outbox WHERE status='sent' AND updated_at < ?`,
		cutoff,
	)
	return err
}

// UpsertDevice sets the single registered FCM token, replacing any prior row.
func (q *Queue) UpsertDevice(token string) error {
	now := time.Now().Unix()
	tx, err := q.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM devices`); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO devices(token, updated_at) VALUES(?, ?)`, token, now); err != nil {
		return err
	}
	return tx.Commit()
}

// GetDeviceToken returns the registered FCM token, or "" if none.
func (q *Queue) GetDeviceToken() (string, error) {
	row := q.db.QueryRow(`SELECT token FROM devices LIMIT 1`)
	var token string
	if err := row.Scan(&token); err != nil {
		if err == sql.ErrNoRows {
			return "", nil
		}
		return "", err
	}
	return token, nil
}

// AuthUser represents a row in the users table.
type AuthUser struct {
	ID        int64
	GoogleSub string
	Email     string
	Name      string
	Active    bool
}

// LookupUserBySub finds a user by their Google sub claim. Returns nil if not found.
func (q *Queue) LookupUserBySub(googleSub string) (*AuthUser, error) {
	row := q.db.QueryRow(
		`SELECT id, google_sub, email, name, active FROM users WHERE google_sub = ?`,
		googleSub,
	)
	var u AuthUser
	var active int
	err := row.Scan(&u.ID, &u.GoogleSub, &u.Email, &u.Name, &active)
	if err != nil {
		if err.Error() == "sql: no rows in result set" {
			return nil, nil
		}
		return nil, fmt.Errorf("lookup user: %w", err)
	}
	u.Active = active != 0
	return &u, nil
}

// LookupSession validates a session token and returns the user ID.
// Returns an error if the token is not found or expired.
func (q *Queue) LookupSession(token string) (int64, error) {
	now := time.Now().Unix()
	var userID int64
	err := q.db.QueryRow(
		`SELECT user_id FROM sessions WHERE token = ? AND expires_at > ?`,
		token, now,
	).Scan(&userID)
	if err != nil {
		return 0, fmt.Errorf("lookup session: %w", err)
	}
	return userID, nil
}

// InsertSession stores a new session token.
func (q *Queue) InsertSession(token string, userID int64, googleSub string, expiresAt time.Time) error {
	now := time.Now().Unix()
	_, err := q.db.Exec(
		`INSERT INTO sessions (token, user_id, google_sub, created_at, expires_at) VALUES (?, ?, ?, ?, ?)`,
		token, userID, googleSub, now, expiresAt.Unix(),
	)
	if err != nil {
		return fmt.Errorf("insert session: %w", err)
	}
	return nil
}

// RedeemInvite atomically validates an invite code, creates a user, and marks
// the code as used. Returns the new user's ID.
func (q *Queue) RedeemInvite(code, googleSub, email, name string) (int64, error) {
	tx, err := q.db.Begin()
	if err != nil {
		return 0, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	// Check invite code exists and is unused.
	var codeID int64
	var used int
	err = tx.QueryRow(
		`SELECT id, used FROM invite_codes WHERE code = ?`, code,
	).Scan(&codeID, &used)
	if err != nil {
		return 0, fmt.Errorf("invalid invite code")
	}
	if used != 0 {
		return 0, fmt.Errorf("invite code already used")
	}

	// Create the user.
	now := time.Now().Unix()
	res, err := tx.Exec(
		`INSERT INTO users (google_sub, email, name, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
		googleSub, email, name, now, now,
	)
	if err != nil {
		return 0, fmt.Errorf("create user: %w", err)
	}
	userID, _ := res.LastInsertId()

	// Mark code as used.
	_, err = tx.Exec(
		`UPDATE invite_codes SET used = 1, used_by = ?, used_at = ? WHERE id = ?`,
		googleSub, now, codeID,
	)
	if err != nil {
		return 0, fmt.Errorf("mark invite used: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit: %w", err)
	}
	return userID, nil
}

// CreateInviteCode generates and stores a new 8-character alphanumeric invite code.
func (q *Queue) CreateInviteCode() (string, error) {
	code := generateInviteCode()
	now := time.Now().Unix()
	_, err := q.db.Exec(
		`INSERT INTO invite_codes (code, created_at) VALUES (?, ?)`,
		code, now,
	)
	if err != nil {
		return "", fmt.Errorf("create invite code: %w", err)
	}
	return code, nil
}

// SeedInviteCodes inserts static invite codes (from server.yaml) into the DB,
// skipping any that already exist. Returns the count of newly inserted codes.
func (q *Queue) SeedInviteCodes(codes []string) (int, error) {
	var seeded int
	now := time.Now().Unix()
	for _, code := range codes {
		if code == "" {
			continue
		}
		res, err := q.db.Exec(
			`INSERT OR IGNORE INTO invite_codes (code, created_at) VALUES (?, ?)`,
			code, now,
		)
		if err != nil {
			return seeded, fmt.Errorf("seed invite code %q: %w", code, err)
		}
		n, _ := res.RowsAffected()
		seeded += int(n)
	}
	return seeded, nil
}

// generateInviteCode returns a cryptographically random 8-char alphanumeric string.
func generateInviteCode() string {
	const charset = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, 8)
	// Use crypto/rand for unguessable codes.
	randBytes := make([]byte, 8)
	crand.Read(randBytes)
	for i := range b {
		b[i] = charset[int(randBytes[i])%len(charset)]
	}
	return string(b)
}

// classifySkipReason derives a machine-readable skip tag from score + verdict.
// Returns "" for scored items (score > 0) or items with no verdict.
func classifySkipReason(score int, verdict string) string {
	if score > 0 || verdict == "" {
		return ""
	}
	v := strings.ToLower(verdict)
	switch {
	case strings.Contains(v, "paywall"):
		return "paywalled"
	case strings.Contains(v, "no content") || strings.Contains(v, "empty") || strings.Contains(v, "no meaningful content"):
		return "no_content"
	case strings.Contains(v, "not technical") || strings.Contains(v, "non-technical"):
		return "not_technical"
	case strings.Contains(v, "song") || strings.Contains(v, "lyrics"):
		return "song_lyrics"
	case strings.Contains(v, "duplicate"):
		return "duplicate"
	case strings.Contains(v, "login") || strings.Contains(v, "sign in") || strings.Contains(v, "authentication required"):
		return "login_required"
	case strings.Contains(v, "404") || strings.Contains(v, "not found"):
		return "not_found"
	default:
		return "skipped"
	}
}

func (q *Queue) query(sqlStr string, args ...any) ([]QueueItem, error) {
	rows, err := q.db.Query(sqlStr, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := []QueueItem{}
	for rows.Next() {
		var it QueueItem
		var score int
		if err := rows.Scan(&it.ID, &it.URL, &it.Text, &it.Type, &it.Action, &it.Profile, &it.Status, &score, &it.Tags, &it.QueuedAt, &it.RelayedAt, &it.ScoredAt, &it.ArchivedAt, &it.Verdict, &it.Slug, &it.Progress, &it.Outcome, &it.OutcomeAt, &it.Feedback, &it.FeedbackAt); err != nil {
			return nil, err
		}
		if score != 0 {
			it.Score = &score
		}
		it.SkipReason = classifySkipReason(score, it.Verdict)
		items = append(items, it)
	}
	return items, rows.Err()
}
