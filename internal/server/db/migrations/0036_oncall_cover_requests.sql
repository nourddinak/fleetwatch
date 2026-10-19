-- "Can someone cover me Saturday?": asked on WhatsApp or the On-call page; the first
-- teammate to say yes gets an override (see internal/server/oncall/cover.go).
CREATE TABLE oncall_cover_requests (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    person      TEXT    NOT NULL,
    from_ms     INTEGER NOT NULL,
    to_ms       INTEGER NOT NULL,
    note        TEXT    NOT NULL DEFAULT '',
    created_at  INTEGER NOT NULL,
    taken_by    TEXT    NOT NULL DEFAULT '',
    taken_at    INTEGER NOT NULL DEFAULT 0,
    canceled_at INTEGER NOT NULL DEFAULT 0
);
