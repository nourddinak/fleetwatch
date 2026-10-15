CREATE TABLE servers (
    id              TEXT    PRIMARY KEY,
    name            TEXT    NOT NULL,
    provider        TEXT    NOT NULL DEFAULT '',     -- free-text label set by the admin
    notes           TEXT    NOT NULL DEFAULT '',
    notify_override TEXT    NOT NULL DEFAULT 'default',
    -- reported by the agent
    hostname        TEXT    NOT NULL DEFAULT '',
    os              TEXT    NOT NULL DEFAULT '',
    kernel          TEXT    NOT NULL DEFAULT '',
    arch            TEXT    NOT NULL DEFAULT '',
    cpu_cores       INTEGER NOT NULL DEFAULT 0,
    mem_total       INTEGER NOT NULL DEFAULT 0,
    swap_total      INTEGER NOT NULL DEFAULT 0,
    disk_total      INTEGER NOT NULL DEFAULT 0,
    ips             TEXT    NOT NULL DEFAULT '[]',   -- JSON array of interface addresses
    public_ip       TEXT    NOT NULL DEFAULT '',     -- address the agent connected from
    agent_version   TEXT    NOT NULL DEFAULT '',
    capabilities    TEXT    NOT NULL DEFAULT '[]',
    boot_time       INTEGER NOT NULL DEFAULT 0,
    last_metrics    TEXT    NOT NULL DEFAULT '',     -- JSON proto.Metrics, last known state
    created_at      INTEGER NOT NULL,
    enrolled_at     INTEGER,
    last_seen_at    INTEGER,
    connected_since INTEGER
);

-- One-time tokens used by install.sh; only the hash is stored.
CREATE TABLE enrollment_tokens (
    token_hash TEXT    PRIMARY KEY,
    server_id  TEXT    NOT NULL REFERENCES servers (id) ON DELETE CASCADE,
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL,
    used_at    INTEGER
);
CREATE INDEX enrollment_tokens_server ON enrollment_tokens (server_id);

-- Permanent agent credentials ("fwa.<id>.<secret>"); only the secret's hash is stored.
CREATE TABLE agent_credentials (
    id           TEXT    PRIMARY KEY,
    server_id    TEXT    NOT NULL REFERENCES servers (id) ON DELETE CASCADE,
    secret_hash  TEXT    NOT NULL,
    created_at   INTEGER NOT NULL,
    revoked_at   INTEGER,
    last_used_at INTEGER
);
CREATE INDEX agent_credentials_server ON agent_credentials (server_id);

-- Timeline of notable server/app events (agent connected, rebooted, ...).
CREATE TABLE events (
    id        INTEGER PRIMARY KEY,
    ts        INTEGER NOT NULL,
    server_id TEXT    REFERENCES servers (id) ON DELETE CASCADE,
    app_id    TEXT,
    kind      TEXT    NOT NULL,
    text      TEXT    NOT NULL
);
CREATE INDEX events_server_ts ON events (server_id, ts);
CREATE INDEX events_ts ON events (ts);

-- Host metrics at three resolutions. mem/swap/disk are percentages, net is bytes/s.
CREATE TABLE server_metrics_1m (
    server_id TEXT    NOT NULL REFERENCES servers (id) ON DELETE CASCADE,
    ts        INTEGER NOT NULL,
    cpu REAL NOT NULL, mem REAL NOT NULL, swap REAL NOT NULL, disk REAL NOT NULL,
    load1 REAL NOT NULL, net_rx REAL NOT NULL, net_tx REAL NOT NULL,
    PRIMARY KEY (server_id, ts)
) WITHOUT ROWID;

CREATE TABLE server_metrics_5m (
    server_id TEXT    NOT NULL REFERENCES servers (id) ON DELETE CASCADE,
    ts        INTEGER NOT NULL,
    cpu REAL NOT NULL, mem REAL NOT NULL, swap REAL NOT NULL, disk REAL NOT NULL,
    load1 REAL NOT NULL, net_rx REAL NOT NULL, net_tx REAL NOT NULL,
    PRIMARY KEY (server_id, ts)
) WITHOUT ROWID;

CREATE TABLE server_metrics_1h (
    server_id TEXT    NOT NULL REFERENCES servers (id) ON DELETE CASCADE,
    ts        INTEGER NOT NULL,
    cpu REAL NOT NULL, mem REAL NOT NULL, swap REAL NOT NULL, disk REAL NOT NULL,
    load1 REAL NOT NULL, net_rx REAL NOT NULL, net_tx REAL NOT NULL,
    PRIMARY KEY (server_id, ts)
) WITHOUT ROWID;
