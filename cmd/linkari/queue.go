package main

import (
	"database/sql"
	"fmt"
	"log/slog"
	"math"
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
}

// Queue persists share requests in SQLite for deferred replay.
type Queue struct {
	db    *sql.DB
	debug bool
}

// NewQueue opens (or creates) the SQLite database at dbPath.
func NewQueue(dbPath string, debug bool) (*Queue, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open queue db: %w", err)
	}

	// WAL mode for concurrent reads during replay.
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("set WAL mode: %w", err)
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

const queueCols = "id, url, text, type, action, profile, status, COALESCE(score,0), COALESCE(tags,''), queued_at, COALESCE(relayed_at,''), COALESCE(scored_at,''), COALESCE(archived_at,''), COALESCE(verdict,''), COALESCE(slug,'')"

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
	Status      string
	Attempts    int
	NextAttempt int64
	CreatedAt   int64
	UpdatedAt   int64
	LastError   string
}

// EnqueuePush inserts a pending row into push_outbox and returns its id.
func (q *Queue) EnqueuePush(kind string, score int, slug, verdict, url string) (int64, error) {
	now := time.Now().Unix()
	res, err := q.db.Exec(
		`INSERT INTO push_outbox (score, slug, verdict, url, kind, status, attempts, next_attempt, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, 'pending', 0, ?, ?, ?)`,
		score, slug, verdict, url, kind, now, now, now,
	)
	if err != nil {
		return 0, fmt.Errorf("enqueue push: %w", err)
	}
	id, _ := res.LastInsertId()
	return id, nil
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
		`SELECT id, score, slug, verdict, url, kind, status, attempts, next_attempt, created_at, updated_at, last_error
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
		if err := rows.Scan(&p.ID, &p.Score, &p.Slug, &p.Verdict, &p.URL, &p.Kind, &p.Status, &p.Attempts, &p.NextAttempt, &p.CreatedAt, &p.UpdatedAt, &p.LastError); err != nil {
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
		if err := rows.Scan(&it.ID, &it.URL, &it.Text, &it.Type, &it.Action, &it.Profile, &it.Status, &score, &it.Tags, &it.QueuedAt, &it.RelayedAt, &it.ScoredAt, &it.ArchivedAt, &it.Verdict, &it.Slug); err != nil {
			return nil, err
		}
		if score != 0 {
			it.Score = &score
		}
		items = append(items, it)
	}
	return items, rows.Err()
}
