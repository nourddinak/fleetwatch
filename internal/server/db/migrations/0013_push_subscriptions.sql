-- Browsers and installed apps that receive push notifications.
CREATE TABLE push_subscriptions (
    id          TEXT    PRIMARY KEY,
    endpoint    TEXT    NOT NULL UNIQUE,
    p256dh      TEXT    NOT NULL,
    auth        TEXT    NOT NULL,
    device      TEXT    NOT NULL DEFAULT '', -- user agent, to tell devices apart
    created_at  INTEGER NOT NULL,
    last_ok_at  INTEGER NOT NULL DEFAULT 0,
    last_error  TEXT    NOT NULL DEFAULT ''
);
