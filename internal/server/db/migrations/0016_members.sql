-- Team members besides the owner (the admin table): operators and viewers.
CREATE TABLE members (
    id            TEXT    PRIMARY KEY,
    username      TEXT    NOT NULL UNIQUE COLLATE NOCASE,
    display_name  TEXT    NOT NULL DEFAULT '',
    email         TEXT    NOT NULL DEFAULT '',
    password_hash TEXT    NOT NULL,
    role          TEXT    NOT NULL,             -- operator | viewer
    servers       TEXT    NOT NULL DEFAULT '*', -- "*" = every server, else a JSON list of ids
    disabled      INTEGER NOT NULL DEFAULT 0,
    created_at    INTEGER NOT NULL,
    last_login_at INTEGER NOT NULL DEFAULT 0
);

-- '' = the owner's session.
ALTER TABLE sessions ADD COLUMN member_id TEXT NOT NULL DEFAULT '';
