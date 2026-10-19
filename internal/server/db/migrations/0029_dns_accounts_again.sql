-- Cloudflare accounts people signed in with (see internal/server/dns); 0028 dropped the older table.
CREATE TABLE dns_accounts (
    id         TEXT    PRIMARY KEY,
    owner      TEXT    NOT NULL,   -- "owner" or a member ID
    data       TEXT    NOT NULL,   -- provider, label, zones, settings
    secret_enc BLOB    NOT NULL,   -- the OAuth tokens, encrypted
    created_at INTEGER NOT NULL
);
CREATE INDEX dns_accounts_owner ON dns_accounts (owner);
