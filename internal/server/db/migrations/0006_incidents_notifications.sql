CREATE TABLE incidents (
    id          TEXT    PRIMARY KEY,
    type        TEXT    NOT NULL,          -- server_offline, app_offline, crash_loop, health_failed, ...
    key         TEXT    NOT NULL,          -- grouping key: one open incident per key
    severity    TEXT    NOT NULL,          -- info | warning | critical
    status      TEXT    NOT NULL,          -- active | acknowledged | muted | resolved
    resolved_by TEXT    NOT NULL DEFAULT '', -- auto | admin
    title       TEXT    NOT NULL,
    server_id   TEXT    REFERENCES servers (id) ON DELETE CASCADE,
    app_id      TEXT    REFERENCES apps (id) ON DELETE CASCADE,
    occurrences INTEGER NOT NULL DEFAULT 1,
    first_at    INTEGER NOT NULL,
    last_at     INTEGER NOT NULL,
    resolved_at INTEGER,
    muted_until INTEGER,
    resolution  TEXT    NOT NULL DEFAULT '',
    samples     TEXT    NOT NULL DEFAULT '[]', -- recent related log lines / details
    timeline    TEXT    NOT NULL DEFAULT '[]'  -- [{"ts":..,"text":..}]
);
CREATE UNIQUE INDEX incidents_open_key ON incidents (key) WHERE status <> 'resolved';
CREATE INDEX incidents_last ON incidents (last_at);
CREATE INDEX incidents_app ON incidents (app_id);
CREATE INDEX incidents_server ON incidents (server_id);

-- One row per provider (ntfy, telegram, discord); config is AES-GCM encrypted JSON.
CREATE TABLE notification_channels (
    provider   TEXT    PRIMARY KEY,
    enabled    INTEGER NOT NULL DEFAULT 0,
    config_enc BLOB,
    updated_at INTEGER NOT NULL
);

CREATE TABLE notification_history (
    id          INTEGER PRIMARY KEY,
    ts          INTEGER NOT NULL,
    event       TEXT    NOT NULL,
    provider    TEXT    NOT NULL,
    server_id   TEXT,
    app_id      TEXT,
    incident_id TEXT,
    title       TEXT    NOT NULL,
    message     TEXT    NOT NULL,
    result      TEXT    NOT NULL,          -- delivered | failed
    error       TEXT    NOT NULL DEFAULT '',
    latency_ms  INTEGER NOT NULL DEFAULT 0,
    read        INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX notification_history_ts ON notification_history (ts);
