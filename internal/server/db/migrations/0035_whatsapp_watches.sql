-- Reminders and watches asked for on WhatsApp (see cmd/server/watches.go): "remind
-- me at 6pm…", "call me in 30 minutes if the API is still down", "tell me when the
-- CPU drops below 50%". A reminder has a due time; a watch checks every minute until
-- its condition holds or it expires.
CREATE TABLE whatsapp_watches (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    person      TEXT    NOT NULL,
    what        TEXT    NOT NULL,
    due_at      INTEGER NOT NULL DEFAULT 0,  -- a reminder's time; 0 = a watch
    cond        TEXT    NOT NULL DEFAULT '', -- JSON condition; '' = none
    how         TEXT    NOT NULL DEFAULT 'text',
    created_at  INTEGER NOT NULL,
    expires_at  INTEGER NOT NULL,
    done_at     INTEGER NOT NULL DEFAULT 0,
    canceled_at INTEGER NOT NULL DEFAULT 0,
    result      TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX whatsapp_watches_active ON whatsapp_watches (done_at, canceled_at);
