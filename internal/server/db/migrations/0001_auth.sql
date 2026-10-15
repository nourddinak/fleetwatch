-- All timestamps are unix milliseconds.

-- Single administrator; id is always 1.
CREATE TABLE admin (
    id                  INTEGER PRIMARY KEY CHECK (id = 1),
    username            TEXT    NOT NULL,
    display_name        TEXT    NOT NULL DEFAULT '',
    email               TEXT    NOT NULL DEFAULT '',
    password_hash       TEXT    NOT NULL,          -- argon2id PHC string
    password_changed_at INTEGER NOT NULL,
    totp_secret_enc     BLOB,                      -- AES-GCM; NULL = 2FA disabled
    totp_pending_enc    BLOB,                      -- secret during setup, before verification
    totp_last_step      INTEGER NOT NULL DEFAULT 0 -- last accepted TOTP step, blocks replay
);

CREATE TABLE recovery_codes (
    code_hash TEXT PRIMARY KEY,                    -- sha256 hex of the normalized code
    used_at   INTEGER
);

CREATE TABLE sessions (
    id           TEXT    PRIMARY KEY,              -- public id, safe to show in the UI
    token_hash   TEXT    NOT NULL UNIQUE,          -- sha256 hex of the cookie value
    mfa_pending  INTEGER NOT NULL DEFAULT 0,       -- 1 = password ok, waiting for TOTP
    created_at   INTEGER NOT NULL,
    last_seen_at INTEGER NOT NULL,
    expires_at   INTEGER NOT NULL,                 -- absolute expiry
    ip           TEXT    NOT NULL DEFAULT '',
    user_agent   TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX sessions_expires ON sessions (expires_at);

-- JSON documents keyed by settings section ("security", "logs", ...).
CREATE TABLE settings (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

CREATE TABLE audit_events (
    id         INTEGER PRIMARY KEY,
    ts         INTEGER NOT NULL,
    action     TEXT    NOT NULL,                   -- e.g. login, 2fa.enable, session.revoke
    ok         INTEGER NOT NULL DEFAULT 1,
    ip         TEXT    NOT NULL DEFAULT '',
    user_agent TEXT    NOT NULL DEFAULT '',
    target     TEXT    NOT NULL DEFAULT '',
    detail     TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX audit_events_ts ON audit_events (ts);
CREATE INDEX audit_events_action_ts ON audit_events (action, ts);
