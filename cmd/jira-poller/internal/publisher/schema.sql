-- Event outbox: durable queue for async delivery to configured Sink.
CREATE TABLE IF NOT EXISTS pending_events (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    event_id        TEXT NOT NULL UNIQUE,
    payload         TEXT NOT NULL,
    created_at      INTEGER NOT NULL DEFAULT (unixepoch()),
    status          TEXT NOT NULL DEFAULT 'pending',
    attempts        INTEGER NOT NULL DEFAULT 0,
    last_attempt_at INTEGER,
    next_attempt_at INTEGER NOT NULL DEFAULT 0,
    error           TEXT
);

CREATE INDEX IF NOT EXISTS idx_pending_events_ready
    ON pending_events(status, next_attempt_at);
