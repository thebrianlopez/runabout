package cache

import (
	"bytes"
	"compress/gzip"
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"filippo.io/age"
	_ "modernc.org/sqlite"
)

// Store is a SQLite-backed cache with TTL and gzip compression.
type Store struct {
	db       *sql.DB
	identity *age.X25519Identity // nil = encryption disabled
}

// CacheStats holds aggregate statistics about the cache.
type CacheStats struct {
	TotalEntries int
	TotalBytes   int64
	BySource     map[string]SourceStats
}

// SourceStats holds per-source cache statistics.
type SourceStats struct {
	Entries int
	Bytes   int64
}

const schema = `
CREATE TABLE IF NOT EXISTS cache (
	key        TEXT PRIMARY KEY,
	source     TEXT    NOT NULL,
	value      BLOB    NOT NULL,
	created_at INTEGER NOT NULL,
	expires_at INTEGER NOT NULL,
	size_bytes INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_cache_source ON cache(source);
CREATE INDEX IF NOT EXISTS idx_cache_expires ON cache(expires_at);
`

// Open creates or opens a SQLite cache database at dbPath.
// Returns nil (not an error) if the database cannot be opened, enabling graceful degradation.
func Open(dbPath string) *Store {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0700); err != nil {
		return nil
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil
	}

	// WAL mode for concurrent reads + busy timeout
	for _, pragma := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA busy_timeout=5000",
	} {
		if _, err := db.Exec(pragma); err != nil {
			db.Close()
			return nil
		}
	}

	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil
	}

	return &Store{db: db}
}

// OpenWithPassphrase opens the cache and enables age encryption using the given
// passphrase. The X25519 identity is loaded from (or created in) configDir.
// Returns nil on any failure, enabling graceful degradation.
func OpenWithPassphrase(dbPath, configDir, passphrase string) *Store {
	s := Open(dbPath)
	if s == nil {
		return nil
	}
	identity, err := loadOrCreateIdentity(configDir, passphrase)
	if err != nil {
		s.db.Close()
		return nil
	}
	s.identity = identity
	return s
}

// Close closes the underlying database connection.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// Get retrieves a non-expired entry by key. Returns nil if not found or expired.
// Encrypted entries are transparently decrypted when an identity is available;
// if the entry is encrypted but no identity is set, nil is returned (cache miss).
func (s *Store) Get(key string) ([]byte, error) {
	var blob []byte
	err := s.db.QueryRow(
		"SELECT value FROM cache WHERE key = ? AND expires_at > ?",
		key, time.Now().Unix(),
	).Scan(&blob)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("cache get: %w", err)
	}
	compressed := blob
	if isAgeEncrypted(blob) {
		if s.identity == nil {
			// Encrypted entry without passphrase — treat as miss.
			return nil, nil
		}
		compressed, err = decryptBlob(s.identity, blob)
		if err != nil {
			return nil, fmt.Errorf("cache decrypt: %w", err)
		}
	}
	return decompress(compressed)
}

// Put inserts or replaces a cache entry with gzip compression and optional age encryption.
func (s *Store) Put(key, source string, data []byte, ttl time.Duration) error {
	compressed, err := compress(data)
	if err != nil {
		return fmt.Errorf("cache compress: %w", err)
	}

	blob := compressed
	if s.identity != nil {
		blob, err = encryptBlob(s.identity, compressed)
		if err != nil {
			return fmt.Errorf("cache encrypt: %w", err)
		}
	}

	now := time.Now().Unix()
	expiresAt := time.Now().Add(ttl).Unix()

	_, err = s.db.Exec(
		`INSERT OR REPLACE INTO cache (key, source, value, created_at, expires_at, size_bytes)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		key, source, blob, now, expiresAt, len(data),
	)
	if err != nil {
		return fmt.Errorf("cache put: %w", err)
	}
	return nil
}

// HasValid reports whether a non-expired entry exists for key, without
// decompressing the value. This is cheaper than Get when you only need to
// check existence (e.g. incremental cache warming).
func (s *Store) HasValid(key string) bool {
	var count int
	err := s.db.QueryRow(
		"SELECT COUNT(*) FROM cache WHERE key = ? AND expires_at > ?",
		key, time.Now().Unix(),
	).Scan(&count)
	return err == nil && count > 0
}

// Delete removes a single cache entry by key.
func (s *Store) Delete(key string) error {
	_, err := s.db.Exec("DELETE FROM cache WHERE key = ?", key)
	if err != nil {
		return fmt.Errorf("cache delete: %w", err)
	}
	return nil
}

// Clear removes entries matching optional filters.
// If source is non-empty, only entries for that source are removed.
// If olderThan > 0, only entries created before (now - olderThan) are removed.
// If both are zero-valued, all entries are removed.
func (s *Store) Clear(source string, olderThan time.Duration) error {
	query := "DELETE FROM cache WHERE 1=1"
	var args []interface{}

	if source != "" {
		query += " AND source = ?"
		args = append(args, source)
	}
	if olderThan > 0 {
		cutoff := time.Now().Add(-olderThan).Unix()
		query += " AND created_at < ?"
		args = append(args, cutoff)
	}

	_, err := s.db.Exec(query, args...)
	if err != nil {
		return fmt.Errorf("cache clear: %w", err)
	}
	return nil
}

// Prune removes all expired entries.
func (s *Store) Prune() error {
	_, err := s.db.Exec("DELETE FROM cache WHERE expires_at <= ?", time.Now().Unix())
	if err != nil {
		return fmt.Errorf("cache prune: %w", err)
	}
	return nil
}

// GetStats returns aggregate cache statistics.
func (s *Store) GetStats() (*CacheStats, error) {
	stats := &CacheStats{
		BySource: make(map[string]SourceStats),
	}

	rows, err := s.db.Query("SELECT source, COUNT(*), COALESCE(SUM(size_bytes), 0) FROM cache GROUP BY source")
	if err != nil {
		return nil, fmt.Errorf("cache stats: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var source string
		var count int
		var bytes int64
		if err := rows.Scan(&source, &count, &bytes); err != nil {
			return nil, fmt.Errorf("cache stats scan: %w", err)
		}
		stats.BySource[source] = SourceStats{Entries: count, Bytes: bytes}
		stats.TotalEntries += count
		stats.TotalBytes += bytes
	}
	return stats, rows.Err()
}

// compress gzip-compresses data.
func compress(data []byte) ([]byte, error) {
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	if _, err := w.Write(data); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// maxDecompressBytes is the upper bound on decompressed cache entry size (128 MiB).
// Prevents unbounded memory growth from corrupt or maliciously crafted gzip payloads.
const maxDecompressBytes int64 = 128 << 20

// decompress gzip-decompresses data.
func decompress(data []byte) ([]byte, error) {
	r, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer r.Close()
	// Read up to limit+1 bytes; if we get more than limit, the entry is invalid.
	out, err := io.ReadAll(io.LimitReader(r, maxDecompressBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(out)) > maxDecompressBytes {
		return nil, fmt.Errorf("cache entry exceeds maximum decompressed size (%d MiB)", maxDecompressBytes>>20)
	}
	return out, nil
}
