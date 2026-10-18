-- Every version of an app's .env files saved from the dashboard (see internal/server/envfile).
CREATE TABLE env_history (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    app_id      TEXT    NOT NULL REFERENCES apps (id) ON DELETE CASCADE,
    name        TEXT    NOT NULL,          -- .env, .env.production…
    by          TEXT    NOT NULL,          -- who saved it ("on the server": changed outside Fleetwatch)
    ts          INTEGER NOT NULL,
    content_enc BLOB    NOT NULL           -- the file, encrypted
);
CREATE INDEX env_history_file ON env_history (app_id, name, id);
