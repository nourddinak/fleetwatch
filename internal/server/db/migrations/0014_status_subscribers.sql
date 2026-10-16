-- People and systems that get told when a status page changes.
CREATE TABLE status_subscribers (
    id           TEXT    PRIMARY KEY,
    page_id      TEXT    NOT NULL,
    kind         TEXT    NOT NULL,             -- email | webhook
    target       TEXT    NOT NULL,             -- address or URL
    token        TEXT    NOT NULL UNIQUE,      -- confirm and unsubscribe links
    base_url     TEXT    NOT NULL DEFAULT '',  -- where the visitor signed up, for links
    confirmed    INTEGER NOT NULL DEFAULT 0,
    created_at   INTEGER NOT NULL,
    last_sent_at INTEGER NOT NULL DEFAULT 0,
    last_error   TEXT    NOT NULL DEFAULT '',
    UNIQUE (page_id, kind, target)
);
CREATE INDEX status_subscribers_page ON status_subscribers (page_id, confirmed);
