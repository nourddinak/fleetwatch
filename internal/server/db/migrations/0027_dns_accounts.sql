-- People's own DNS and CDN accounts (see internal/server/dns).
CREATE TABLE dns_accounts (
    id         TEXT    PRIMARY KEY,
    owner      TEXT    NOT NULL,   -- "owner" or a member ID
    data       TEXT    NOT NULL,   -- provider, label, zones, settings
    secret_enc BLOB    NOT NULL,   -- the API keys, encrypted
    created_at INTEGER NOT NULL
);
CREATE INDEX dns_accounts_owner ON dns_accounts (owner);
