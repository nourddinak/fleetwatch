-- Discussion on incidents.
CREATE TABLE incident_comments (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    incident_id TEXT    NOT NULL REFERENCES incidents (id) ON DELETE CASCADE,
    ts          INTEGER NOT NULL,
    author      TEXT    NOT NULL,
    text        TEXT    NOT NULL
);
CREATE INDEX incident_comments_incident ON incident_comments (incident_id, ts);

-- The written parts of incident postmortems (the timeline is built when asked).
CREATE TABLE postmortems (
    incident_id TEXT    PRIMARY KEY REFERENCES incidents (id) ON DELETE CASCADE,
    data        TEXT    NOT NULL,
    updated_at  INTEGER NOT NULL
);

-- Who did it: a team member's username or the owner's name ('' for older rows and
-- sign-in events, whose detail names the account).
ALTER TABLE audit_events ADD COLUMN actor TEXT NOT NULL DEFAULT '';
CREATE INDEX audit_events_ts_ok ON audit_events (ok, ts);

-- Every on-call page sent, for the on-call report.
CREATE TABLE oncall_pages (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    incident_id TEXT    NOT NULL,
    person      TEXT    NOT NULL, -- oncall person ID ("owner" or a member ID)
    level       INTEGER NOT NULL, -- 0 = first in line, 1+ = escalated
    ts          INTEGER NOT NULL,
    ok          INTEGER NOT NULL
);
CREATE INDEX oncall_pages_ts ON oncall_pages (ts);

-- Notes left for the next on-call shift.
CREATE TABLE oncall_handoffs (
    shift_from INTEGER PRIMARY KEY, -- unix ms start of the shift the note was written in
    person     TEXT    NOT NULL,
    note       TEXT    NOT NULL,
    written_by TEXT    NOT NULL,
    updated_at INTEGER NOT NULL
);
