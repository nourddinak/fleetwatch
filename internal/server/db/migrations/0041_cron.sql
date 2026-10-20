-- The cron jobs and timers each server's agent found, as last reported, and when each
-- job was first seen (a job is never called missed before it could have run).
CREATE TABLE cron_reports (
    server_id  TEXT    PRIMARY KEY REFERENCES servers (id) ON DELETE CASCADE,
    data       TEXT    NOT NULL,
    first_seen TEXT    NOT NULL DEFAULT '{}',
    at         INTEGER NOT NULL
);
