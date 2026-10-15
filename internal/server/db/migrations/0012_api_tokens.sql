-- Read-only API tokens for scripts and Prometheus. Only a hash of each is stored.
CREATE TABLE api_tokens (
    id           TEXT    PRIMARY KEY,
    name         TEXT    NOT NULL,
    token_hash   TEXT    NOT NULL UNIQUE,
    prefix       TEXT    NOT NULL, -- first characters, to recognize a token
    created_at   INTEGER NOT NULL,
    last_used_at INTEGER NOT NULL DEFAULT 0,
    last_used_ip TEXT    NOT NULL DEFAULT ''
);
