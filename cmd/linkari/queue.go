package main

import (
	"database/sql"
	"fmt"
	"log"
	"time"

	_ "modernc.org/sqlite"
)

const maxQueueSize = 200

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
		log.Printf("WARN: fts5 backfill: %v", err)
	}

	if err := q.Prune(); err != nil {
		log.Printf("WARN: queue prune on startup: %v", err)
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
	if q.debug {
		log.Printf("[DEBUG] queue: enqueued id=%d type=%s", id, req.Type)
	}
	return id, nil
}

const queueCols = "id, url, text, type, action, profile, status, COALESCE(score,0), COALESCE(tags,''), queued_at, COALESCE(relayed_at,''), COALESCE(scored_at,''), COALESCE(archived_at,''), COALESCE(verdict,''), COALESCE(slug,'')"

// Pending returns all items with status=pending, ordered by id ASC (FIFO).
func (q *Queue) Pending() ([]QueueItem, error) {
	return q.query("SELECT "+queueCols+" FROM queue WHERE status='pending' ORDER BY id ASC")
}

// List returns items filtered by status (empty string = all), limited to n rows.
func (q *Queue) List(status string, limit int) ([]QueueItem, error) {
	if limit <= 0 {
		limit = 50
	}
	if status != "" {
		return q.query("SELECT "+queueCols+" FROM queue WHERE status=? ORDER BY id DESC LIMIT ?", status, limit)
	}
	return q.query("SELECT "+queueCols+" FROM queue ORDER BY id DESC LIMIT ?", limit)
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
func (q *Queue) ListArchived(profile string, limit int) ([]QueueItem, error) {
	if limit <= 0 {
		limit = 50
	}
	if profile != "" {
		return q.query("SELECT "+queueCols+" FROM queue WHERE status='archived' AND profile=? ORDER BY score DESC LIMIT ?", profile, limit)
	}
	return q.query("SELECT "+queueCols+" FROM queue WHERE status='archived' ORDER BY score DESC LIMIT ?", limit)
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

func (q *Queue) query(sqlStr string, args ...any) ([]QueueItem, error) {
	rows, err := q.db.Query(sqlStr, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []QueueItem
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
