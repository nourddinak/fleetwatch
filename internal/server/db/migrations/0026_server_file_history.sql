-- Every version of a server's Caddyfile saved from the dashboard (see internal/server/envfile).
CREATE TABLE server_file_history (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    server_id   TEXT    NOT NULL REFERENCES servers (id) ON DELETE CASCADE,
    name        TEXT    NOT NULL,
    by          TEXT    NOT NULL,
    ts          INTEGER NOT NULL,
    content_enc BLOB    NOT NULL
);
CREATE INDEX server_file_history_file ON server_file_history (server_id, name, id);
