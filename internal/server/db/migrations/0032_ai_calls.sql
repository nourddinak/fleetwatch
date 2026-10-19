-- Every WhatsApp call the AI held: who, which way, the transcript, a summary
-- (see internal/server/ai/history.go).
CREATE TABLE ai_calls (
    id          TEXT    PRIMARY KEY,
    at          INTEGER NOT NULL,
    person      TEXT    NOT NULL,
    name        TEXT    NOT NULL,
    direction   TEXT    NOT NULL, -- outgoing | incoming | follow-up | test
    incident_id TEXT    NOT NULL DEFAULT '',
    seconds     INTEGER NOT NULL,
    summary     TEXT    NOT NULL DEFAULT '',
    transcript  TEXT    NOT NULL, -- JSON lines
    report      TEXT    NOT NULL  -- JSON CallReport
);
CREATE INDEX ai_calls_at ON ai_calls (at);
CREATE INDEX ai_calls_incident ON ai_calls (incident_id);
