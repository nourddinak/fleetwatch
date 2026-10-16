-- GPU utilization and memory (percent) next to the other host metrics.
ALTER TABLE server_metrics_1m ADD COLUMN gpu REAL NOT NULL DEFAULT 0;
ALTER TABLE server_metrics_1m ADD COLUMN gpu_mem REAL NOT NULL DEFAULT 0;
ALTER TABLE server_metrics_5m ADD COLUMN gpu REAL NOT NULL DEFAULT 0;
ALTER TABLE server_metrics_5m ADD COLUMN gpu_mem REAL NOT NULL DEFAULT 0;
ALTER TABLE server_metrics_1h ADD COLUMN gpu REAL NOT NULL DEFAULT 0;
ALTER TABLE server_metrics_1h ADD COLUMN gpu_mem REAL NOT NULL DEFAULT 0;

-- Uptime checks: a URL checked from the dashboard and/or from other servers' agents.
CREATE TABLE uptime_checks (
    id         TEXT    PRIMARY KEY,
    data       TEXT    NOT NULL, -- JSON, see internal/server/uptime
    created_at INTEGER NOT NULL
);

CREATE TABLE uptime_results (
    check_id   TEXT    NOT NULL REFERENCES uptime_checks (id) ON DELETE CASCADE,
    location   TEXT    NOT NULL, -- "dashboard" or a server ID
    ts         INTEGER NOT NULL,
    ok         INTEGER NOT NULL,
    status     INTEGER NOT NULL,
    latency_ms INTEGER NOT NULL,
    error      TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX uptime_results_check_ts ON uptime_results (check_id, ts);
