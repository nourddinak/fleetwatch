-- Health check configuration per app (JSON: enabled, url, intervalSec, timeoutMs, expectedStatus).
ALTER TABLE apps ADD COLUMN health TEXT NOT NULL DEFAULT '{}';

-- Admin-defined commands. Built-in start/stop/restart/reload are not stored.
CREATE TABLE app_commands (
    id          TEXT    PRIMARY KEY,
    app_id      TEXT    NOT NULL REFERENCES apps (id) ON DELETE CASCADE,
    name        TEXT    NOT NULL,
    command     TEXT    NOT NULL,
    cwd         TEXT    NOT NULL DEFAULT '', -- relative to the app directory
    timeout_sec INTEGER NOT NULL DEFAULT 300,
    confirm     INTEGER NOT NULL DEFAULT 1,
    env         TEXT    NOT NULL DEFAULT '[]', -- JSON ["KEY=value", ...]
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL
);
CREATE INDEX app_commands_app ON app_commands (app_id);

CREATE TABLE command_runs (
    id           TEXT    PRIMARY KEY,
    app_id       TEXT    NOT NULL REFERENCES apps (id) ON DELETE CASCADE,
    command_id   TEXT    NOT NULL,          -- builtin:<action> or app_commands.id
    name         TEXT    NOT NULL,
    preview      TEXT    NOT NULL DEFAULT '',
    cwd          TEXT    NOT NULL DEFAULT '',
    run_as       TEXT    NOT NULL DEFAULT '',
    started_at   INTEGER NOT NULL,
    finished_at  INTEGER,
    status       TEXT    NOT NULL,          -- running | succeeded | failed | timeout | refused | error
    exit_code    INTEGER,
    stdout       TEXT    NOT NULL DEFAULT '',
    stderr       TEXT    NOT NULL DEFAULT '',
    error        TEXT    NOT NULL DEFAULT '',
    initiated_by TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX command_runs_app ON command_runs (app_id, started_at);

CREATE TABLE health_results (
    id         INTEGER PRIMARY KEY,
    app_id     TEXT    NOT NULL REFERENCES apps (id) ON DELETE CASCADE,
    ts         INTEGER NOT NULL,
    ok         INTEGER NOT NULL,
    status     INTEGER NOT NULL,
    latency_ms INTEGER NOT NULL,
    error      TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX health_results_app ON health_results (app_id, ts);
CREATE INDEX health_results_ts ON health_results (ts);

-- Latest folder-size scan per app.
CREATE TABLE app_storage (
    app_id     TEXT    PRIMARY KEY REFERENCES apps (id) ON DELETE CASCADE,
    scanned_at INTEGER NOT NULL,
    data       TEXT    NOT NULL           -- JSON proto.StorageResult
);
