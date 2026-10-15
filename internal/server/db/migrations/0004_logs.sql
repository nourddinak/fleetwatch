-- Application log lines. app_id is NULL for agent notices about the server itself.
CREATE TABLE logs (
    id        INTEGER PRIMARY KEY,
    ts        INTEGER NOT NULL,                      -- unix ms
    server_id TEXT    NOT NULL REFERENCES servers (id) ON DELETE CASCADE,
    app_id    TEXT             REFERENCES apps (id) ON DELETE CASCADE,
    source    TEXT    NOT NULL,                      -- pm2 | systemd | agent
    stream    TEXT    NOT NULL,                      -- stdout | stderr | journal
    severity  TEXT    NOT NULL,                      -- debug | info | warning | error | critical | unknown
    message   TEXT    NOT NULL
);
-- Pages are fetched newest-first by id within an app or server.
CREATE INDEX logs_app_id    ON logs (app_id, id);
CREATE INDEX logs_server_id ON logs (server_id, id);
CREATE INDEX logs_ts        ON logs (ts);

-- Full-text index over messages, kept in sync by triggers.
CREATE VIRTUAL TABLE logs_fts USING fts5 (message, content = 'logs', content_rowid = 'id', tokenize = 'unicode61');
CREATE TRIGGER logs_ai AFTER INSERT ON logs BEGIN
    INSERT INTO logs_fts (rowid, message) VALUES (new.id, new.message);
END;
CREATE TRIGGER logs_ad AFTER DELETE ON logs BEGIN
    INSERT INTO logs_fts (logs_fts, rowid, message) VALUES ('delete', old.id, old.message);
END;
