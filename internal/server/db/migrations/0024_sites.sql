-- Static sites deployed from GitHub (see internal/server/sites).
CREATE TABLE sites (
    id         TEXT    PRIMARY KEY,
    server_id  TEXT    NOT NULL REFERENCES servers (id) ON DELETE CASCADE,
    data       TEXT    NOT NULL,
    tried      TEXT    NOT NULL DEFAULT '', -- last commit a deploy was started for
    env_enc    BLOB,                        -- the .env, encrypted
    created_at INTEGER NOT NULL
);

CREATE TABLE site_deploys (
    id      INTEGER PRIMARY KEY AUTOINCREMENT,
    site_id TEXT    NOT NULL REFERENCES sites (id) ON DELETE CASCADE,
    ts      INTEGER NOT NULL,
    data    TEXT    NOT NULL
);
CREATE INDEX site_deploys_site ON site_deploys (site_id, id);
