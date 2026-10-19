-- How each dashboard user is reached on WhatsApp (see internal/server/whatsapp).
CREATE TABLE whatsapp_people (
    person TEXT PRIMARY KEY, -- "owner" or a member ID
    data   TEXT NOT NULL      -- phone, language, quiet hours
);
