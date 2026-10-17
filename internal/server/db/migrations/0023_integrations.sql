-- Connected services (GitHub first). public is JSON shown in the dashboard; the keys
-- are AES-GCM encrypted JSON.
CREATE TABLE integrations (
    provider   TEXT    PRIMARY KEY,
    public     TEXT    NOT NULL DEFAULT '{}',
    secret_enc BLOB,
    created_at INTEGER NOT NULL
);
