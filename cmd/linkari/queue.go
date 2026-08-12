package main

import (
	"context"
	crand "crypto/rand"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"os"
	"strings"
	"sync/atomic"
	"time"

	_ "modernc.org/sqlite"
)

const maxQueueSize = 200

// validStatuses enumerates every legal status value a client may filter on.
// Used by /queue and /archive query-param validation.
var validStatuses = map[string]bool{
	"pending":     true,
	"relayed":     true,
	"scored":      true,
	"archived":    true,
	"failed":      true,
	"eval_failed": true, // EPIC-001 M2: evaluator double-failure terminal status
	"prefiltered": true, // EPIC-001 M4: pre-filter transparency queue rows
	"captured":    true, // F4: structured-content capture terminal status
	"all":         true,
}

// QueueItem represents a persisted share request.
type QueueItem struct {
	ID                      int64    `json:"id"`
	URL                     string   `json:"url"`
	Text                    string   `json:"text"`
	Type                    string   `json:"type"`
	Action                  string   `json:"action"`
	Profile                 string   `json:"profile"`
	Status                  string   `json:"status"`
	Score                   *int     `json:"score,omitempty"`
	Tags                    string   `json:"tags,omitempty"`
	QueuedAt                string   `json:"queued_at"`
	RelayedAt               string   `json:"relayed_at,omitempty"`
	ScoredAt                string   `json:"scored_at,omitempty"`
	ArchivedAt              string   `json:"archived_at,omitempty"`
	Verdict                 string   `json:"verdict,omitempty"`
	Slug                    string   `json:"slug,omitempty"`
	Progress                string   `json:"progress,omitempty"`
	SkipReason              string   `json:"skip_reason,omitempty"`
	Outcome                 string   `json:"outcome,omitempty"`
	OutcomeAt               string   `json:"outcome_at,omitempty"`
	Feedback                string   `json:"feedback,omitempty"`
	FeedbackAt              string   `json:"feedback_at,omitempty"`
	Title                   string   `json:"title,omitempty"`
	RubricScores            string   `json:"rubric_scores,omitempty"`
	TopicTags               string   `json:"topic_tags,omitempty"`
	ClusterID               *int64   `json:"cluster_id,omitempty"`
	ActionRoute             string   `json:"action_route,omitempty"`
	ClassifySource          string   `json:"classify_source,omitempty"`                  // EPIC-077 M1
	IsScreenshot            bool     `json:"is_screenshot,omitempty"`                    // EPIC-078 M4
	FileSize                int64    `json:"file_size,omitempty"`                        // EPIC-078 M5
	IsShorts                bool     `json:"is_shorts,omitempty"`                        // EPIC-012 M3
	Source                  string   `json:"source,omitempty"`                           // EPIC-016 M2: firehose source tracking
	ArtifactPath            string   `json:"artifact_path,omitempty" db:"artifact_path"` // F2: capture artifact file path
	ContentWarning          string   `json:"content_warning,omitempty"`                  // EPIC-102: "lit_parse_failed" when extraction failed
	ExtractionConfidence    *float64 `json:"extraction_confidence,omitempty"`            // EPIC-104: mean per-page confidence; -1.0 = JSON parse fallback; nil = non-PDF or pre-feature
	RetryCount              int      `json:"retry_count,omitempty"`                      // EPIC-108 M3: audio fallback retry attempts completed
	RetryAfter              int64    `json:"retry_after,omitempty"`                      // EPIC-108 M3: Unix timestamp; 0 = process immediately
	ErrorReason             string   `json:"error_reason,omitempty"`                     // EPIC-111 F2: terminal failure reason; populated for status=failed
	ContentHash             string   `json:"content_hash,omitempty"`                     // EPIC-111 F3: SHA-256 hex of raw fetched bytes (set at intake)
	TraceID                 string   `json:"trace_id,omitempty"`                         // EPIC-111 F3: UUID v4 persisted at intake; immutable across retries
	UserTags                string   `json:"user_tags,omitempty"`                        // EPIC-149 F2: user-supplied tags (JSON array)
	Intent                  string   `json:"intent,omitempty"`                           // EPIC-154 F1: score|capture|transcribe
	InferredTags            string   `json:"inferred_tags,omitempty"`                    // EPIC-154 F1: system-inferred tags (JSON array); never merged with UserTags
	UserRationaleText       string   `json:"user_rationale_text,omitempty"`              // Share-time voice/typed rationale text
	UserRationaleSource     string   `json:"user_rationale_source,omitempty"`            // typed|voice_transcript
	UserRationaleDurationMS int64    `json:"user_rationale_duration_ms,omitempty"`       // voice capture duration when known
	CaptureMode             string   `json:"capture_mode,omitempty"`                     // Android capture mode hint
	SourceApp               string   `json:"source_app,omitempty"`                       // originating Android package when known
	SubmittedByDeviceID     string   `json:"submitted_by_device_id,omitempty"`           // EPIC-167 F3
	SubmittedByUserID       int64    `json:"-"`                                          // EPIC-167 F4 internal token lookup owner
	WikiContextUsed         bool     `json:"wiki_context_used,omitempty"`                // EPIC-180 M4
	WikiTopic               string   `json:"wiki_topic,omitempty"`                       // EPIC-180 M4
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

	traceIDFn func() string
}

// boolToInt converts a bool to 0/1 for SQLite INTEGER columns.
func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
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
	// _busy_timeout=5000: wait up to 5s for a write lock instead of returning
	// SQLITE_BUSY immediately. Defense-in-depth alongside SetMaxOpenConns(1).
	db, err := sql.Open("sqlite", dbPath+"?_busy_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("open queue db: %w", err)
	}
	// Serialize all SQLite access through a single connection. SQLite allows
	// only one writer at a time; multiple connections under concurrent load
	// produce SQLITE_BUSY (observed: 75% push loss rate during 8-job whisper
	// burst  -  release checklist §6, 2026-05-09).
	db.SetMaxOpenConns(1)

	// Integrity check before any schema work  -  catches SQLITE_CORRUPT (11) that
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
		return nil, fmt.Errorf("queue.db is corrupt (%s)  -  recover with: sqlite3 ~/.config/linkari/queue.db \".recover\" | sqlite3 ~/.config/linkari/queue_recovered.db && mv queue.db queue.db.bak && mv queue_recovered.db queue.db", integrityResult)
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
	// recover  -  the 2026-04-13 SQLITE_CORRUPT (11) incident occurred with WAL
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

	// Idempotent schema migration  -  add scoring/archive columns.
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
		// EPIC-072 M2: rubric_scores + title columns.
		"ALTER TABLE queue ADD COLUMN title TEXT DEFAULT ''",
		"ALTER TABLE queue ADD COLUMN rubric_scores TEXT DEFAULT ''",
		// EPIC-072 M5: topic tags for clustering.
		"ALTER TABLE queue ADD COLUMN topic_tags TEXT DEFAULT ''",
		// EPIC-072 M6: cluster_id FK on queue.
		"ALTER TABLE queue ADD COLUMN cluster_id INTEGER DEFAULT NULL",
		// EPIC-072 M9: action_route column.
		"ALTER TABLE queue ADD COLUMN action_route TEXT DEFAULT ''",
		// EPIC-038 M3: intent metadata columns for audit/replay.
		"ALTER TABLE queue ADD COLUMN mime_type TEXT DEFAULT ''",
		"ALTER TABLE queue ADD COLUMN calling_package TEXT DEFAULT ''",
		"ALTER TABLE queue ADD COLUMN relative_path TEXT DEFAULT ''",
		"ALTER TABLE queue ADD COLUMN file_name TEXT DEFAULT ''",
		// EPIC-077 M1: classification source  -  which cascade stage won pre-enqueue.
		"ALTER TABLE queue ADD COLUMN classify_source TEXT NOT NULL DEFAULT ''",
		// EPIC-078 M4: screenshot flag for accuracy audits.
		"ALTER TABLE queue ADD COLUMN is_screenshot INTEGER NOT NULL DEFAULT 0",
		// EPIC-078 M5: file size for pre-enqueue dedup of repeated file shares.
		"ALTER TABLE queue ADD COLUMN file_size INTEGER DEFAULT 0",
		// EPIC-082 M1: prompt traceability columns.
		"ALTER TABLE queue ADD COLUMN prompt_hash TEXT DEFAULT ''",
		"ALTER TABLE queue ADD COLUMN prompt_version TEXT DEFAULT ''",
		// EPIC-088 M3: per-call scoring cost and vision token persistence.
		"ALTER TABLE queue ADD COLUMN scoring_cost_usd REAL DEFAULT NULL",
		"ALTER TABLE queue ADD COLUMN image_tokens_estimated INTEGER DEFAULT NULL",
		// EPIC-012 M2: YouTube Shorts detection flag.
		"ALTER TABLE queue ADD COLUMN is_shorts INTEGER NOT NULL DEFAULT 0",
		// EPIC-016 M2: Firehose source tracking.
		"ALTER TABLE queue ADD COLUMN source TEXT NOT NULL DEFAULT ''",
		// F2: structured-content capture artifact path.
		"ALTER TABLE queue ADD COLUMN artifact_path TEXT DEFAULT NULL",
		// EPIC-102: content_warning for lit parse failures.
		"ALTER TABLE queue ADD COLUMN content_warning TEXT DEFAULT NULL",
		// EPIC-104: mean per-page confidence from --format json; -1.0 = JSON parse fallback; NULL = non-PDF or pre-feature.
		"ALTER TABLE queue ADD COLUMN extraction_confidence REAL",
		// EPIC-108 M3: dead-letter retry counters for audio fallback jobs.
		"ALTER TABLE queue ADD COLUMN retry_count INTEGER DEFAULT 0",
		"ALTER TABLE queue ADD COLUMN retry_after INTEGER DEFAULT 0",
		// EPIC-111 F3 M8: replay-safe fields for content drift detection and event correlation.
		"ALTER TABLE queue ADD COLUMN content_hash TEXT DEFAULT ''",
		"ALTER TABLE queue ADD COLUMN trace_id TEXT DEFAULT ''",
		// EPIC-149: user-applied tags from share-time (JSON array).
		"ALTER TABLE queue ADD COLUMN user_tags TEXT DEFAULT ''",
		// EPIC-154 F1: intent (score|capture|transcribe) and system-inferred tags.
		"ALTER TABLE queue ADD COLUMN intent TEXT DEFAULT NULL",
		"ALTER TABLE queue ADD COLUMN inferred_tags TEXT DEFAULT NULL",
		// Share-time rationale (typed or voice transcript) context.
		"ALTER TABLE queue ADD COLUMN user_rationale_text TEXT DEFAULT ''",
		"ALTER TABLE queue ADD COLUMN user_rationale_source TEXT DEFAULT ''",
		"ALTER TABLE queue ADD COLUMN user_rationale_duration_ms INTEGER DEFAULT 0",
		"ALTER TABLE queue ADD COLUMN capture_mode TEXT DEFAULT ''",
		"ALTER TABLE queue ADD COLUMN source_app TEXT DEFAULT ''",
		// EPIC-167 F3: origin device attribution for targeted push routing.
		"ALTER TABLE queue ADD COLUMN submitted_by_device_id TEXT DEFAULT NULL",
		"ALTER TABLE queue ADD COLUMN submitted_by_user_id INTEGER DEFAULT NULL",
		"CREATE INDEX IF NOT EXISTS idx_queue_submitted_by_device ON queue(submitted_by_device_id)",
		// EPIC-159 F6: indexes for intent/tag stats queries.
		"CREATE INDEX IF NOT EXISTS idx_queue_intent_status ON queue(intent, status)",
		// EPIC-180 M4: wiki context gap-signal persistence.
		"ALTER TABLE queue ADD COLUMN wiki_context_used INTEGER NOT NULL DEFAULT 0",
		"ALTER TABLE queue ADD COLUMN wiki_topic TEXT NOT NULL DEFAULT ''",
	}
	for _, m := range migrations {
		db.Exec(m) // Ignore "duplicate column" errors.
	}

	// EPIC-171: append-only share/tag analytics lifecycle event log.
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS share_analytics_events (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		event_id TEXT NOT NULL UNIQUE,
		event_type TEXT NOT NULL,
		share_id INTEGER,
		created_at TEXT NOT NULL,
		profile TEXT,
		intent TEXT,
		content_type TEXT,
		share_surface TEXT,
		source_app TEXT,
		url_domain TEXT,
		user_tags_json TEXT,
		has_user_rationale INTEGER NOT NULL DEFAULT 0,
		rationale_word_count INTEGER NOT NULL DEFAULT 0,
		score REAL,
		verdict TEXT,
		outcome TEXT,
		feedback TEXT,
		details_json TEXT NOT NULL DEFAULT '{}'
	)`); err != nil {
		db.Close()
		return nil, fmt.Errorf("create share_analytics_events: %w", err)
	}
	for _, m := range []string{
		"CREATE INDEX IF NOT EXISTS idx_share_analytics_created_at ON share_analytics_events(created_at)",
		"CREATE INDEX IF NOT EXISTS idx_share_analytics_event_type ON share_analytics_events(event_type)",
		"CREATE INDEX IF NOT EXISTS idx_share_analytics_url_domain ON share_analytics_events(url_domain)",
		"CREATE INDEX IF NOT EXISTS idx_share_analytics_profile ON share_analytics_events(profile)",
		"CREATE INDEX IF NOT EXISTS idx_share_analytics_intent ON share_analytics_events(intent)",
	} {
		db.Exec(m)
	}

	// EPIC-149: tag inventory table for ranked suggestions.
	db.Exec(`CREATE TABLE IF NOT EXISTS tags (
		name         TEXT PRIMARY KEY,
		use_count    INTEGER NOT NULL DEFAULT 0,
		last_used_at TEXT NOT NULL DEFAULT '',
		created_at   TEXT NOT NULL DEFAULT ''
	)`)

	// EPIC-072 M6: clusters table.
	db.Exec(`CREATE TABLE IF NOT EXISTS clusters (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		profile    TEXT NOT NULL DEFAULT '',
		theme      TEXT NOT NULL DEFAULT '',
		synthesis  TEXT DEFAULT '',
		formed_at  TEXT NOT NULL,
		item_count INTEGER NOT NULL DEFAULT 0
	)`)

	// EPIC-016 M2: Firehose subscription tables.
	db.Exec(`CREATE TABLE IF NOT EXISTS firehose_subscriptions (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		profile TEXT NOT NULL,
		keyword TEXT NOT NULL,
		created_at INTEGER NOT NULL DEFAULT (strftime('%s','now')),
		UNIQUE(profile, keyword)
	)`)
	db.Exec(`CREATE TABLE IF NOT EXISTS firehose_events (
		seq INTEGER PRIMARY KEY,
		event_cbor BLOB
	)`)

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
		"ALTER TABLE push_outbox ADD COLUMN gap_summary TEXT NOT NULL DEFAULT ''",     // EPIC-058 M7
		"ALTER TABLE push_outbox ADD COLUMN content_type TEXT NOT NULL DEFAULT ''",    // EPIC-071 M3
		"ALTER TABLE push_outbox ADD COLUMN action_route TEXT NOT NULL DEFAULT ''",    // EPIC-072 M9
		"ALTER TABLE push_outbox ADD COLUMN classify_source TEXT NOT NULL DEFAULT ''", // EPIC-077 M6
		"ALTER TABLE push_outbox ADD COLUMN content_warning TEXT NOT NULL DEFAULT ''", // EPIC-102
		"ALTER TABLE push_outbox ADD COLUMN error_reason TEXT NOT NULL DEFAULT ''",    // EPIC-111 F2 M6: failure reason for status=failed pushes
		"ALTER TABLE push_outbox ADD COLUMN target_device_id TEXT DEFAULT NULL",       // EPIC-167 F4: device-targeted pushes
		"ALTER TABLE push_outbox ADD COLUMN target_user_id INTEGER DEFAULT NULL",      // EPIC-167 F4: target device owner
		"ALTER TABLE push_outbox ADD COLUMN push_kind TEXT NOT NULL DEFAULT ''",       // EPIC-167 F4: semantic push kind
		"ALTER TABLE devices ADD COLUMN device_id TEXT DEFAULT ''",                    // EPIC-167 F1: per-user device identity
		"ALTER TABLE devices ADD COLUMN device_name TEXT DEFAULT ''",
		"ALTER TABLE devices ADD COLUMN platform TEXT NOT NULL DEFAULT 'android'",
		"ALTER TABLE devices ADD COLUMN app_version TEXT DEFAULT ''",
		"ALTER TABLE devices ADD COLUMN enabled INTEGER NOT NULL DEFAULT 1",
		"ALTER TABLE devices ADD COLUMN created_at INTEGER NOT NULL DEFAULT 0",
		"ALTER TABLE devices ADD COLUMN token_updated_at INTEGER NOT NULL DEFAULT 0",
		"ALTER TABLE devices ADD COLUMN last_seen_at INTEGER NOT NULL DEFAULT 0",
		// EPIC-180 M4: wiki topic threading for FCM payload.
		"ALTER TABLE push_outbox ADD COLUMN wiki_topic TEXT NOT NULL DEFAULT ''",
	}
	for _, m := range pushMigrations {
		db.Exec(m) // ignore "duplicate column" errors
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_push_outbox_kind_profile_created
		ON push_outbox(kind, profile, created_at)`); err != nil {
		db.Close()
		return nil, fmt.Errorf("create push_outbox kind/profile index: %w", err)
	}

	// EPIC-001: auth tables  -  users, invite_codes, sessions.
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

	// EPIC-018 M2: Watch Later video dedup table.
	db.Exec(`CREATE TABLE IF NOT EXISTS youtube_watchlater_videos (
		id           INTEGER PRIMARY KEY AUTOINCREMENT,
		video_id     TEXT NOT NULL UNIQUE,
		discovered_at INTEGER NOT NULL,
		scored_at    INTEGER,
		queue_id     INTEGER
	)`)

	// Liked Videos dedup table  -  mirrors youtube_watchlater_videos for LL playlist.
	db.Exec(`CREATE TABLE IF NOT EXISTS youtube_liked_videos (
		id           INTEGER PRIMARY KEY AUTOINCREMENT,
		video_id     TEXT NOT NULL UNIQUE,
		discovered_at INTEGER NOT NULL,
		scored_at    INTEGER,
		queue_id     INTEGER
	)`)

	// EPIC-019 M3: monitored subscription videos dedup table.
	db.Exec(`CREATE TABLE IF NOT EXISTS youtube_monitored_videos (
		id           INTEGER PRIMARY KEY AUTOINCREMENT,
		channel_id   TEXT NOT NULL,
		video_id     TEXT NOT NULL UNIQUE,
		discovered_at INTEGER NOT NULL,
		scored_at    INTEGER,
		queue_id     INTEGER
	)`)

	// EPIC-091 M2: unified seen_content dedup table  -  replaces per-source tables.
	// source values are bound to ContentSource.Name() return values:
	//   "bsky_firehose"  → BlueskyFirehoseSource
	//   "yt_watch_later" → YouTubeWatchLaterSource
	//   "yt_liked"       → YouTubeLikedSource
	//   "yt_monitored"   → YouTubeSubsSource
	// These values are immutable  -  changing them discards all prior dedup history.
	db.Exec(`CREATE TABLE IF NOT EXISTS seen_content (
		source   TEXT    NOT NULL,
		item_id  TEXT    NOT NULL,
		seen_at  INTEGER NOT NULL DEFAULT (strftime('%s','now')),
		queue_id INTEGER,
		PRIMARY KEY (source, item_id)
	)`)

	// EPIC-091 M4: migrate per-source tables → seen_content in one transaction.
	// Idempotent: old tables are empty on second run; INSERT OR IGNORE is a no-op.
	{
		tx, txErr := db.Begin()
		if txErr == nil {
			var rowsCopied int64
			for _, migration := range []struct {
				source string
				table  string
			}{
				{"yt_watch_later", "youtube_watchlater_videos"},
				{"yt_liked", "youtube_liked_videos"},
				{"yt_monitored", "youtube_monitored_videos"},
			} {
				res, _ := tx.Exec(fmt.Sprintf(
					`INSERT OR IGNORE INTO seen_content (source, item_id, seen_at, queue_id)
					 SELECT '%s', video_id, discovered_at, queue_id FROM %s`,
					migration.source, migration.table,
				))
				if res != nil {
					n, _ := res.RowsAffected()
					rowsCopied += n
				}
			}
			tx.Exec(`DROP TABLE IF EXISTS youtube_watchlater_videos`)
			tx.Exec(`DROP TABLE IF EXISTS youtube_liked_videos`)
			tx.Exec(`DROP TABLE IF EXISTS youtube_monitored_videos`)
			if commitErr := tx.Commit(); commitErr != nil {
				tx.Rollback()
				slog.Error("seen_content_migration_failed", "error", commitErr, "step", "commit")
			} else {
				slog.Info(
					"seen_content_migration_complete",
					"rows_copied", rowsCopied,
					"tables_dropped", 3,
				)
			}
		}
	}

	// EPIC-001: add user_id column to devices for session association.
	db.Exec("ALTER TABLE devices ADD COLUMN user_id INTEGER DEFAULT NULL")
	// EPIC-167 F1: per-device registry uniqueness and active-device listing.
	db.Exec("CREATE UNIQUE INDEX IF NOT EXISTS idx_devices_user_device ON devices(user_id, device_id)")
	db.Exec("CREATE INDEX IF NOT EXISTS idx_devices_user_enabled ON devices(user_id, enabled)")
	// EPIC-013 M2: Bluesky session persistence.
	db.Exec("ALTER TABLE users ADD COLUMN bluesky_session_json TEXT DEFAULT NULL")
	// EPIC-015 M2: Bluesky publish opt-in flag.
	db.Exec("ALTER TABLE users ADD COLUMN bluesky_publish_opt_in INTEGER NOT NULL DEFAULT 0")
	// EPIC-014 M2: YouTube OAuth token persistence.
	db.Exec("ALTER TABLE users ADD COLUMN youtube_refresh_token TEXT NOT NULL DEFAULT ''")
	db.Exec("ALTER TABLE users ADD COLUMN youtube_token_expires_at INTEGER NOT NULL DEFAULT 0")
	// EPIC-181 M1: YouTube multi-account OAuth slots table.
	db.Exec(`CREATE TABLE IF NOT EXISTS youtube_oauth_slots (
		id               INTEGER PRIMARY KEY AUTOINCREMENT,
		user_id          INTEGER NOT NULL REFERENCES users(id),
		slot_name        TEXT    NOT NULL,
		refresh_token    TEXT    NOT NULL DEFAULT '',
		token_expires_at INTEGER NOT NULL DEFAULT 0,
		source           TEXT    NOT NULL DEFAULT 'cli',
		created_at       INTEGER NOT NULL,
		updated_at       INTEGER NOT NULL,
		UNIQUE(user_id, slot_name)
	)`)
	// EPIC-184 F4: existing F-019 databases created youtube_oauth_slots before
	// the source column existed. CREATE TABLE IF NOT EXISTS will not add columns,
	// so keep this additive migration explicit for doctor delegation checks.
	db.Exec("ALTER TABLE youtube_oauth_slots ADD COLUMN source TEXT NOT NULL DEFAULT 'cli'")
	// Migration: copy existing youtube_refresh_token rows to slot "default" (idempotent via INSERT OR IGNORE).
	db.Exec(`INSERT OR IGNORE INTO youtube_oauth_slots
		(user_id, slot_name, refresh_token, token_expires_at, created_at, updated_at)
	SELECT
		id, 'default', youtube_refresh_token, COALESCE(youtube_token_expires_at, 0),
		strftime('%s','now'), strftime('%s','now')
	FROM users
	WHERE youtube_refresh_token IS NOT NULL AND youtube_refresh_token != ''`)

	// FTS5 full-text search index over queue content.
	// EPIC-072 M5: includes topic_tags column. Version sentinel triggers drop+recreate
	// when upgrading from the v1 schema (which lacked topic_tags).
	const fts5Version = 2 // bump to force rebuild when columns change
	var currentFTS5Version int
	db.QueryRow("SELECT COALESCE(MAX(version),0) FROM (SELECT 0 AS version UNION ALL SELECT version FROM fts5_version)").Scan(&currentFTS5Version)
	if currentFTS5Version < fts5Version {
		// Drop old FTS5 objects (idempotent  -  ignore errors if they don't exist).
		db.Exec("DROP TRIGGER IF EXISTS queue_fts_insert")
		db.Exec("DROP TRIGGER IF EXISTS queue_fts_update")
		db.Exec("DROP TRIGGER IF EXISTS queue_fts_delete")
		db.Exec("DROP TABLE IF EXISTS queue_fts")
	}
	db.Exec("CREATE TABLE IF NOT EXISTS fts5_version (version INTEGER)")
	db.Exec("DELETE FROM fts5_version")
	db.Exec("INSERT INTO fts5_version (version) VALUES (?)", fts5Version)

	const fts5Setup = `
		CREATE VIRTUAL TABLE IF NOT EXISTS queue_fts
			USING fts5(url, tags, profile, verdict, topic_tags, content='queue', content_rowid='id');

		CREATE TRIGGER IF NOT EXISTS queue_fts_insert AFTER INSERT ON queue BEGIN
			INSERT INTO queue_fts(rowid, url, tags, profile, verdict, topic_tags)
			VALUES (new.id, new.url, COALESCE(new.tags,''), COALESCE(new.profile,''), COALESCE(new.verdict,''), COALESCE(new.topic_tags,''));
		END;

		CREATE TRIGGER IF NOT EXISTS queue_fts_update AFTER UPDATE ON queue BEGIN
			INSERT INTO queue_fts(queue_fts, rowid, url, tags, profile, verdict, topic_tags)
			VALUES ('delete', old.id, old.url, COALESCE(old.tags,''), COALESCE(old.profile,''), COALESCE(old.verdict,''), COALESCE(old.topic_tags,''));
			INSERT INTO queue_fts(rowid, url, tags, profile, verdict, topic_tags)
			VALUES (new.id, new.url, COALESCE(new.tags,''), COALESCE(new.profile,''), COALESCE(new.verdict,''), COALESCE(new.topic_tags,''));
		END;

		CREATE TRIGGER IF NOT EXISTS queue_fts_delete AFTER DELETE ON queue BEGIN
			INSERT INTO queue_fts(queue_fts, rowid, url, tags, profile, verdict, topic_tags)
			VALUES ('delete', old.id, old.url, COALESCE(old.tags,''), COALESCE(old.profile,''), COALESCE(old.verdict,''), COALESCE(old.topic_tags,''));
		END;
	`
	if _, err := db.Exec(fts5Setup); err != nil {
		db.Close()
		return nil, fmt.Errorf("create fts5 index: %w", err)
	}

	q := &Queue{db: db, debug: debug, traceIDFn: newTraceID}

	// Backfill FTS5 index with any existing rows not yet indexed.
	if err := q.initFTS5(); err != nil {
		slog.Warn("fts5 backfill failed", "error", err)
	}

	if err := q.Prune(); err != nil {
		slog.Warn("queue prune on startup failed", "error", err)
	}
	return q, nil
}

func (q *Queue) newTraceID() string {
	if q != nil && q.traceIDFn != nil {
		return q.traceIDFn()
	}
	return newTraceID()
}

// Enqueue inserts a share request into the queue with status=pending.
// req.ClassifySource (EPIC-077 M1) is persisted to the classify_source column
// to record which pre-enqueue cascade stage determined the profile.
// EPIC-111 F3 M9: generates trace_id (UUID v4) and computes content_hash from
// request text/URL bytes at intake so all pipeline events share a stable trace.
func (q *Queue) Enqueue(req *ShareRequest) (int64, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	traceID := q.newTraceID()
	// content_hash is computed from available request content at intake.
	// For URL shares, this is the URL bytes (refined to fetched content in scoreAsync).
	// For file/audio shares, request.Text carries metadata or transcript path.
	contentData := []byte(req.URL + req.Text)
	contentHashVal := ContentHash(contentData)
	res, err := q.db.Exec(
		`INSERT INTO queue (url, text, type, action, profile, status, queued_at, title, mime_type, calling_package, relative_path, file_name, classify_source, is_screenshot, file_size, slug, trace_id, content_hash, intent, inferred_tags, user_rationale_text, user_rationale_source, user_rationale_duration_ms, capture_mode, source_app, submitted_by_device_id, submitted_by_user_id)
		 VALUES (?, ?, ?, ?, ?, 'pending', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		req.URL, req.Text, req.Type, req.Action, req.Profile, now, req.Title,
		req.MimeType, req.CallingPackage, req.RelativePath, req.Filename, req.ClassifySource,
		boolToInt(req.IsScreenshot), req.FileSize, urlToSlug(req.URL),
		traceID, contentHashVal, req.Intent, req.InferredTagsJSON, req.UserRationaleText, req.UserRationaleSource,
		req.UserRationaleDurationMS, req.CaptureMode, req.SourceApp, req.SubmittedByDeviceID, req.SubmittedByUserID,
	)
	if err != nil {
		return 0, fmt.Errorf("enqueue: %w", err)
	}
	id, _ := res.LastInsertId()
	slog.Debug(
		"queue enqueued",
		"event_type", "queue_enqueue",
		"id", id,
		"type", req.Type,
		"classify_source", req.ClassifySource,
		"trace_id", traceID,
	)
	return id, nil
}

// EnqueuePrefiltered inserts a minimal queue row for a share that was rejected
// before scoring. The row status is 'prefiltered' with the given reason stored
// as the verdict. This satisfies the share→queue row guarantee (EPIC-001 M1)
// and gives operators an audit trail of skipped shares.
// EPIC-001 M4.
func (q *Queue) EnqueuePrefiltered(req *ShareRequest, reason string) (int64, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := q.db.Exec(
		`INSERT INTO queue (url, text, type, action, profile, status, verdict, queued_at, title, mime_type, calling_package, classify_source)
		 VALUES (?, ?, ?, ?, ?, 'prefiltered', ?, ?, ?, ?, ?, ?)`,
		req.URL, req.Text, req.Type, req.Action, req.Profile, reason, now, req.Title,
		req.MimeType, req.CallingPackage, req.ClassifySource,
	)
	if err != nil {
		return 0, fmt.Errorf("enqueue prefiltered: %w", err)
	}
	id, _ := res.LastInsertId()
	slog.Debug(
		"queue enqueued (prefiltered)",
		"event_type", "queue_enqueue_prefiltered",
		"id", id,
		"reason", reason,
		"type", req.Type,
	)
	return id, nil
}

// EnqueueScored inserts a share request as pre-scored (status=scored, score=0).
// Used by auto_score actions (EPIC-057 ginit_*) so the RelayedWatchdog never
// sweeps these rows  -  they skip the pending→relayed→scored progression.
// req.ClassifySource (EPIC-077 M1) is persisted to the classify_source column.
func (q *Queue) EnqueueScored(req *ShareRequest, verdict string) (int64, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := q.db.Exec(
		`INSERT INTO queue (url, text, type, action, profile, status, score, verdict, queued_at, scored_at, title, mime_type, calling_package, relative_path, file_name, classify_source, is_screenshot, file_size)
		 VALUES (?, ?, ?, ?, ?, 'scored', 0, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		req.URL, req.Text, req.Type, req.Action, req.Profile, verdict, now, now, req.Title,
		req.MimeType, req.CallingPackage, req.RelativePath, req.Filename, req.ClassifySource,
		boolToInt(req.IsScreenshot), req.FileSize,
	)
	if err != nil {
		return 0, fmt.Errorf("enqueue scored: %w", err)
	}
	id, _ := res.LastInsertId()
	slog.Debug(
		"queue enqueued (auto-scored)",
		"event_type", "queue_enqueue_scored",
		"id", id,
		"type", req.Type,
		"verdict", verdict,
		"classify_source", req.ClassifySource,
	)
	return id, nil
}

const queueCols = "id, url, text, type, action, profile, status, COALESCE(score,0), COALESCE(tags,''), queued_at, COALESCE(relayed_at,''), COALESCE(scored_at,''), COALESCE(archived_at,''), COALESCE(verdict,''), COALESCE(slug,''), COALESCE(progress,''), COALESCE(outcome,''), COALESCE(outcome_at,''), COALESCE(feedback,''), COALESCE(feedback_at,''), COALESCE(title,''), COALESCE(rubric_scores,''), COALESCE(topic_tags,''), cluster_id, COALESCE(action_route,''), COALESCE(classify_source,''), COALESCE(is_screenshot,0), COALESCE(file_size,0), COALESCE(is_shorts,0), COALESCE(source,''), COALESCE(artifact_path,''), COALESCE(content_warning,''), extraction_confidence, COALESCE(retry_count,0), COALESCE(retry_after,0), COALESCE(error_reason,''), COALESCE(content_hash,''), COALESCE(trace_id,''), COALESCE(user_tags,''), COALESCE(user_rationale_text,''), COALESCE(user_rationale_source,''), COALESCE(user_rationale_duration_ms,0), COALESCE(capture_mode,''), COALESCE(source_app,''), COALESCE(submitted_by_device_id,''), COALESCE(submitted_by_user_id,0)"

// Pending returns all items with status=pending whose retry_after has elapsed,
// ordered by id ASC (FIFO). Rows with retry_after=0 are always included (default).
func (q *Queue) Pending() ([]QueueItem, error) {
	return q.query("SELECT " + queueCols + " FROM queue WHERE status='pending' AND retry_after<=strftime('%s','now') ORDER BY id ASC")
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

// UpdateFailedVerdict writes the verdict column before a failure mark so that
// failed rows carry a human-readable outcome for observability queries.
// Call this immediately before MarkFailedWithReason when a verdict is known.
func (q *Queue) UpdateFailedVerdict(id int64, verdict string) error {
	_, err := q.db.Exec("UPDATE queue SET verdict=? WHERE id=?", verdict, id)
	return err
}

// MarkFailedWithReason sets status=failed and records an error_reason.
// EPIC-054 M3: used by the relayed-state watchdog to classify timeouts.
// EPIC-264 M5: emits its own centralized structured ERROR — a row reaching
// terminal failure is the most important operational fact in the pipeline
// and previously had zero log presence (silent DB write).
func (q *Queue) MarkFailedWithReason(id int64, reason string) error {
	_, err := q.db.Exec(
		"UPDATE queue SET status='failed', error_reason=? WHERE id=?",
		reason, id,
	)
	slog.Error("queue row terminal failure",
		"event_type", "row_terminal_failure",
		"row_id", id,
		"reason", reason,
	)
	return err
}

// EnqueueAudioRetry resets a failed audio-fallback row to pending with an
// exponential backoff delay. retryCount is the new total attempt count (1-based).
// Backoff schedule: attempt 1→30s, 2→120s, 3+→600s. EPIC-108 M3.
func (q *Queue) EnqueueAudioRetry(id int64, retryCount int) error {
	backoffs := []int64{30, 120, 600}
	idx := retryCount - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(backoffs) {
		idx = len(backoffs) - 1
	}
	retryAfter := time.Now().Unix() + backoffs[idx]
	_, err := q.db.Exec(
		"UPDATE queue SET status='pending', retry_count=?, retry_after=?, error_reason='yt_audio_pending_retry' WHERE id=?",
		retryCount, retryAfter, id,
	)
	return err
}

// SetContentWarning writes the content_warning field on a queue row.
// Called by scoreAsync when LiteParse returns an error (EPIC-102).
func (q *Queue) SetContentWarning(id int64, warning string) error {
	_, err := q.db.Exec("UPDATE queue SET content_warning=? WHERE id=?", warning, id)
	return err
}

// SetExtractionConfidence writes the mean per-page extraction confidence to a
// queue row. confidence==-1.0 signals JSON parse fallback; NULL (not written)
// means non-PDF or pre-feature. Called by scoreAsync after LiteParse. EPIC-104.
func (q *Queue) SetExtractionConfidence(id int64, confidence float64) error {
	_, err := q.db.Exec("UPDATE queue SET extraction_confidence=? WHERE id=?", confidence, id)
	return err
}

// SetCaptured marks a queue row as captured (terminal status) and records the artifact path.
// The UPDATE guard excludes already-terminal rows (captured, failed, archived) to prevent
// double-write races when the same URL is shared twice rapidly.
func (q *Queue) SetCaptured(id int64, artifactPath string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := q.db.Exec(
		"UPDATE queue SET status='captured', artifact_path=?, scored_at=? WHERE id=? AND status NOT IN ('captured','failed','archived')",
		artifactPath, now, id,
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
// already scored or not found (lost the race  -  safe to ignore).
// EPIC-055 M1.
func (q *Queue) IngestScoreIfRelayed(id int64, score int, tags, verdict, slug, promptHashVal, promptVersionVal string, rubricScores ...map[string]int) (bool, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	rubricJSON := ""
	if len(rubricScores) > 0 && rubricScores[0] != nil {
		if b, err := json.Marshal(rubricScores[0]); err == nil {
			rubricJSON = string(b)
		}
	}
	res, err := q.db.Exec(
		`UPDATE queue SET status='scored', score=?, tags=?, verdict=?, slug=?, scored_at=?, rubric_scores=?, prompt_hash=?, prompt_version=?
		 WHERE id=? AND status='relayed'`,
		score, tags, verdict, slug, now, rubricJSON, promptHashVal, promptVersionVal, id,
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
// caller). The WHERE status='relayed' filter guarantees idempotency  -  a row
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
// EPIC-082 M1: accepts prompt_hash and prompt_version for traceability.
func (q *Queue) UpdateScore(id int64, score int, tags, verdict, slug, promptHashVal, promptVersionVal string, rubricScores ...map[string]int) error {
	now := time.Now().UTC().Format(time.RFC3339)
	rubricJSON := ""
	if len(rubricScores) > 0 && rubricScores[0] != nil {
		if b, err := json.Marshal(rubricScores[0]); err == nil {
			rubricJSON = string(b)
		}
	}
	_, err := q.db.Exec(
		"UPDATE queue SET status='scored', score=?, tags=?, verdict=?, slug=?, scored_at=?, rubric_scores=?, prompt_hash=?, prompt_version=?, progress='' WHERE id=?",
		score, tags, verdict, slug, now, rubricJSON, promptHashVal, promptVersionVal, id,
	)
	return err
}

// ScoreByID updates a queue row to scored status, using row ID as the key.
// Used by scoreAsync (EPIC-077 M5) for file shares where no URL is available
// for URL-based dedup lookup. Includes an idempotency guard modeled on
// ScoreByURL: if the row is already scored or archived, the update is skipped.
// Returns (true, nil) when the row was updated; (false, nil) when already scored/archived.
func (q *Queue) ScoreByID(rowID int64, score int, tags, verdict, slug, promptHashVal, promptVersionVal string, rubricScores ...map[string]int) (bool, error) {
	// Idempotency guard: skip if already scored or archived.
	var status string
	err := q.db.QueryRow("SELECT status FROM queue WHERE id=?", rowID).Scan(&status)
	if err != nil {
		return false, fmt.Errorf("ScoreByID status check id=%d: %w", rowID, err)
	}
	if status == "scored" || status == "archived" {
		return false, nil
	}

	now := time.Now().UTC().Format(time.RFC3339)
	rubricJSON := ""
	if len(rubricScores) > 0 && rubricScores[0] != nil {
		if b, err := json.Marshal(rubricScores[0]); err == nil {
			rubricJSON = string(b)
		}
	}
	res, err := q.db.Exec(
		"UPDATE queue SET status='scored', score=?, tags=?, verdict=?, slug=?, scored_at=?, rubric_scores=?, prompt_hash=?, prompt_version=? WHERE id=?",
		score, tags, verdict, slug, now, rubricJSON, promptHashVal, promptVersionVal, rowID,
	)
	if err != nil {
		return false, fmt.Errorf("ScoreByID update id=%d: %w", rowID, err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// UpdateScoringCost persists the per-call scoring cost and image token estimate
// after ScoreByURL/ScoreByID. Called from scoreAsync after vision back-calculation
// so the corrected image_tokens_estimated value is written. EPIC-088 M3.
func (q *Queue) UpdateScoringCost(rowID int64, costUSD float64, imageTokensEstimated int) error {
	_, err := q.db.Exec(
		"UPDATE queue SET scoring_cost_usd=?, image_tokens_estimated=? WHERE id=?",
		costUSD, imageTokensEstimated, rowID,
	)
	return err
}

// SetActionRoute persists the action_route on a queue item (EPIC-072 M9).
func (q *Queue) SetActionRoute(id int64, route string) error {
	_, err := q.db.Exec("UPDATE queue SET action_route=? WHERE id=?", route, id)
	return err
}

// SetTopicTags persists topic_tags (JSON array) on a queue item (EPIC-072 M5).
func (q *Queue) SetTopicTags(id int64, topicTags []string) error {
	if len(topicTags) == 0 {
		return nil
	}
	b, err := json.Marshal(topicTags)
	if err != nil {
		return err
	}
	_, err = q.db.Exec("UPDATE queue SET topic_tags=? WHERE id=?", string(b), id)
	return err
}

// SetWikiContext persists wiki context state for a queue row (EPIC-180 M4).
func (q *Queue) SetWikiContext(id int64, used bool, topic string) error {
	usedInt := 0
	if used {
		usedInt = 1
	}
	_, err := q.db.Exec("UPDATE queue SET wiki_context_used=?, wiki_topic=? WHERE id=?", usedInt, topic, id)
	return err
}

// SetPushWikiTopic stores the wiki topic on a push_outbox row for FCM payload inclusion (EPIC-180 M4).
func (q *Queue) SetPushWikiTopic(id int64, topic string) error {
	_, err := q.db.Exec("UPDATE push_outbox SET wiki_topic=? WHERE id=?", topic, id)
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

// feedbackAliases maps Android-native vocabulary to canonical feedback values (EPIC-072 M1).
var feedbackAliases = map[string]string{
	"positive": "accurate",
	"negative": "too_low",
}

// normalizeFeedback maps aliases to canonical values and validates.
func normalizeFeedback(feedback string) (string, error) {
	if canonical, ok := feedbackAliases[feedback]; ok {
		feedback = canonical
	}
	if !validFeedbacks[feedback] {
		return "", fmt.Errorf("invalid feedback %q: must be accurate, too_high, too_low, positive, or negative", feedback)
	}
	return feedback, nil
}

// UpdateOutcome sets the outcome and outcome_at on a queue item.
// Idempotent: if the row already carries the same outcome value, no write is performed.
func (q *Queue) UpdateOutcome(id int64, outcome string) error {
	if !validOutcomes[outcome] {
		return fmt.Errorf("invalid outcome %q: must be acted, ignored, or deferred", outcome)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := q.db.Exec(
		"UPDATE queue SET outcome=?, outcome_at=? WHERE id=? AND (outcome IS NULL OR outcome != ?)",
		outcome, now, id, outcome,
	)
	return err
}

// UpdateFeedback sets the feedback and feedback_at on a queue item.
// Accepts both canonical values (accurate, too_high, too_low) and Android aliases (positive, negative).
func (q *Queue) UpdateFeedback(id int64, feedback string) error {
	normalized, err := normalizeFeedback(feedback)
	if err != nil {
		return err
	}
	feedback = normalized
	now := time.Now().UTC().Format(time.RFC3339)
	_, err = q.db.Exec("UPDATE queue SET feedback=?, feedback_at=? WHERE id=?", feedback, now, id)
	return err
}

// ProfileStat holds aggregate scoring/feedback stats for a single profile.
type ProfileStat struct {
	Profile                   string             `json:"profile"`
	Count                     int                `json:"count"`
	AvgScore                  float64            `json:"avg_score"`
	AccurateCount             int                `json:"accurate_count"`
	TooHighCount              int                `json:"too_high_count"`
	TooLowCount               int                `json:"too_low_count"`
	FeedbackCount             int                `json:"feedback_count"`
	RubricAverages            map[string]float64 `json:"rubric_averages,omitempty"`
	AvgScoreActed             *float64           `json:"avg_score_acted,omitempty"`
	AvgScoreIgnored           *float64           `json:"avg_score_ignored,omitempty"`
	DriftScore                *float64           `json:"drift_score,omitempty"`
	CalibrationRecommendation string             `json:"calibration_recommendation,omitempty"`
	MisclassifyBySource       map[string]int     `json:"misclassify_by_source,omitempty"`      // EPIC-082 M2
	ScoreBuckets              map[string]int     `json:"score_buckets,omitempty"`              // EPIC-082 M3
	AvgScore7d                *float64           `json:"avg_score_7d,omitempty"`               // EPIC-082 M3
	AvgScore30d               *float64           `json:"avg_score_30d,omitempty"`              // EPIC-082 M3
	FeedbackCalibrationScore  *float64           `json:"feedback_calibration_score,omitempty"` // EPIC-082 M3: (TooHigh-TooLow)/FeedbackCount
	WikiEnrichedCount         int                `json:"wiki_enriched_count,omitempty"`        // EPIC-180 M4: items scored with wiki context
	WikiEnrichedThumbsUp      int                `json:"wiki_enriched_thumbs_up,omitempty"`    // EPIC-180 M4: wiki-enriched items with outcome=acted
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
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Compute per-axis rubric averages (EPIC-072 M2) and drift scores (EPIC-072 M4).
	for i := range stats {
		avgs, err := q.rubricAverages(stats[i].Profile)
		if err != nil {
			slog.Warn("rubric averages failed", "profile", stats[i].Profile, "error", err)
		} else if len(avgs) > 0 {
			stats[i].RubricAverages = avgs
		}

		q.computeDrift(&stats[i])

		// EPIC-082 M2: misclassification audit by classify_source.
		if mcs, err := q.misclassifyBySource(stats[i].Profile); err != nil {
			slog.Warn("misclassify_by_source failed", "profile", stats[i].Profile, "error", err)
		} else if len(mcs) > 0 {
			stats[i].MisclassifyBySource = mcs
		}

		// EPIC-082 M3: score distribution buckets.
		if bkts, err := q.scoreBuckets(stats[i].Profile); err != nil {
			slog.Warn("score_buckets failed", "profile", stats[i].Profile, "error", err)
		} else {
			stats[i].ScoreBuckets = bkts
		}

		// EPIC-082 M3: rolling averages.
		stats[i].AvgScore7d, _ = q.rollingAvgScore(stats[i].Profile, 7)
		stats[i].AvgScore30d, _ = q.rollingAvgScore(stats[i].Profile, 30)

		// EPIC-082 M3: feedback calibration score  -  signed bias.
		if stats[i].FeedbackCount > 0 {
			fcs := float64(stats[i].TooHighCount-stats[i].TooLowCount) / float64(stats[i].FeedbackCount)
			stats[i].FeedbackCalibrationScore = &fcs
		}

		// EPIC-180 M4: wiki enrichment counts (per-profile sub-query).
		var wikiCount, wikiThumbsUp int
		_ = q.db.QueryRow(
			"SELECT COUNT(*), COALESCE(SUM(CASE WHEN outcome='acted' THEN 1 ELSE 0 END),0) FROM queue WHERE wiki_context_used=1 AND status IN ('scored','archived') AND profile=?",
			stats[i].Profile,
		).Scan(&wikiCount, &wikiThumbsUp)
		if wikiCount > 0 {
			stats[i].WikiEnrichedCount = wikiCount
			stats[i].WikiEnrichedThumbsUp = wikiThumbsUp
		}
	}
	return stats, nil
}

// computeDrift calculates score-vs-outcome drift for a profile (EPIC-072 M4).
func (q *Queue) computeDrift(s *ProfileStat) {
	var avgActed, avgIgnored sql.NullFloat64
	_ = q.db.QueryRow(
		"SELECT AVG(score) FROM queue WHERE profile=? AND status IN ('scored','archived') AND outcome='acted'",
		s.Profile,
	).Scan(&avgActed)
	_ = q.db.QueryRow(
		"SELECT AVG(score) FROM queue WHERE profile=? AND status IN ('scored','archived') AND outcome='ignored'",
		s.Profile,
	).Scan(&avgIgnored)

	if avgActed.Valid {
		s.AvgScoreActed = &avgActed.Float64
	}
	if avgIgnored.Valid {
		s.AvgScoreIgnored = &avgIgnored.Float64
	}

	if avgActed.Valid && avgIgnored.Valid {
		drift := math.Abs(avgActed.Float64 - avgIgnored.Float64)
		s.DriftScore = &drift
		if drift < 10 {
			s.CalibrationRecommendation = "low_drift_well_calibrated"
		} else if drift < 25 {
			s.CalibrationRecommendation = "moderate_drift_review_thresholds"
		} else {
			s.CalibrationRecommendation = "high_drift_recalibrate_rubric"
		}
	}
}

// misclassifyBySource returns negative-feedback counts grouped by classify_source (EPIC-082 M2).
func (q *Queue) misclassifyBySource(profile string) (map[string]int, error) {
	rows, err := q.db.Query(
		`SELECT classify_source, COUNT(*) FROM queue
		 WHERE profile=? AND status IN ('scored','archived')
		 AND feedback IN ('too_high','too_low') AND classify_source!=''
		 GROUP BY classify_source`,
		profile,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := map[string]int{}
	for rows.Next() {
		var src string
		var cnt int
		if err := rows.Scan(&src, &cnt); err != nil {
			return nil, err
		}
		result[src] = cnt
	}
	return result, rows.Err()
}

// scoreBuckets returns score distribution across 5 buckets (EPIC-082 M3).
func (q *Queue) scoreBuckets(profile string) (map[string]int, error) {
	row := q.db.QueryRow(`SELECT
		SUM(CASE WHEN score BETWEEN 0 AND 19 THEN 1 ELSE 0 END),
		SUM(CASE WHEN score BETWEEN 20 AND 39 THEN 1 ELSE 0 END),
		SUM(CASE WHEN score BETWEEN 40 AND 59 THEN 1 ELSE 0 END),
		SUM(CASE WHEN score BETWEEN 60 AND 79 THEN 1 ELSE 0 END),
		SUM(CASE WHEN score BETWEEN 80 AND 100 THEN 1 ELSE 0 END)
		FROM queue WHERE profile=? AND status IN ('scored','archived') AND score IS NOT NULL`, profile)
	var b0, b1, b2, b3, b4 int
	if err := row.Scan(&b0, &b1, &b2, &b3, &b4); err != nil {
		return nil, err
	}
	return map[string]int{
		"0-19": b0, "20-39": b1, "40-59": b2, "60-79": b3, "80-100": b4,
	}, nil
}

// rollingAvgScore returns the average score for a profile over the last N days (EPIC-082 M3).
func (q *Queue) rollingAvgScore(profile string, days int) (*float64, error) {
	var avg sql.NullFloat64
	err := q.db.QueryRow(
		`SELECT AVG(score) FROM queue WHERE profile=? AND status IN ('scored','archived')
		 AND scored_at >= datetime('now', ? || ' days')`,
		profile, fmt.Sprintf("-%d", days),
	).Scan(&avg)
	if err != nil || !avg.Valid {
		return nil, err
	}
	return &avg.Float64, nil
}

// rubricAverages computes per-axis average rubric scores for a profile.
func (q *Queue) rubricAverages(profile string) (map[string]float64, error) {
	rows, err := q.db.Query(
		"SELECT rubric_scores FROM queue WHERE profile=? AND status IN ('scored','archived') AND rubric_scores!=''",
		profile,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	sums := map[string]float64{}
	counts := map[string]int{}
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var rubric map[string]int
		if err := json.Unmarshal([]byte(raw), &rubric); err != nil {
			continue
		}
		for axis, score := range rubric {
			sums[axis] += float64(score)
			counts[axis]++
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	avgs := make(map[string]float64, len(sums))
	for axis, sum := range sums {
		avgs[axis] = sum / float64(counts[axis])
	}
	return avgs, nil
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
	ScoreMin  *int
	ScoreMax  *int
	Since     string // RFC3339
	Until     string // RFC3339
	ClusterID *int64 // EPIC-072 M7
	UserTag   string // EPIC-153: filter to rows containing this tag in user_tags JSON array
}

// ListArchivedCursorTyped extends ListArchivedCursor with type and score/date filters.
// itemType "jira" matches ginit_* actions; "url" matches non-ginit actions;
// empty string disables the filter. No schema migration required  -  type is
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
		if filter.ClusterID != nil {
			sqlStr += " AND cluster_id = ?"
			args = append(args, *filter.ClusterID)
		}
		if filter.UserTag != "" {
			// EPIC-153: filter to rows whose user_tags JSON array contains the value.
			// Use NULLIF to convert empty-string (default) to NULL before passing to
			// json_each; json_each(NULL) returns zero rows safely. This avoids a
			// SQLite error from json_each('') which receives a non-JSON empty string.
			sqlStr += " AND EXISTS (SELECT 1 FROM json_each(NULLIF(queue.user_tags,'')) WHERE value = ?)"
			args = append(args, filter.UserTag)
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

// CountRecentScored returns the total number of items scored since the given time.
func (q *Queue) CountRecentScored(since time.Time) (int, error) {
	var count int
	err := q.db.QueryRow("SELECT COUNT(*) FROM queue WHERE status IN ('scored','archived') AND scored_at >= ?", since.UTC().Format(time.RFC3339)).Scan(&count)
	return count, err
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

// GetBySlug returns the most recent queue item matching slug.
func (q *Queue) GetBySlug(slug string) (*QueueItem, error) {
	items, err := q.query("SELECT "+queueCols+" FROM queue WHERE slug=? ORDER BY id DESC LIMIT 1", slug)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, fmt.Errorf("queue item with slug %q not found", slug)
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
// VACUUM INTO. The destination file is removed first because VACUUM INTO refuses
// to overwrite an existing file. Intended for periodic point-in-time backups  -
// if queue.db becomes corrupt, the last snapshot is the recovery baseline before
// attempting sqlite3 .recover.
func (q *Queue) Snapshot(destPath string) error {
	if err := os.Remove(destPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("snapshot remove old %s: %w", destPath, err)
	}
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
func (q *Queue) ScoreByURL(url string, score int, verdict, tags, profile, slug, promptHashVal, promptVersionVal string, rubricScores ...map[string]int) (*QueueItem, bool, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	rubricJSON := ""
	if len(rubricScores) > 0 && rubricScores[0] != nil {
		if b, err := json.Marshal(rubricScores[0]); err == nil {
			rubricJSON = string(b)
		}
	}

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

	// Try to find an existing pending or relayed item by URL.
	// 'pending' handles the firehose flow (EPIC-123 M3: MarkRelayed removed);
	// 'relayed' handles the existing HTTP share flow.
	relayed, err := q.query(
		"SELECT "+queueCols+" FROM queue WHERE url=? AND status IN ('pending','relayed') ORDER BY id DESC LIMIT 1",
		url,
	)
	if err != nil {
		return nil, false, err
	}

	if len(relayed) > 0 {
		id := relayed[0].ID
		// Include profile in the UPDATE so that auto-classified profiles (set in
		// scoreAsync after enqueue) are persisted on the queue row. The enqueued
		// row may carry an empty profile when the caller did not know the profile
		// at share time; the scoring path resolves it and passes it here.
		_, err = q.db.Exec(
			"UPDATE queue SET status='scored', score=?, tags=?, verdict=?, slug=?, scored_at=?, rubric_scores=?, prompt_hash=?, prompt_version=?, profile=? WHERE id=?",
			score, tags, verdict, slug, now, rubricJSON, promptHashVal, promptVersionVal, profile, id,
		)
		if err != nil {
			return nil, false, fmt.Errorf("ScoreByURL update: %w", err)
		}
		item, err := q.GetByID(id)
		return item, false, err
	}

	// INSERT path  -  CLI-originated score with no prior relayed row.
	res, err := q.db.Exec(
		`INSERT INTO queue (url, text, type, action, profile, status, queued_at, scored_at, score, tags, verdict, slug, rubric_scores, prompt_hash, prompt_version)
		 VALUES (?, '', 'url', '', ?, 'scored', ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		url, profile, now, now, score, tags, verdict, slug, rubricJSON, promptHashVal, promptVersionVal,
	)
	if err != nil {
		return nil, false, fmt.Errorf("ScoreByURL insert: %w", err)
	}
	id, _ := res.LastInsertId()
	item, err := q.GetByID(id)
	return item, true, err
}

// FindRecentFile returns the most-recent queue row whose file_name and
// file_size match and whose queued_at is within window of now, excluding
// failed and archived rows. Returns (nil, nil) when no match is found.
//
// EPIC-078 M5: used by handleShare as a pre-enqueue dedup guard for
// repeated file shares (same file shared multiple times in quick succession).
func (q *Queue) FindRecentFile(filename string, fileSize int64, window time.Duration) (*QueueItem, error) {
	if filename == "" || fileSize <= 0 || window <= 0 {
		return nil, nil
	}
	cutoff := time.Now().UTC().Add(-window).Format(time.RFC3339)
	rows, err := q.query(
		"SELECT "+queueCols+" FROM queue"+
			" WHERE file_name = ? AND file_size = ? AND queued_at > ?"+
			" AND status NOT IN ('failed','archived')"+
			" ORDER BY id DESC LIMIT 1",
		filename, fileSize, cutoff,
	)
	if err != nil {
		return nil, fmt.Errorf("FindRecentFile: %w", err)
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return &rows[0], nil
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
	ID             int64
	Score          int
	Slug           string
	Verdict        string
	URL            string
	Kind           string
	Profile        string // EPIC-061: auto-classified profile for FCM payload
	Status         string
	Attempts       int
	NextAttempt    int64
	CreatedAt      int64
	UpdatedAt      int64
	LastError      string
	GapSummary     string // EPIC-058 M7
	ContentType    string // EPIC-071 M3: "voice_note" for audio shares
	ClassifySource string // EPIC-077 M6: cascade stage that produced the profile
	ContentWarning string // EPIC-102: "lit_parse_failed" when extraction failed
	ErrorReason    string // EPIC-111 F2 M6: populated for status=failed pushes
	TargetDeviceID string // EPIC-167 F4: route to this device only
	TargetUserID   int64  // EPIC-167 F4: target device owner
	PushKind       string // EPIC-167 F4: semantic kind (e.g. score_complete)
	WikiTopic      string // EPIC-180 M4: non-empty when wiki context was used
}

// EnqueuePush inserts a pending row into push_outbox and returns its id.
// Profile defaults to "" for legacy callers. Prefer EnqueuePushWithProfile
// or EnqueueDigestIfDue for new code.
func (q *Queue) EnqueuePush(kind string, score int, slug, verdict, url string) (int64, error) {
	return q.EnqueuePushWithProfile(kind, "", score, slug, verdict, url, "")
}

// EnqueuePushWithProfile is the profile-aware primitive used by
// EnqueueDigestIfDue. Direct callers are discouraged outside the unified
// helper  -  EPIC-051 M3 will consolidate call sites behind EnqueueDigestIfDue.
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

// EnqueueDevicePush inserts a score-complete push targeted at the originating device.
func (q *Queue) EnqueueDevicePush(profile string, score int, slug, verdict, url string, targetUserID int64, targetDeviceID string) (int64, error) {
	now := time.Now().Unix()
	res, err := q.db.Exec(
		`INSERT INTO push_outbox (score, slug, verdict, url, kind, profile, push_kind, target_user_id, target_device_id, status, attempts, next_attempt, created_at, updated_at)
		 VALUES (?, ?, ?, ?, 'digest', ?, 'score_complete', ?, ?, 'pending', 0, ?, ?, ?)`,
		score, slug, verdict, url, profile, targetUserID, targetDeviceID, now, now, now,
	)
	if err != nil {
		return 0, fmt.Errorf("enqueue device push: %w", err)
	}
	id, _ := res.LastInsertId()
	return id, nil
}

// EnqueueDigestResult holds the outcome of EnqueueDigestIfDue. A successful
// enqueue populates ID; a suppressed call leaves ID zero and records Reason.
type EnqueueDigestResult struct {
	Enqueued            bool
	Reason              string // "enqueued", "throttled", "below_min_score"
	ID                  int64  // row id when enqueued; 0 otherwise
	SecondsUntilAllowed int64  // populated on throttled
	ThrottleRemainingMs int64  // populated on enqueued (throttle window length ms)
}

// EnqueueDigestIfDue is the single sanctioned entry point for writing a
// digest row to push_outbox. It applies the NotifyMinScore floor, consults
// the per-profile throttle from the live PushConfig, and atomically inserts
// a new row iff the window has elapsed. Safe for concurrent use across
// multiple processes  -  the throttle check + insert happen inside a single
// SQLite IMMEDIATE transaction so two racing linkari processes can't both
// write a digest row inside the same window.
//
// EPIC-051 M2. See M1 decision in the epic Notes for the NotifyMinScore
// rationale (Position B  -  honor as a uniform floor).
func (q *Queue) EnqueueDigestIfDue(ctx context.Context, profile string, score int, slug, verdict, url string, gapSummary ...string) (EnqueueDigestResult, error) {
	cfg := q.PushConfig()

	// EPIC-001 M2: eval_failed verdict must never trigger an FCM push  -  the row
	// was not scored, so there is nothing meaningful to notify the user about.
	if verdict == "eval_failed" {
		return EnqueueDigestResult{Reason: "eval_failed_skip"}, nil
	}

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
	if err := tx.QueryRowContext(
		ctx,
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
	cs := ""
	if len(gapSummary) > 2 {
		cs = gapSummary[2] // EPIC-077 M6: optional classify_source
	}
	cw := ""
	if len(gapSummary) > 3 {
		cw = gapSummary[3] // EPIC-102: optional content_warning
	}
	res, err := tx.ExecContext(
		ctx,
		`INSERT INTO push_outbox (score, slug, verdict, url, kind, profile, gap_summary, content_type, classify_source, content_warning, status, attempts, next_attempt, created_at, updated_at)
		 VALUES (?, ?, ?, ?, 'digest', ?, ?, ?, ?, ?, 'pending', 0, ?, ?, ?)`,
		score, slug, verdict, url, profile, gs, ct, cs, cw, now, now, now,
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

// EnqueuePrefilterPush inserts a push_outbox row for a prefilter skip/failure
// notification. Bypasses min-score floor and throttle  -  prefilter events are
// low-volume and the user should always be notified when their share was not
// scored. EPIC-084 M2.
func (q *Queue) EnqueuePrefilterPush(profile, slug, verdict, url string) error {
	now := time.Now().Unix()
	_, err := q.db.Exec(
		`INSERT INTO push_outbox (score, slug, verdict, url, kind, profile, gap_summary, content_type, classify_source, status, attempts, next_attempt, created_at, updated_at)
		 VALUES (0, ?, ?, ?, 'digest', ?, '', 'prefilter', '', 'pending', 0, ?, ?, ?)`,
		slug, verdict, url, profile, now, now, now,
	)
	return err
}

// EnqueueTranscriptPush inserts a push_outbox row for a YouTube transcript-only
// notification. Uses content_type='youtube_transcript' so sendOutboxFCM renders
// a transcript-oriented title/body. Bypasses min-score floor and throttle  -
// transcript delivery should always notify. EPIC-090 M2.
func (q *Queue) EnqueueTranscriptPush(profile, slug, verdict, url string) error {
	now := time.Now().Unix()
	_, err := q.db.Exec(
		`INSERT INTO push_outbox (score, slug, verdict, url, kind, profile, gap_summary, content_type, classify_source, status, attempts, next_attempt, created_at, updated_at)
		 VALUES (0, ?, ?, ?, 'transcript', ?, '', 'youtube_transcript', '', 'pending', 0, ?, ?, ?)`,
		slug, verdict, url, profile, now, now, now,
	)
	return err
}

// SetPushContentType updates the content_type field of a push_outbox row.
// Used by scoreYouTubeAsync after EnqueueDigestIfDue to tag the row as
// content_type='youtube' so sendOutboxFCM renders a YouTube-specific title.
// EPIC-090 M5.
func (q *Queue) SetPushContentType(id int64, contentType string) error {
	_, err := q.db.Exec(`UPDATE push_outbox SET content_type = ? WHERE id = ?`, contentType, id)
	return err
}

// SetIsShorts marks a queue row as a YouTube Short (is_shorts=1) or clears
// the flag (is_shorts=0). Called by scoreYouTubeAsync after detectShorts.
// EPIC-012 M3.
func (q *Queue) SetIsShorts(rowID int64, isShorts bool) error {
	_, err := q.db.Exec(`UPDATE queue SET is_shorts = ? WHERE id = ?`, boolToInt(isShorts), rowID)
	return err
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
		`SELECT id, score, slug, verdict, url, kind, profile, status, attempts, next_attempt, created_at, updated_at, last_error, gap_summary, content_type, COALESCE(classify_source,''), COALESCE(content_warning,''), COALESCE(error_reason,''), COALESCE(target_device_id,''), COALESCE(target_user_id,0), COALESCE(push_kind,''), COALESCE(wiki_topic,'')
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
		if err := rows.Scan(&p.ID, &p.Score, &p.Slug, &p.Verdict, &p.URL, &p.Kind, &p.Profile, &p.Status, &p.Attempts, &p.NextAttempt, &p.CreatedAt, &p.UpdatedAt, &p.LastError, &p.GapSummary, &p.ContentType, &p.ClassifySource, &p.ContentWarning, &p.ErrorReason, &p.TargetDeviceID, &p.TargetUserID, &p.PushKind, &p.WikiTopic); err != nil {
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

// PersistBlueskySession stores a Bluesky session as JSON for the given user.
// EPIC-013 M2.
func (q *Queue) PersistBlueskySession(userID int64, data BlueskySessionData) error {
	b, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("marshal bluesky session: %w", err)
	}
	_, err = q.db.Exec("UPDATE users SET bluesky_session_json=? WHERE id=?", string(b), userID)
	return err
}

// LoadBlueskySession reads the persisted Bluesky session for the given user.
// Returns nil, nil when no session is stored.
func (q *Queue) LoadBlueskySession(userID int64) (*BlueskySessionData, error) {
	var raw sql.NullString
	if err := q.db.QueryRow("SELECT bluesky_session_json FROM users WHERE id=?", userID).Scan(&raw); err != nil {
		return nil, err
	}
	if !raw.Valid || raw.String == "" {
		return nil, nil
	}
	var data BlueskySessionData
	if err := json.Unmarshal([]byte(raw.String), &data); err != nil {
		return nil, fmt.Errorf("unmarshal bluesky session: %w", err)
	}
	return &data, nil
}

// UpdateBlueskySession replaces the stored Bluesky session (called on token refresh).
func (q *Queue) UpdateBlueskySession(userID int64, data BlueskySessionData) error {
	return q.PersistBlueskySession(userID, data)
}

// SetBlueskyPublishOptIn sets the bluesky_publish_opt_in flag for the given user.
// EPIC-015 M2.
func (q *Queue) SetBlueskyPublishOptIn(userID int64, optIn bool) error {
	v := 0
	if optIn {
		v = 1
	}
	_, err := q.db.Exec("UPDATE users SET bluesky_publish_opt_in=? WHERE id=?", v, userID)
	return err
}

// GetBlueskyPublishOptIn returns the bluesky_publish_opt_in flag for the given user.
// Returns false on DB error (default-safe  -  opt-out is the safe default).
// EPIC-015 M2.
func (q *Queue) GetBlueskyPublishOptIn(userID int64) (bool, error) {
	var v int
	err := q.db.QueryRow("SELECT COALESCE(bluesky_publish_opt_in,0) FROM users WHERE id=?", userID).Scan(&v)
	if err != nil {
		return false, nil // default-safe
	}
	return v == 1, nil
}

// GetYouTubeRefreshToken returns the stored YouTube refresh token and expiry for user_id=1.
// Returns ("", 0, nil) when no token is stored.
func (q *Queue) GetYouTubeRefreshToken(profile string) (token string, expiresAt int64, err error) {
	err = q.db.QueryRow(
		`SELECT COALESCE(youtube_refresh_token,''), COALESCE(youtube_token_expires_at,0)
         FROM users WHERE id=1`,
	).Scan(&token, &expiresAt)
	if err == sql.ErrNoRows {
		return "", 0, nil
	}
	return token, expiresAt, err
}

// SetYouTubeRefreshToken persists a YouTube refresh token for user_id=1 (single-user).
// Soak-window write-through: also upserts to youtube_oauth_slots "default" so that
// callers using the old API continue to work after the slot-based lookup is live.
func (q *Queue) SetYouTubeRefreshToken(profile, token string, expiresAt int64) error {
	_, err := q.db.Exec(
		`UPDATE users SET youtube_refresh_token=?, youtube_token_expires_at=? WHERE id=1`,
		token, expiresAt,
	)
	if err != nil {
		return err
	}
	return q.SetYouTubeSlotToken(1, "default", token, expiresAt)
}

// GetYouTubeSlotToken retrieves the stored refresh token and expiry for a named OAuth slot.
// Returns sql.ErrNoRows if the slot has no stored token (slot was never authed).
func (q *Queue) GetYouTubeSlotToken(userID int64, slot string) (refreshToken string, expiresAt int64, err error) {
	err = q.db.QueryRow(
		`SELECT refresh_token, token_expires_at FROM youtube_oauth_slots WHERE user_id=? AND slot_name=?`,
		userID, slot,
	).Scan(&refreshToken, &expiresAt)
	return refreshToken, expiresAt, err
}

// SetYouTubeSlotToken upserts a refresh token for a named OAuth slot.
// Creates the row if absent; replaces in place if present.
// source is set to "cli" (default for backwards compat with existing callers).
func (q *Queue) SetYouTubeSlotToken(userID int64, slot string, refreshToken string, expiresAt int64) error {
	return q.SetYouTubeSlotTokenWithSource(userID, slot, refreshToken, expiresAt, "cli")
}

// SetYouTubeSlotTokenWithSource is like SetYouTubeSlotToken but records the auth source.
// source is "cli" for linkari auth youtube --slot, "android" for POST /auth/youtube. EPIC-184 M1.
func (q *Queue) SetYouTubeSlotTokenWithSource(userID int64, slot string, refreshToken string, expiresAt int64, source string) error {
	now := time.Now().Unix()
	_, err := q.db.Exec(
		`INSERT OR REPLACE INTO youtube_oauth_slots
			(user_id, slot_name, refresh_token, token_expires_at, source, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		userID, slot, refreshToken, expiresAt, source, now, now,
	)
	return err
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
	// EPIC-001 M2: eval_failed is a distinct pipeline error, not a content
	// quality signal. Check it first so it doesn't fall through to "skipped".
	if verdict == "eval_failed" {
		return "eval_failed"
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
		var isScreenshotInt, isShortsInt int
		if err := rows.Scan(&it.ID, &it.URL, &it.Text, &it.Type, &it.Action, &it.Profile, &it.Status, &score, &it.Tags, &it.QueuedAt, &it.RelayedAt, &it.ScoredAt, &it.ArchivedAt, &it.Verdict, &it.Slug, &it.Progress, &it.Outcome, &it.OutcomeAt, &it.Feedback, &it.FeedbackAt, &it.Title, &it.RubricScores, &it.TopicTags, &it.ClusterID, &it.ActionRoute, &it.ClassifySource, &isScreenshotInt, &it.FileSize, &isShortsInt, &it.Source, &it.ArtifactPath, &it.ContentWarning, &it.ExtractionConfidence, &it.RetryCount, &it.RetryAfter, &it.ErrorReason, &it.ContentHash, &it.TraceID, &it.UserTags, &it.UserRationaleText, &it.UserRationaleSource, &it.UserRationaleDurationMS, &it.CaptureMode, &it.SourceApp, &it.SubmittedByDeviceID, &it.SubmittedByUserID); err != nil {
			return nil, err
		}
		if score != 0 {
			it.Score = &score
		}
		it.IsScreenshot = isScreenshotInt != 0
		it.IsShorts = isShortsInt != 0
		it.SkipReason = classifySkipReason(score, it.Verdict)
		items = append(items, it)
	}
	return items, rows.Err()
}

// --- EPIC-016: Firehose subscription management ---

// AddFirehoseSubscription adds a keyword subscription for a profile.
// Idempotent: duplicate (profile, keyword) pairs are silently ignored.
func (q *Queue) AddFirehoseSubscription(profile, keyword string) error {
	_, err := q.db.Exec(
		"INSERT OR IGNORE INTO firehose_subscriptions (profile, keyword) VALUES (?,?)",
		profile, strings.ToLower(keyword),
	)
	return err
}

// RemoveFirehoseSubscription removes a keyword subscription for a profile.
func (q *Queue) RemoveFirehoseSubscription(profile, keyword string) error {
	_, err := q.db.Exec(
		"DELETE FROM firehose_subscriptions WHERE profile=? AND keyword=?",
		profile, strings.ToLower(keyword),
	)
	return err
}

// ListFirehoseSubscriptions returns all keywords subscribed for a profile.
func (q *Queue) ListFirehoseSubscriptions(profile string) ([]string, error) {
	rows, err := q.db.Query("SELECT keyword FROM firehose_subscriptions WHERE profile=?", profile)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var keywords []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		keywords = append(keywords, k)
	}
	return keywords, rows.Err()
}

// PersistFirehoseSeq records the latest firehose sequence number.
// eventCBOR may be nil  -  the seq is the only required value for cursor resume.
func (q *Queue) PersistFirehoseSeq(seq int64, eventCBOR []byte) error {
	_, err := q.db.Exec(
		"INSERT OR REPLACE INTO firehose_events (seq, event_cbor) VALUES (?,?)",
		seq, eventCBOR,
	)
	return err
}

// LoadLastFirehoseSeq returns the highest persisted firehose sequence number,
// or 0 if no events have been recorded yet.
func (q *Queue) LoadLastFirehoseSeq() (int64, error) {
	var seq int64
	err := q.db.QueryRow("SELECT COALESCE(MAX(seq),0) FROM firehose_events").Scan(&seq)
	return seq, err
}

// EnqueueWithSource inserts a share request with an explicit source tag.
// Used by the firehose worker to mark rows as source='firehose'.
func (q *Queue) EnqueueWithSource(req *ShareRequest, source string) (int64, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	traceID := q.newTraceID()
	res, err := q.db.Exec(
		`INSERT INTO queue (url, text, type, action, profile, status, queued_at, title, mime_type, calling_package, relative_path, file_name, classify_source, is_screenshot, file_size, slug, source, trace_id)
		 VALUES (?, ?, ?, ?, ?, 'pending', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		req.URL, req.Text, req.Type, req.Action, req.Profile, now, req.Title,
		req.MimeType, req.CallingPackage, req.RelativePath, req.Filename, req.ClassifySource,
		boolToInt(req.IsScreenshot), req.FileSize, urlToSlug(req.URL), source, traceID,
	)
	if err != nil {
		return 0, fmt.Errorf("enqueue with source: %w", err)
	}
	id, _ := res.LastInsertId()
	return id, nil
}

// --- EPIC-018 M3: Watch Later dedup methods ---

// CountScoredMonitoredVideosToday returns how many monitored videos scored today
// crossed the worth-watching threshold (score >= 60) vs. below it.
// Joins seen_content (source='yt_monitored') with queue via queue_id.
func (q *Queue) CountScoredMonitoredVideosToday(profile string) (worthWatching int, skipped int, err error) {
	const threshold = 60
	now := time.Now().UTC()
	startOfDay := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).Unix()

	rows, err := q.db.Query(
		`
		SELECT q.score
		FROM seen_content sc
		JOIN queue q ON q.id = sc.queue_id
		WHERE sc.source = 'yt_monitored'
		  AND q.profile = ?
		  AND sc.seen_at >= ?
		  AND q.score IS NOT NULL`,
		profile, startOfDay,
	)
	if err != nil {
		return 0, 0, err
	}
	defer rows.Close()
	for rows.Next() {
		var score int
		if err := rows.Scan(&score); err != nil {
			return 0, 0, err
		}
		if score >= threshold {
			worthWatching++
		} else {
			skipped++
		}
	}
	return worthWatching, skipped, rows.Err()
}

// --- EPIC-019 M5: Subscription digest push ---

// EnqueueSubscriptionDigest writes a push_outbox row with kind='subscription_digest'.
// At-most-once-per-day: returns nil (not an error) when a row already exists today.
// This method is independent of EnqueueDigestIfDue  -  it uses its own kind value
// and its own throttle query so the two paths never interfere with each other.
func (q *Queue) EnqueueSubscriptionDigest(profile, body string, worthWatching, skipped int) error {
	now := time.Now().Unix()
	startOfDay := now - (now % 86400) // floor to midnight UTC

	var last sql.NullInt64
	if err := q.db.QueryRow(
		`SELECT MAX(created_at) FROM push_outbox WHERE kind='subscription_digest' AND profile=?`,
		profile,
	).Scan(&last); err != nil {
		return fmt.Errorf("query last subscription_digest: %w", err)
	}
	if last.Valid && last.Int64 >= startOfDay {
		return nil // already sent today
	}

	_, err := q.db.Exec(
		`INSERT INTO push_outbox (score, slug, verdict, url, kind, profile, gap_summary, content_type, classify_source, status, attempts, next_attempt, created_at, updated_at)
		 VALUES (?, '', ?, '', 'subscription_digest', ?, ?, 'youtube_subscriptions', '', 'pending', 0, ?, ?, ?)`,
		worthWatching, body, profile, body, now, now, now,
	)
	return err
}

// --- EPIC-091: Unified seen_content dedup methods ---

// IsNewContent returns true if (source, itemID) has never been seen.
// Returns (false, nil) if already seen.
// Returns (false, error) on DB failure.
func (q *Queue) IsNewContent(source, itemID string) (bool, error) {
	var count int
	err := q.db.QueryRow(
		`SELECT COUNT(*) FROM seen_content WHERE source=? AND item_id=?`,
		source, itemID,
	).Scan(&count)
	if err != nil {
		slog.Warn("dedup_check_failed", "source", source, "item_id", itemID, "error", err)
		return false, err
	}
	return count == 0, nil
}

// SourceForQueueRow returns the content source tag (e.g. "yt_monitored", "yt_watch_later")
// for the given queue row by looking up the seen_content table's queue_id FK.
// Returns empty string if no mapping exists (e.g. manual shares, firehose items).
func (q *Queue) SourceForQueueRow(queueID int64) string {
	var source string
	err := q.db.QueryRow(`SELECT source FROM seen_content WHERE queue_id = ? LIMIT 1`, queueID).Scan(&source)
	if err != nil {
		return ""
	}
	return source
}

// MarkContentSeen records that (source, itemID) has been processed.
// Idempotent: safe to call multiple times for the same pair.
// queueID is the row ID from q.Enqueue(); pass 0 if enqueue was skipped.
func (q *Queue) MarkContentSeen(source, itemID string, queueID int64) error {
	var queueIDVal interface{}
	if queueID != 0 {
		queueIDVal = queueID
	}
	_, err := q.db.Exec(
		`INSERT OR IGNORE INTO seen_content (source, item_id, seen_at, queue_id) VALUES (?, ?, strftime('%s','now'), ?)`,
		source, itemID, queueIDVal,
	)
	if err != nil {
		slog.Warn("dedup_mark_failed", "source", source, "item_id", itemID, "queue_id", queueID, "error", err)
	}
	return err
}
