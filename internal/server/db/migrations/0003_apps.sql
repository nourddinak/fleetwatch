-- Latest agent inventory (JSON proto.Inventory), so app state survives a server restart.
ALTER TABLE servers ADD COLUMN last_inventory TEXT NOT NULL DEFAULT '';

-- Monitored applications. Live state (status, PID, CPU, ports, git) comes from the
-- server's latest inventory, matched on (manager, process_key).
CREATE TABLE apps (
    id           TEXT    PRIMARY KEY,
    server_id    TEXT    NOT NULL REFERENCES servers (id) ON DELETE CASCADE,
    name         TEXT    NOT NULL,
    manager      TEXT    NOT NULL,          -- pm2 | systemd | standalone
    process_key  TEXT    NOT NULL,          -- PM2 name, systemd unit, or path for standalone
    path         TEXT    NOT NULL DEFAULT '',
    domain       TEXT    NOT NULL DEFAULT '',
    notes        TEXT    NOT NULL DEFAULT '',
    source       TEXT    NOT NULL DEFAULT 'manual', -- pm2 | systemd | folder | manual
    flags        TEXT    NOT NULL DEFAULT '{}',
    created_at   INTEGER NOT NULL,
    last_seen_at INTEGER,
    UNIQUE (server_id, manager, process_key)
);
CREATE INDEX apps_server ON apps (server_id);

-- Applications found by agents. Ignored rows are kept so they do not reappear until
-- their identity (fingerprint) changes.
CREATE TABLE discoveries (
    id          TEXT    PRIMARY KEY,
    server_id   TEXT    NOT NULL REFERENCES servers (id) ON DELETE CASCADE,
    fingerprint TEXT    NOT NULL,
    kind        TEXT    NOT NULL,           -- pm2 | systemd | folder
    name        TEXT    NOT NULL,
    process_key TEXT    NOT NULL,
    path        TEXT    NOT NULL DEFAULT '',
    state       TEXT    NOT NULL DEFAULT 'pending', -- pending | ignored | added
    data        TEXT    NOT NULL DEFAULT '{}',      -- snapshot shown by Inspect
    first_seen  INTEGER NOT NULL,
    last_seen   INTEGER NOT NULL,
    UNIQUE (server_id, fingerprint)
);
CREATE INDEX discoveries_state ON discoveries (state);

CREATE TABLE app_metrics_1m (
    app_id TEXT NOT NULL REFERENCES apps (id) ON DELETE CASCADE,
    ts     INTEGER NOT NULL,
    cpu REAL NOT NULL, mem REAL NOT NULL, restarts REAL NOT NULL,
    PRIMARY KEY (app_id, ts)
) WITHOUT ROWID;

CREATE TABLE app_metrics_5m (
    app_id TEXT NOT NULL REFERENCES apps (id) ON DELETE CASCADE,
    ts     INTEGER NOT NULL,
    cpu REAL NOT NULL, mem REAL NOT NULL, restarts REAL NOT NULL,
    PRIMARY KEY (app_id, ts)
) WITHOUT ROWID;

CREATE TABLE app_metrics_1h (
    app_id TEXT NOT NULL REFERENCES apps (id) ON DELETE CASCADE,
    ts     INTEGER NOT NULL,
    cpu REAL NOT NULL, mem REAL NOT NULL, restarts REAL NOT NULL,
    PRIMARY KEY (app_id, ts)
) WITHOUT ROWID;

CREATE INDEX events_app_ts ON events (app_id, ts);
