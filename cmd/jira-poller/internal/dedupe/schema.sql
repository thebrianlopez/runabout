-- Idempotency store: one row per processed event_id, with a TTL.
CREATE TABLE IF NOT EXISTS seen_events (
    event_id   TEXT PRIMARY KEY,
    seen_at    INTEGER NOT NULL DEFAULT (unixepoch()),
    expires_at INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_seen_events_expires
    ON seen_events(expires_at);
