-- Backup runs (restic) and the copies each server's repository holds.
CREATE TABLE backup_runs (
    id        TEXT    PRIMARY KEY,
    server_id TEXT    NOT NULL REFERENCES servers (id) ON DELETE CASCADE,
    started   INTEGER NOT NULL,
    finished  INTEGER NOT NULL DEFAULT 0,
    status    TEXT    NOT NULL, -- running | ok | partial | failed
    data      TEXT    NOT NULL DEFAULT '{}'
);
CREATE INDEX backup_runs_server ON backup_runs (server_id, started);

CREATE TABLE backup_snapshots (
    server_id TEXT    PRIMARY KEY REFERENCES servers (id) ON DELETE CASCADE,
    data      TEXT    NOT NULL,
    at        INTEGER NOT NULL
);
