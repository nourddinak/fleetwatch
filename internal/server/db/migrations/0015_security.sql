-- Each server's latest security findings from its agent.
CREATE TABLE security_reports (
    server_id TEXT    PRIMARY KEY,
    at        INTEGER NOT NULL,
    findings  TEXT    NOT NULL -- JSON list
);

-- Successful SSH logins, kept 90 days.
CREATE TABLE ssh_logins (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    server_id   TEXT    NOT NULL,
    ts          INTEGER NOT NULL,
    user        TEXT    NOT NULL,
    ip          TEXT    NOT NULL,
    method      TEXT    NOT NULL,
    key_fp      TEXT    NOT NULL DEFAULT '',
    country     TEXT    NOT NULL DEFAULT '',
    new_country INTEGER NOT NULL DEFAULT 0,
    new_key     INTEGER NOT NULL DEFAULT 0,
    UNIQUE (server_id, ts, user, ip)
);
CREATE INDEX ssh_logins_server_ts ON ssh_logins (server_id, ts DESC);

-- Where IP addresses are registered (looked up at the regional registries).
CREATE TABLE ip_countries (
    ip        TEXT    PRIMARY KEY,
    country   TEXT    NOT NULL,
    looked_at INTEGER NOT NULL
);
