-- One-row memory of the last release that failed its health check after an
-- unattended update. Without it the updater reinstalls and rolls back the same
-- bad tag on every hourly tick. The single row is seeded here so reads always
-- find it; RecordBadTag updates it in place. Tags only move forward, so the most
-- recent bad tag is all that need be remembered.
CREATE TABLE update_state (
    id          INTEGER PRIMARY KEY CHECK (id = 1),
    bad_tag     TEXT    NOT NULL DEFAULT '',
    recorded_at TEXT    NOT NULL DEFAULT (datetime('now'))
);

INSERT INTO update_state (id, bad_tag) VALUES (1, '');
