-- Messages a team member asked the WhatsApp AI to pass on to another one
-- (see cmd/server/notes.go): delivered on that person's next call or chat.
CREATE TABLE whatsapp_notes (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    from_person  TEXT    NOT NULL,
    to_person    TEXT    NOT NULL,
    text         TEXT    NOT NULL,
    created_at   INTEGER NOT NULL,
    delivered_at INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX whatsapp_notes_pending ON whatsapp_notes (to_person, delivered_at);
