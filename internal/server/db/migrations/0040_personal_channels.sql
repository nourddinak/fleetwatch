-- A team member's own alert channels (Telegram, Discord, webhook, email); settings
-- encrypted like the dashboard's own channels.
CREATE TABLE personal_channels (
    person     TEXT    NOT NULL,
    provider   TEXT    NOT NULL,
    enabled    INTEGER NOT NULL DEFAULT 0,
    config_enc BLOB,
    updated_at INTEGER NOT NULL,
    PRIMARY KEY (person, provider)
);
