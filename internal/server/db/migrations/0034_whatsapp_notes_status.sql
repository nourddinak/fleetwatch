-- Messages passed on can be canceled while they wait, and record how they went:
-- text (sent at once), call (the AI called), later (on the next call or chat).
ALTER TABLE whatsapp_notes ADD COLUMN canceled_at INTEGER NOT NULL DEFAULT 0;
ALTER TABLE whatsapp_notes ADD COLUMN via TEXT NOT NULL DEFAULT 'later';
