-- Latest certificate and registration check per site domain (data is JSON, see
-- internal/server/domains).
CREATE TABLE domain_checks (
    host            TEXT    PRIMARY KEY,
    data            TEXT    NOT NULL,
    checked_at      INTEGER NOT NULL,
    rdap_checked_at INTEGER NOT NULL DEFAULT 0
);
