package main

import (
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite"
)

// ReadOnlyDB wraps a read-only SQLite connection to queue.db.
type ReadOnlyDB struct {
	db *sql.DB
}

// OpenReadOnlyDB opens queue.db in read-only WAL mode.
func OpenReadOnlyDB(path string) (*ReadOnlyDB, error) {
	dsn := fmt.Sprintf("file:%s?mode=ro&_journal=WAL", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open read-only db: %w", err)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping read-only db: %w", err)
	}
	return &ReadOnlyDB{db: db}, nil
}

// LookupScoreForURI returns the score for a known AT URI, or -1 if not found.
func (r *ReadOnlyDB) LookupScoreForURI(atURI string) int {
	var score int
	err := r.db.QueryRow(
		`SELECT score FROM queue WHERE url=? AND status IN ('scored','archived') ORDER BY id DESC LIMIT 1`,
		atURI,
	).Scan(&score)
	if err != nil {
		return -1
	}
	return score
}

func (r *ReadOnlyDB) Close() error {
	return r.db.Close()
}
