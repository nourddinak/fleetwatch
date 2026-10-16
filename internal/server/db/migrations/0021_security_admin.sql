-- Two-factor authentication for team members, like the owner's (admin table).
ALTER TABLE members ADD COLUMN totp_secret_enc BLOB;
ALTER TABLE members ADD COLUMN totp_pending_enc BLOB;
ALTER TABLE members ADD COLUMN totp_last_step INTEGER NOT NULL DEFAULT 0;
-- '' = the owner's recovery codes, else the member's ID.
ALTER TABLE recovery_codes ADD COLUMN member_id TEXT NOT NULL DEFAULT '';

-- Addresses blocked on every server (see internal/server/hostadmin).
CREATE TABLE ip_blocks (
    ip         TEXT    PRIMARY KEY, -- address or CIDR range
    data       TEXT    NOT NULL,    -- JSON Block
    created_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL DEFAULT 0 -- 0 = until removed
);

-- Known vulnerabilities in each app's dependencies (see internal/server/vulns).
CREATE TABLE app_vulns (
    app_id     TEXT    PRIMARY KEY REFERENCES apps (id) ON DELETE CASCADE,
    server_id  TEXT    NOT NULL,
    data       TEXT    NOT NULL,
    scanned_at INTEGER NOT NULL
);

-- OSV advisories, cached for a week.
CREATE TABLE vuln_cache (
    id         TEXT    PRIMARY KEY,
    data       TEXT    NOT NULL,
    fetched_at INTEGER NOT NULL
);
