-- Phones running the Fleetwatch Notifications app.
CREATE TABLE mobile_devices (
    id           TEXT    PRIMARY KEY,
    member_id    TEXT    NOT NULL DEFAULT '', -- '' = the owner
    name         TEXT    NOT NULL,
    platform     TEXT    NOT NULL DEFAULT '',
    app_version  TEXT    NOT NULL DEFAULT '',
    token_hash   TEXT    NOT NULL UNIQUE,     -- sha256 of the device token
    alerts       TEXT    NOT NULL DEFAULT 'all', -- all | oncall
    created_at   INTEGER NOT NULL,
    last_seen_at INTEGER NOT NULL DEFAULT 0
);

-- Short single-use codes that pair a phone (10 minutes).
CREATE TABLE mobile_pairing (
    code_hash  TEXT    PRIMARY KEY,
    member_id  TEXT    NOT NULL DEFAULT '',
    expires_at INTEGER NOT NULL
);

-- Alerts waiting for (or already sent to) each phone, kept a week.
CREATE TABLE mobile_messages (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    device_id  TEXT    NOT NULL,
    created_at INTEGER NOT NULL,
    payload    TEXT    NOT NULL
);
CREATE INDEX mobile_messages_device ON mobile_messages (device_id, id);
