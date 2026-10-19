-- What was already sent after an on-call page: the "resolved" message and the
-- follow-up call (see cmd/server/followups.go).
CREATE TABLE whatsapp_followups (
    incident_id   TEXT    NOT NULL,
    person        TEXT    NOT NULL,
    resolved_sent INTEGER NOT NULL DEFAULT 0,
    followed_up   INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (incident_id, person)
);
